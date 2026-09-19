package capture

import (
	"context"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/gamerjp64/devgateway/internal/exchange"
)

// Record is the record of a request in flight. It is used by the goroutine
// that serves the request; only the request body, which the upstream
// transport also reads, has protection of its own.
type Record struct {
	rec *Recorder
	// enabled turns on observing the exchange; store, writing it to the
	// history.
	enabled, store bool
	seq            uint64
	start          time.Time

	w    http.ResponseWriter
	req  *http.Request
	cw   *writer
	body *bodyTap

	ex exchange.Exchange

	// upgradeStatus and upgradeHeader hold the response of a protocol
	// upgrade, which the ReverseProxy writes straight to the hijacked
	// connection, without going through the capture's writer.
	upgradeStatus int
	upgradeHeader http.Header

	upStart, upEnd time.Time
	// injected is the measured injected delay; injectedUp is the part of it
	// that happened inside the upstream window and is subtracted from it.
	injected, injectedUp time.Duration
	finished             bool
}

// Seq is the request's arrival sequence number within the process.
func (rec *Record) Seq() uint64 { return rec.seq }

// Enabled reports whether this request is being recorded in the history.
func (rec *Record) Enabled() bool { return rec.store }

// Writer is the ResponseWriter to use from here on: with recording on, it
// observes status, headers and body without changing what reaches the client.
func (rec *Record) Writer() http.ResponseWriter { return rec.w }

// Request is the request to use from here on: with recording on, the body is
// observed as it is read, and reaches whoever reads it in full.
func (rec *Record) Request() *http.Request { return rec.req }

// SetRoute notes the matched route and the destination upstream.
func (rec *Record) SetRoute(route, upstream string) {
	rec.ex.Route = route
	rec.ex.Upstream = upstream
}

// Intervene notes the override responsible ("route/override") and what it
// did: synthesized, delayed or dropped. Synthesizing or dropping sets the
// exchange's outcome.
func (rec *Record) Intervene(override, kind string) {
	if override != "" {
		rec.ex.Override = override
	}
	if !slices.Contains(rec.ex.Interventions, kind) {
		rec.ex.Interventions = append(rec.ex.Interventions, kind)
	}
	switch kind {
	case "synthesized":
		rec.ex.Outcome = exchange.OutcomeSynthesized
	case "dropped":
		rec.ex.Outcome = exchange.OutcomeDropped
	}
}

// Dropped notes a dropped connection and how it happened (hijack or
// stream_reset). The exchange is left with no response status.
func (rec *Record) Dropped(override, mode string) {
	rec.Intervene(override, "dropped")
	rec.ex.DropMode = mode
}

// Fail marks the exchange as an error response from the gateway itself (no
// route, no upstream, upstream unavailable or slow), with the description of
// the failure.
func (rec *Record) Fail(msg string) {
	rec.ex.Outcome = exchange.OutcomeGateway
	rec.ex.Error = msg
}

// Abort notes that the transfer was interrupted after it had started, without
// changing who produced the response.
func (rec *Record) Abort(msg string) {
	// A drop caused by an override ends the handler through the same panic as
	// an interrupted transfer, but it is not a failure.
	if rec.ex.Outcome == exchange.OutcomeDropped {
		return
	}
	if rec.ex.Error == "" {
		rec.ex.Error = msg
	}
}

// Upgrade notes the response of a protocol upgrade (101 Switching Protocols,
// as in WebSocket). On that path the ReverseProxy hijacks the connection and
// writes the response to it without calling the capture writer's WriteHeader;
// without this note the exchange would end up with status 200 and no headers.
// A response written by the writer after that (the upgrade's failure, for
// instance) wins.
func (rec *Record) Upgrade(status int, h http.Header) {
	if !rec.enabled {
		return
	}
	rec.upgradeStatus = status
	rec.upgradeHeader = sentHeader(h)
}

// UpstreamStarted marks the request being sent to the upstream.
func (rec *Record) UpstreamStarted() {
	if rec.upStart.IsZero() {
		rec.upStart = rec.rec.now()
	}
}

// UpstreamDone marks the end of the contact with the upstream: the end of the
// response body or the failure. With no UpstreamStarted before it, it has no
// effect. With a streaming body, the end of the body includes waiting on a
// slow client (see exchange.Timing).
func (rec *Record) UpstreamDone() {
	if !rec.upStart.IsZero() && rec.upEnd.IsZero() {
		rec.upEnd = rec.rec.now()
	}
}

// Delay is the delay injection point: called between the response being ready
// and the write to the client, it waits d and counts the time actually waited
// as injected. With d zero it does nothing. Returns the context's error if
// the client gives up during the wait.
func (rec *Record) Delay(ctx context.Context, override string, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	rec.Intervene(override, "delayed")
	t0 := rec.rec.now()
	t := time.NewTimer(d)
	defer t.Stop()
	var err error
	select {
	case <-t.C:
	case <-ctx.Done():
		err = ctx.Err()
	}
	waited := rec.rec.now().Sub(t0)
	rec.injected += waited
	if !rec.upStart.IsZero() && rec.upEnd.IsZero() {
		rec.injectedUp += waited
	}
	return err
}

// DrainMaxBytes caps how much DrainRequest reads beyond the capture limit. A
// body larger than that ends up with the size declared in Content-Length,
// when there is one, or with whatever was read, and marked as truncated.
const DrainMaxBytes = 64 << 20

// DrainRequest reads the request body to the end when no one is going to
// forward it (a response from the gateway itself), keeping only the
// beginning, up to the capture limit, and counting the real size, including
// that of a body with no Content-Length. It must not be called after the
// request has gone to the upstream.
func (rec *Record) DrainRequest() {
	if rec.body == nil {
		return
	}
	rec.body.mu.Lock()
	eof := rec.body.eof
	rec.body.mu.Unlock()
	if !eof {
		io.Copy(io.Discard, io.LimitReader(rec.body, int64(rec.body.tap.limit)+DrainMaxBytes))
	}
}

// Finish closes the record with the broken-down timings and, with recording
// on, hands the exchange over to be written, without waiting for it. Repeated
// calls have no effect.
func (rec *Record) Finish() {
	if rec.finished {
		return
	}
	rec.finished = true
	defer rec.rec.finished()
	end := rec.rec.now()
	if !rec.enabled {
		return
	}
	rec.UpstreamDone()
	e := &rec.ex

	var upstream time.Duration
	if !rec.upStart.IsZero() {
		upstream = max(rec.upEnd.Sub(rec.upStart)-rec.injectedUp, 0)
	}
	total := end.Sub(rec.start)
	e.Timing = exchange.Timing{
		TotalMs:    exchange.Ms(total),
		UpstreamMs: exchange.Ms(upstream),
		InjectedMs: exchange.Ms(rec.injected),
		GatewayMs:  exchange.Ms(max(total-upstream-rec.injected, 0)),
	}

	if rec.body != nil {
		e.Request = rec.body.message(rec.req.ContentLength, e.Request.Headers)
	}
	cw := rec.cw
	header := cw.header
	switch {
	case e.Outcome == exchange.OutcomeDropped:
		e.Status = 0
	case cw.status != 0:
		e.Status = cw.status
	case rec.upgradeStatus != 0:
		e.Status, header = rec.upgradeStatus, rec.upgradeHeader
	case e.Error == "":
		// The handler ended without writing: the server answers an empty 200.
		e.Status = http.StatusOK
	}
	e.Response = cw.tap.message(header)
	if rec.store {
		rec.rec.enqueue(e)
	}
}

// Exchange returns the observed exchange, after Finish. False when the
// exchange was not observed. The copy shares headers and bodies with the
// exchange handed over to be written and must be treated as read-only.
func (rec *Record) Exchange() (exchange.Exchange, bool) {
	if !rec.enabled || !rec.finished {
		return exchange.Exchange{}, false
	}
	return rec.ex, true
}

// tap keeps the beginning of a body, up to limit bytes, and counts the real
// size.
type tap struct {
	limit int
	buf   []byte
	size  int64
}

func (t *tap) add(p []byte) {
	t.size += int64(len(p))
	if room := t.limit - len(t.buf); room > 0 {
		t.buf = append(t.buf, p[:min(room, len(p))]...)
	}
}

func (t *tap) message(h http.Header) exchange.Message {
	return exchange.Message{
		Headers:   h,
		Body:      t.buf,
		Size:      t.size,
		Truncated: t.size > int64(len(t.buf)),
	}
}

// bodyTap observes the request body as it is read. The upstream transport may
// read it in a goroutine of its own, including after the ReverseProxy
// returns, which is why the state is guarded.
type bodyTap struct {
	rc  io.ReadCloser
	mu  sync.Mutex
	tap tap
	eof bool
}

func (b *bodyTap) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.mu.Lock()
	b.tap.add(p[:n])
	if err == io.EOF {
		b.eof = true
	}
	b.mu.Unlock()
	return n, err
}

func (b *bodyTap) Close() error { return b.rc.Close() }

// message builds the request side. A body that was not read to the end gets
// the size declared in Content-Length, when there is one, and is marked as
// truncated, because the capture did not see all of it.
func (b *bodyTap) message(contentLength int64, h http.Header) exchange.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.tap.message(h)
	m.Body = slices.Clone(m.Body)
	if !b.eof {
		m.Size = max(m.Size, contentLength)
		m.Truncated = true
	}
	return m
}

// writer observes the response on its way to the client. It implements Flush
// and Unwrap so that streaming stays incremental and so that the ReverseProxy
// can still reach the server's Hijacker on a protocol upgrade.
type writer struct {
	http.ResponseWriter
	status int
	header http.Header
	tap    tap
}

func (w *writer) WriteHeader(code int) {
	// Informational responses go through without fixing the status; 101 is
	// final.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.header = sentHeader(w.ResponseWriter.Header())
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *writer) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
		w.header = sentHeader(w.ResponseWriter.Header())
	}
	n, err := w.ResponseWriter.Write(p)
	w.tap.add(p[:n])
	return n, err
}

// FlushError passes the Flush on to the server's ResponseWriter.
func (w *writer) FlushError() error {
	return http.NewResponseController(w.ResponseWriter).Flush()
}

// Flush serves whoever uses the http.Flusher interface directly.
func (w *writer) Flush() { w.FlushError() }

func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// sentHeader copies the headers as they go to the client. A key with an empty
// list is not sent — net/http uses it to suppress an automatic header, such
// as the Content-Type guessed from the body — and so it is left out of the
// capture, instead of showing up as null in the API.
func sentHeader(h http.Header) http.Header {
	out := make(http.Header, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = slices.Clone(v)
		}
	}
	return out
}
