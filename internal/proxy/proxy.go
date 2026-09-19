// Package proxy serves the traffic port: it resolves the route and forwards
// to the upstream over httputil.ReverseProxy.
package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gamerjp64/devgateway/internal/capture"
	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/exchange"
	"github.com/gamerjp64/devgateway/internal/override"
	"github.com/gamerjp64/devgateway/internal/upstream"
)

// DefaultTimeout applies to routes that declare no timeout.
const DefaultTimeout = 30 * time.Second

// Handler is the handler for the traffic port.
type Handler struct {
	live      *config.Live
	rec       *capture.Recorder
	transport http.RoundTripper
	// delayFor decides the delay to inject between the response being ready
	// and the write to the client, along with the override responsible.
	// Without it, or without an override that delays, the delay is zero.
	delayFor func(*http.Request) (override string, d time.Duration)

	tracker   *override.Tracker
	learner   Learner
	upstreams *upstream.Health
}

// Learner learns endpoints from the exchanges answered by the upstream.
// Observe is called at the end of every request while learning mode is on
// and must not block: the write happens off the request path.
type Learner interface {
	Observe(route *config.CompiledRoute, e exchange.Exchange)
}

// Options rounds out the handler.
type Options struct {
	// Tracker holds the live state of the overrides (expiry by time and by
	// count). Without one, the handler creates its own.
	Tracker *override.Tracker
	// Learner receives the exchanges from learning mode. Without one,
	// nothing is learned.
	Learner Learner
	// Upstreams receives the result of every forwarding attempt, feeding the
	// recent availability of the upstreams. Without it, nothing is counted.
	Upstreams *upstream.Health
}

// NewHandler serves traffic with the configuration currently in effect in
// live and records the exchanges in rec.
func NewHandler(live *config.Live, rec *capture.Recorder) *Handler {
	return NewHandlerWith(live, rec, Options{})
}

// NewHandlerWith is NewHandler with the given options.
func NewHandlerWith(live *config.Live, rec *capture.Recorder, o Options) *Handler {
	if o.Tracker == nil {
		o.Tracker = override.NewTracker(live)
	}
	return &Handler{live: live, rec: rec, transport: newTransport(), tracker: o.Tracker, learner: o.Learner, upstreams: o.Upstreams}
}

// Tracker returns the live override state used by the handler.
func (h *Handler) Tracker() *override.Tracker { return h.tracker }

func newTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil, // the gateway talks straight to the upstream, with no proxy from the environment
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Without this the Transport would ask the upstream for gzip on its
		// own and decompress the response, adding an Accept-Encoding the
		// client never sent and changing the body it receives.
		DisableCompression: true,
	}
}

// ServeHTTP walks the request path in the fixed order laid out by the design:
// resolve the route (1), open the capture record (2), resolve the most
// specific enabled override that matches (3), draw for application, drop and
// delay (4), carry on as if the override did not exist when the application
// is not drawn (5), synthesize the declared response or forward to the
// upstream (7), apply the delay between the response being ready and the
// write to the client (8) and close the record with the timings broken down
// (9), handing the exchange over to learning while it is on. A connection
// drop (6) ends the request before any response.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// The snapshot is taken once and only once: a reload in the middle of the
	// request does not affect it.
	snap := h.live.Load()
	route := Resolve(snap, r)

	learning := h.learner != nil && snap.Settings.LearningEnabled && route != nil && route.Upstream != nil
	rec := h.rec.Begin(w, r, capture.Options{
		Record:       snap.Settings.HistoryRecord,
		Observe:      learning,
		MaxBodyBytes: snap.Settings.CaptureMaxBodyBytes,
	})
	defer func() {
		// An interrupted body copy makes the ReverseProxy abort the handler
		// with a panic, and so does a drop over HTTP/2; the exchange is
		// recorded before the panic carries on.
		if p := recover(); p != nil {
			rec.Abort("the response transfer was interrupted")
			rec.Finish()
			panic(p)
		}
		rec.Finish()
		// Step 9: learning receives the exchange already answered and writes
		// off the request path.
		if learning {
			if e, ok := rec.Exchange(); ok {
				// With recording off the exchange never reaches the history,
				// so the learned override cannot point at it.
				if !rec.Enabled() {
					e.ID = ""
				}
				h.learner.Observe(route, e)
			}
		}
	}()
	w, r = rec.Writer(), rec.Request()

	if route == nil {
		rec.DrainRequest()
		d := writeNoRoute(w, snap)
		rec.Fail(d.Error + ": " + d.Message)
		return
	}
	rec.SetRoute(route.Name(), route.Doc.Upstream)

	dec := h.decide(snap, route, &r, rec)

	// Step 6: the drop takes precedence over the declared response.
	if o := dec.Applied(); o != nil && dec.Drop {
		h.drop(w, r, o, rec)
		return
	}

	// Step 7.
	if o := dec.Applied(); o != nil && o.Doc.Respond != nil {
		h.synthesize(w, r, route, dec, rec)
		return
	}
	if route.Upstream == nil {
		rec.DrainRequest()
		d := diag{
			Error:   "no_upstream",
			Message: "the route declares no upstream and no override intercepted the request",
			Route:   route.Name(),
		}
		rec.Fail(d.Error + ": " + d.Message)
		id := h.ident(route, dec, "")
		if h.delay(r, dec, rec) != nil {
			return
		}
		writeDiag(w, http.StatusNotImplemented, id, d)
		return
	}
	h.forward(w, r, route, dec, rec)
}

// decide runs steps 3 through 5. Step 3: among the enabled, unexpired
// overrides that select the request, the most specific one wins. A body
// criterion reads the request; *r then becomes a request that hands the body
// over again, intact, to whoever forwards it. Steps 4 and 5: the draw happens
// before any contact with the upstream, so that the same seed produces the
// same decisions whatever the upstream's availability. With no application
// drawn, dec.Applied is nil and the request carries on as if the override did
// not exist.
//
// The drawn application is counted against the override's live state. If,
// between selection and counting, the override ran out because of another
// concurrent request, it drops out of the selection and the choice is made
// again, from the same source derived from (seed, sequence), as if it had
// already been expired on the way in.
func (h *Handler) decide(snap *config.Snapshot, route *config.CompiledRoute, r **http.Request, rec *capture.Record) override.Decision {
	q := override.NewRequest(*r)
	defer func() { *r = q.Request() }()
	var exhausted []*config.CompiledOverride
	for {
		selected := override.SelectWhere(route, q, func(o *config.CompiledOverride) bool {
			return !slices.Contains(exhausted, o) && h.tracker.Active(o)
		})
		if selected == nil {
			return override.Decision{}
		}
		dec := override.Decide(selected, override.Source(snap.Settings.Seed, rec.Seq()))
		if !dec.Apply || h.tracker.Claim(selected) {
			return dec
		}
		exhausted = append(exhausted, selected)
	}
}

// drop ends the connection without sending a response. Over HTTP/1.x the
// socket is hijacked and closed with a reset; over HTTP/2, where the request
// has no socket of its own, the handler is aborted with http.ErrAbortHandler
// and the server cancels the stream. The capture records which of the two
// happened. The request body is read first, so that it shows up in the
// capture.
func (h *Handler) drop(w http.ResponseWriter, r *http.Request, o *config.CompiledOverride, rec *capture.Record) {
	rec.DrainRequest()
	mode := exchange.DropStreamReset
	if r.ProtoMajor < 2 {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err == nil {
			rec.Dropped(o.ID(), exchange.DropHijack)
			if tc, ok := conn.(*net.TCPConn); ok {
				// No waiting on pending sends: the client sees the reset.
				tc.SetLinger(0)
			}
			conn.Close()
			return
		}
		mode = exchange.DropAbort
	}
	rec.Dropped(o.ID(), mode)
	panic(http.ErrAbortHandler)
}

// synthesize answers with the response declared by the override, with no
// contact with the upstream. The request body is read to the end so that it
// shows up in the capture.
func (h *Handler) synthesize(w http.ResponseWriter, r *http.Request, route *config.CompiledRoute, dec override.Decision, rec *capture.Record) {
	o := dec.Override
	rec.DrainRequest()
	rec.Intervene(o.ID(), "synthesized")
	hdr := w.Header()
	override.SetHeaders(hdr, o)
	hdr.Set(HeaderGateway, h.ident(route, dec, "synthesized").String())
	// Step 8: the response is ready; the delay comes before writing it.
	if h.delay(r, dec, rec) != nil {
		// Nothing reached the client: the exchange is left without a status
		// and with the give-up noted, just as on a forward.
		rec.Abort("client_canceled: the client gave up during the injected delay")
		return
	}
	w.WriteHeader(override.Status(o))
	w.Write(override.Body(o))
}

// ident builds the response's X-Gateway from the interventions applied, in
// the order they happen: synthesized when the override synthesized the
// response, delayed when it delayed it, and both, separated by a comma
// ("synthesized,delayed"), when it did both. With no intervention the header
// identifies only the route.
func (h *Handler) ident(route *config.CompiledRoute, dec override.Decision, kind string) Ident {
	id := Ident{Route: route.Name()}
	o := dec.Applied()
	if o == nil {
		return id
	}
	if dec.Delay > 0 {
		if kind != "" {
			kind += ","
		}
		kind += "delayed"
	}
	if kind != "" {
		id.Override, id.Intervention = o.ID(), kind
	}
	return id
}

// delay applies the delay drawn for the request, at the point between the
// response being ready and the write to the client. The delayFor hook, when
// present, decides in place of the draw.
func (h *Handler) delay(r *http.Request, dec override.Decision, rec *capture.Record) error {
	if h.delayFor != nil {
		name, d := h.delayFor(r)
		return rec.Delay(r.Context(), name, d)
	}
	o := dec.Applied()
	if o == nil {
		return nil
	}
	return rec.Delay(r.Context(), o.ID(), dec.Delay)
}

// forward forwards to the upstream. The route's timeout applies until the
// response headers arrive: past that point the body may well be a long
// stream.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, route *config.CompiledRoute, dec override.Decision, rec *capture.Record) {
	timeout := DefaultTimeout
	if route.Doc.Timeout != nil {
		timeout = time.Duration(*route.Doc.Timeout)
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		timedOut.Store(true)
		cancel()
	})
	defer timer.Stop()

	id := Ident{Route: route.Name()}
	rewrite := rewriteFor(route, id)
	rp := &httputil.ReverseProxy{
		Transport: h.transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			rewrite(pr)
			rec.UpstreamStarted()
		},
		ModifyResponse: func(res *http.Response) error {
			timer.Stop()
			// The upstream answered, whatever the status.
			h.upstreams.Success(route.Doc.Upstream)
			res.Header.Set(HeaderGateway, h.ident(route, dec, "").String())
			// The response is ready; the delay comes before writing it.
			if err := h.delay(r, dec, rec); err != nil {
				return err
			}
			if res.StatusCode == http.StatusSwitchingProtocols {
				// On an upgrade the ReverseProxy writes the response straight
				// to the hijacked connection, bypassing the capture's writer.
				rec.Upgrade(res.StatusCode, res.Header)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			rec.UpstreamDone()
			var d diag
			status := 0
			switch {
			case timedOut.Load():
				d = diag{
					Error:    "upstream_timeout",
					Message:  "the upstream exceeded the timeout of " + timeout.String(),
					Route:    route.Name(),
					Upstream: route.Doc.Upstream,
				}
				status = http.StatusGatewayTimeout
				h.upstreams.Failure(route.Doc.Upstream, d.Message)
			case errors.Is(err, context.Canceled):
				// The client gave up (including during the delay injected in
				// ModifyResponse); there is nobody left to answer.
				d = diag{Error: "client_canceled", Message: "the client gave up before the response"}
			default:
				d = diag{
					Error:    "upstream_unavailable",
					Message:  "could not get a response from the upstream: " + err.Error(),
					Route:    route.Name(),
					Upstream: route.Doc.Upstream,
				}
				status = http.StatusBadGateway
				h.upstreams.Failure(route.Doc.Upstream, err.Error())
			}
			rec.Fail(d.Error + ": " + d.Message)
			if status == 0 {
				return
			}
			// Step 8 applies to the gateway's own error response too: once the
			// response is ready, the override's delay comes before writing it.
			// In these cases ModifyResponse was never called and the delay has
			// not been applied yet.
			if h.delay(r, dec, rec) != nil {
				rec.Fail("client_canceled: the client gave up during the injected delay")
				return
			}
			writeDiag(w, status, h.ident(route, dec, ""), d)
		},
	}
	rp.ServeHTTP(w, r.WithContext(ctx))
	rec.UpstreamDone()
}

// forwardingHeaders are the headers that the ReverseProxy's Rewrite strips
// from the outgoing request before calling the rewrite function.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"}

// incomingForwarding copies from the incoming request the forwarding headers
// that arrived filled in. The ones the client declared in Connection are
// hop-by-hop on that connection and are left out.
func incomingForwarding(in http.Header) http.Header {
	hop := map[string]bool{}
	for _, v := range in.Values("Connection") {
		for tok := range strings.SplitSeq(v, ",") {
			if tok = strings.TrimSpace(tok); tok != "" {
				hop[http.CanonicalHeaderKey(tok)] = true
			}
		}
	}
	out := http.Header{}
	for _, k := range forwardingHeaders {
		if v, ok := in[k]; ok && !hop[k] {
			out[k] = slices.Clone(v)
		}
	}
	return out
}

// filled reports whether any of the header's values has content.
func filled(v []string) bool {
	return slices.ContainsFunc(v, func(s string) bool { return strings.TrimSpace(s) != "" })
}

func rewriteFor(route *config.CompiledRoute, id Ident) func(*httputil.ProxyRequest) {
	return func(pr *httputil.ProxyRequest) {
		if route.Doc.StripPrefix {
			u := pr.Out.URL
			u.Path = route.Path.Strip(u.Path)
			if u.RawPath != "" {
				u.RawPath = route.Path.Strip(u.RawPath)
			}
		}
		pr.SetURL(route.Upstream)
		// Rewrite discards the incoming forwarding headers. Putting
		// X-Forwarded-For back before SetXForwarded makes the client address
		// be appended to the chain; the others that arrived filled in are
		// restored afterwards, taking precedence over whatever SetXForwarded
		// filled in, which then only applies to the ones that are missing. An
		// X-Forwarded-* that arrived empty does not count as filled in and
		// keeps the computed value; Forwarded, which SetXForwarded does not
		// fill in, is restored exactly as it arrived.
		in := incomingForwarding(pr.In.Header)
		if v := in["X-Forwarded-For"]; filled(v) {
			pr.Out.Header["X-Forwarded-For"] = v
		}
		pr.SetXForwarded()
		if v, ok := in["Forwarded"]; ok {
			pr.Out.Header["Forwarded"] = v
		}
		for _, k := range []string{"X-Forwarded-Host", "X-Forwarded-Proto"} {
			if v := in[k]; filled(v) {
				pr.Out.Header[k] = v
			}
		}
		// SetURL swaps the Host for the upstream's; without rewriteHost the
		// original Host is put back so the upstream receives it as it arrived.
		if !route.Doc.RewriteHost {
			pr.Out.Host = pr.In.Host
		}
		pr.Out.Header.Set(HeaderGateway, id.String())
	}
}

// diag is the body of the responses the gateway produces itself.
type diag struct {
	Error    string   `json:"error"`
	Message  string   `json:"message"`
	Route    string   `json:"route,omitempty"`
	Upstream string   `json:"upstream,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
}

func writeDiag(w http.ResponseWriter, status int, id Ident, d diag) {
	w.Header().Set(HeaderGateway, id.String())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(d)
}

func writeNoRoute(w http.ResponseWriter, snap *config.Snapshot) diag {
	patterns := make([]string, 0, len(snap.Routes))
	for _, r := range snap.Routes {
		patterns = append(patterns, r.Pattern())
	}
	d := diag{
		Error:    "no_route",
		Message:  "no route matched the request",
		Patterns: patterns,
	}
	writeDiag(w, http.StatusNotFound, Ident{}, d)
	return d
}
