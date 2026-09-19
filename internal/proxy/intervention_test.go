package proxy

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/exchange"
)

func latency(d time.Duration) *config.Latency {
	c := config.Duration(d)
	return &config.Latency{Fixed: &c}
}

func latencyRange(lo, hi time.Duration) *config.Latency {
	a, b := config.Duration(lo), config.Duration(hi)
	return &config.Latency{Min: &a, Max: &b}
}

// timedGet measures how long the complete response took to arrive.
func timedGet(t *testing.T, url string) (*http.Response, []byte, time.Duration) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	start := time.Now()
	res, body := do(t, req)
	return res, body, time.Since(start)
}

// swapRoutes publishes a new snapshot with the given routes, like a reload.
func (g *capGW) swapRoutes(t *testing.T, routes ...config.Route) {
	t.Helper()
	var docs []config.RouteDoc
	for _, r := range routes {
		docs = append(docs, config.NewRouteDoc(r.Name+".yaml", r))
	}
	compiled, err := config.BuildRoutes(docs)
	if err != nil {
		t.Fatal(err)
	}
	g.h.live.Swap(config.NewSnapshot(g.h.live.Load().Settings, compiled, nil))
}

// Requirement: Latency and connection drop

func TestFixedLatency(t *testing.T) {
	o := synth("slow", "/api/*", "delayed")
	o.Latency = latency(2 * time.Second)
	g := capturing(t, recording(), route("payments", "", "/api/*", withOverrides(o)))
	res, body, el := timedGet(t, g.URL+"/api/x")
	if string(body) != "delayed" {
		t.Fatalf("unexpected body: %q", body)
	}
	if el < 2*time.Second {
		t.Fatalf("the response should arrive at least 2s later, it arrived in %v", el)
	}
	// The response was synthesized and delayed: the header and the exchange
	// name both interventions.
	if got := res.Header.Get(HeaderGateway); got != "route=payments; override=payments/slow; intervention=synthesized,delayed" {
		t.Fatalf("unexpected X-Gateway: %q", got)
	}
	e := g.only(t)
	approx(t, "injected time", e.Timing.InjectedMs, 2000, 2300)
	if !slices.Equal(e.Interventions, []string{"synthesized", "delayed"}) {
		t.Fatalf("the exchange should record both the synthesis and the delay: %v", e.Interventions)
	}
}

func TestLatencyDrawnWithinRange(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := synth("variable", "/api/*", "ok")
	o.Latency = latencyRange(100*time.Millisecond, 500*time.Millisecond)
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	const n = 50
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			res, err := http.Get(g.URL + "/api/x")
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		})
	}
	wg.Wait()
	items := g.history(t, exchange.Filter{})
	if len(items) != n {
		t.Fatalf("want %d exchanges, got %d", n, len(items))
	}
	seen := map[float64]bool{}
	for _, e := range items {
		// The measured delay is the drawn one plus the timer's imprecision.
		if e.Timing.InjectedMs < 100 || e.Timing.InjectedMs > 600 {
			t.Errorf("delay of %.1fms is outside the 100ms to 500ms range", e.Timing.InjectedMs)
		}
		seen[e.Timing.InjectedMs] = true
	}
	if len(seen) < 2 {
		t.Fatalf("the drawn delays should not all be the same: %v", seen)
	}
}

func TestDropEndsWithoutResponse(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	// The drop takes precedence over the declared response.
	o := synth("drop", "/api/*", "never sent")
	o.Respond.Status = http.StatusServiceUnavailable
	o.Drop = config.Drop{On: true}
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	res, err := http.Get(g.URL + "/api/x")
	if err == nil {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("the client should get no response, it got %d %q", res.StatusCode, body)
	}
	if hits.Load() != 0 {
		t.Fatalf("the upstream should not be contacted, it got %d", hits.Load())
	}
	// A connection drop is recorded (traffic-capture).
	e := g.only(t)
	if e.Outcome != exchange.OutcomeDropped || e.Status != 0 || e.DropMode != exchange.DropHijack ||
		e.Override != "payments/drop" || !slices.Equal(e.Interventions, []string{"dropped"}) || e.Error != "" {
		t.Fatalf("the exchange should show up as ended by a drop, with no status: %+v", e)
	}
	if len(e.Response.Body) != 0 {
		t.Fatalf("no body should be part of the response: %q", e.Response.Body)
	}
	// The server keeps serving after the drop.
	if _, body := g.get(t, "/outside"); len(body) == 0 {
		t.Fatal("the gateway should keep answering")
	}
}

// Over HTTP/2 there is no socket to hijack: the stream is cancelled abruptly
// and the capture records that mode.
func TestDropOverHTTP2ResetsStream(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("drop", "/api/*", "never sent")
	o.Drop = config.Drop{On: true}
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	srv := httptest.NewUnstartedServer(g.h)
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = discardLog()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client := srv.Client()

	// A request with no override confirms the connection is HTTP/2.
	res, err := client.Get(srv.URL + "/outside")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.ProtoMajor != 2 {
		t.Fatalf("this test requires HTTP/2, the connection is %s", res.Proto)
	}

	res, err = client.Get(srv.URL + "/api/x")
	if err == nil {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("the client should get no response, it got %d %q", res.StatusCode, body)
	}
	if hits.Load() != 0 {
		t.Fatalf("the upstream should not be contacted, it got %d", hits.Load())
	}
	items := g.history(t, exchange.Filter{Override: "payments/drop"})
	if len(items) != 1 {
		t.Fatalf("want one dropped exchange, got %d", len(items))
	}
	if e := items[0]; e.Outcome != exchange.OutcomeDropped || e.Status != 0 || e.DropMode != exchange.DropStreamReset || e.Error != "" {
		t.Fatalf("the exchange should show up as a cancelled stream, with no status: %+v", e)
	}
}

func TestLatencyOnlyDelaysUpstreamResponse(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := config.Override{Name: "slow", Match: config.OverrideMatch{Path: "/api/*"}, Latency: latency(300 * time.Millisecond)}
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	res, body, el := timedGet(t, g.URL+"/api/x")
	if e := echoOf(t)(res, body); e.Path != "/api/x" {
		t.Fatalf("the request should be forwarded as usual: %+v", e)
	}
	if hits.Load() != 1 {
		t.Fatalf("the upstream should receive the request, it got %d", hits.Load())
	}
	if el < 300*time.Millisecond {
		t.Fatalf("the upstream response should arrive after the delay, it arrived in %v", el)
	}
	if got := res.Header.Get(HeaderGateway); got != "route=payments; override=payments/slow; intervention=delayed" {
		t.Fatalf("X-Gateway should identify the delay: %q", got)
	}
	e := g.only(t)
	if e.Outcome != exchange.OutcomeUpstream || !slices.Equal(e.Interventions, []string{"delayed"}) || e.Override != "payments/slow" {
		t.Fatalf("the exchange should come from the upstream with the delay noted: %+v", e)
	}
	approx(t, "injected time", e.Timing.InjectedMs, 300, 500)
}

// The "Injected time separated from real time" scenario of the
// traffic-capture spec, now with a real override.
func TestInjectedTimeSeparatedFromUpstreamWithOverride(t *testing.T) {
	const upstreamDelay = 150 * time.Millisecond
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(upstreamDelay)
		io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	o := config.Override{Name: "slow", Match: config.OverrideMatch{Path: "/*"}, Latency: latency(2 * time.Second)}
	g := capturing(t, recording(), route("payments", up.URL, "/*", withOverrides(o)))
	_, body, el := timedGet(t, g.URL+"/x")
	if string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
	if el < upstreamDelay+2*time.Second {
		t.Fatalf("the delay should add to the upstream time; the request took %v", el)
	}
	tm := g.only(t).Timing
	approx(t, "injected time", tm.InjectedMs, 2000, 2200)
	approx(t, "upstream time", tm.UpstreamMs, 150, 300)
	if sum := tm.UpstreamMs + tm.InjectedMs + tm.GatewayMs; sum < tm.TotalMs-0.001 || sum > tm.TotalMs+0.001 {
		t.Errorf("the total should be the sum of upstream, injected and overhead: %+v", tm)
	}
}

// Requirement: Expiry by time and by count (end to end)

func TestOverrideExpiresByCountEndToEnd(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("twice", "/api/*", "override")
	o.MaxApplications = ptr(2)
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	var got []string
	for range 3 {
		_, body := g.get(t, "/api/x")
		got = append(got, string(body))
	}
	if got[0] != "override" || got[1] != "override" || got[2] == "override" {
		t.Fatalf("the first two should come from the override and the third from the upstream: %q", got)
	}
	if hits.Load() != 1 {
		t.Fatalf("only the third should reach the upstream, %d did", hits.Load())
	}
	s, ok := g.h.Tracker().State("payments", "twice")
	if !ok || s.Applications != 2 || s.Active || s.Expired == nil || *s.Expired != "applications" {
		t.Fatalf("the query should report two applications and the expiry: %+v", s)
	}
}

func TestOverrideExpiresByTimeEndToEnd(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("brief", "/api/*", "override")
	o.TTL = ptr(config.Duration(300 * time.Millisecond))
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	if _, body := g.get(t, "/api/x"); string(body) != "override" {
		t.Fatalf("within its lifetime the override should apply: %q", body)
	}
	s, _ := g.h.Tracker().State("payments", "brief")
	if s.TTLRemainingMs == nil || *s.TTLRemainingMs <= 0 || *s.TTLRemainingMs > 300 {
		t.Fatalf("the remaining lifetime should be queryable: %+v", s)
	}
	time.Sleep(400 * time.Millisecond)
	echoOf(t)(g.get(t, "/api/x"))
	if hits.Load() != 1 {
		t.Fatalf("past its lifetime the request should go back to the upstream, %d did", hits.Load())
	}
}

// The application limit holds under concurrency, and the exhausted override
// drops out of the precedence chain: the less specific one takes over.
func TestCountLimitUnderConcurrencyFallsBackToLessSpecific(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	exact := synth("exact", "/api/x", "exact")
	exact.MaxApplications = ptr(5)
	g := capturing(t, recording(), route("payments", up.URL, "/api/*",
		withOverrides(exact, synth("wildcard", "/api/*", "wildcard"))))
	var mu sync.Mutex
	count := map[string]int{}
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			res, err := http.Get(g.URL + "/api/x")
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			mu.Lock()
			count[string(b)]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if count["exact"] != 5 || count["wildcard"] != 35 {
		t.Fatalf("want 5 responses from the exact one and 35 from the wildcard: %v", count)
	}
}

// Reloading without changing the override preserves the count.
func TestExpirationStatePreservedAcrossReload(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("twice", "/api/*", "override")
	o.MaxApplications = ptr(2)
	r := route("payments", up.URL, "/api/*", withOverrides(o))
	g := capturing(t, recording(), r)
	g.get(t, "/api/x")
	g.swapRoutes(t, r, route("orders", up.URL, "/orders/*"))
	g.get(t, "/api/x")
	echoOf(t)(g.get(t, "/api/x"))
	if hits.Load() != 1 {
		t.Fatalf("the reload should not reset the count; %d reached the upstream", hits.Load())
	}
}

// Requirement: Intervention identification

func TestUpstreamErrorNotMarkedInHeader(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "failed", http.StatusInternalServerError)
	}))
	t.Cleanup(up.Close)
	// An override that does not match changes nothing.
	g := capturing(t, recording(), route("payments", up.URL, "/api/*",
		withOverrides(synth("other", "/api/other", "x"))))
	res, _ := g.get(t, "/api/x")
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("want a 500 from the upstream, got %d", res.StatusCode)
	}
	if got := res.Header.Values(HeaderGateway); len(got) != 1 || got[0] != "route=payments" {
		t.Fatalf("X-Gateway should identify the route only: %q", got)
	}
	if e := g.only(t); e.Intervened() || e.Override != "" || e.Outcome != exchange.OutcomeUpstream {
		t.Fatalf("the exchange should carry no intervention mark: %+v", e)
	}
}

// The "Filter by intervention" scenario of the traffic-capture spec, with
// exchanges synthesized and delayed by real overrides.
func TestHistoryFilterByInterventionWithRealOverrides(t *testing.T) {
	up := statusUpstream(t)
	slow := config.Override{Name: "slow", Match: config.OverrideMatch{Path: "/api/payments/slow"}, Latency: latency(time.Millisecond)}
	flaky := config.Override{Name: "flaky", Match: config.OverrideMatch{Path: "/api/payments/synthesized"},
		Respond: &config.Respond{Status: http.StatusServiceUnavailable}}
	g := capturing(t, recording(), route("payments", up.URL, "/api/payments/*", withOverrides(slow, flaky)))
	g.get(t, "/api/payments/normal")
	g.get(t, "/api/payments/slow")
	g.get(t, "/api/payments/synthesized")
	g.get(t, "/api/payments/error?status=500")
	yes, no := true, false
	with := g.history(t, exchange.Filter{Intervened: &yes})
	if len(with) != 2 {
		t.Fatalf("the intervention filter should return the synthesized and the delayed one: %v", with)
	}
	synthesized, delayed := with[0], with[1]
	if synthesized.Path != "/api/payments/synthesized" || synthesized.Outcome != exchange.OutcomeSynthesized ||
		synthesized.Override != "payments/flaky" || synthesized.Status != http.StatusServiceUnavailable {
		t.Fatalf("the synthesized exchange should show up in the filter: %+v", synthesized)
	}
	if delayed.Path != "/api/payments/slow" || delayed.Override != "payments/slow" || delayed.Outcome != exchange.OutcomeUpstream {
		t.Fatalf("the delayed exchange should show up in the filter: %+v", delayed)
	}
	if without := g.history(t, exchange.Filter{Intervened: &no}); len(without) != 2 {
		t.Fatalf("want 2 exchanges without intervention, got %d", len(without))
	}
}

// Requirement: Override on and off

func TestDisabledOverrideDoesNotIntercept(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("off", "/api/*", "unavailable")
	o.Respond.Status = http.StatusServiceUnavailable
	o.On = ptr(false)
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	res, body := g.get(t, "/api/x")
	echoOf(t)(res, body)
	if hits.Load() != 1 || res.Header.Get(HeaderGateway) != "route=payments" {
		t.Fatalf("the request should go on to the upstream unmarked: %d %q", hits.Load(), res.Header.Get(HeaderGateway))
	}
}

func TestDisabledDoesNotHideLessSpecific(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	exact := synth("exact", "/api/x", "exact")
	exact.On = ptr(false)
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(exact, synth("wildcard", "/api/*", "wildcard"))))
	if _, body := get(t, gw, "/api/x"); string(body) != "wildcard" {
		t.Fatalf("the enabled wildcard should be applied: %q", body)
	}
}

func TestReenablingRestoresBehavior(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("off", "/api/*", "unavailable")
	o.Respond.Status = http.StatusServiceUnavailable
	o.Respond.Headers = map[string]config.HeaderValues{"Retry-After": {"5"}}
	on := route("payments", up.URL, "/api/*", withOverrides(o))
	g := capturing(t, recording(), on)
	res, first := g.get(t, "/api/x")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("while on, the override should answer: %d", res.StatusCode)
	}
	off := o
	off.On = ptr(false)
	g.swapRoutes(t, route("payments", up.URL, "/api/*", withOverrides(off)))
	echoOf(t)(g.get(t, "/api/x"))
	g.swapRoutes(t, on)
	res, again := g.get(t, "/api/x")
	if res.StatusCode != http.StatusServiceUnavailable || string(again) != string(first) || res.Header.Get("Retry-After") != "5" {
		t.Fatalf("switched back on, the override should answer the same way: %d %q %v", res.StatusCode, again, res.Header)
	}
	if hits.Load() != 1 {
		t.Fatalf("only the request made while the override was off should reach the upstream, %d did", hits.Load())
	}
}

// discardLog silences the test server's log, which would record the stream
// cancellation as an error.
func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

// A latency-only override also delays the gateway's error responses (502 and
// 504), at the same point between the response being ready and the write, and
// X-Gateway identifies the delay.
func TestLatencyOnlyDelaysGatewayErrors(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	timeout := config.Duration(100 * time.Millisecond)
	delayOnly := func(name string) config.Override {
		return config.Override{Name: name, Match: config.OverrideMatch{Path: "/*"}, Latency: latency(400 * time.Millisecond)}
	}
	for _, c := range []struct {
		route    config.Route
		status   int
		minDelay time.Duration
	}{
		{route("down", down.URL, "/down/*", withOverrides(delayOnly("slow"))), http.StatusBadGateway, 400 * time.Millisecond},
		{route("slow", slow.URL, "/slow/*", withOverrides(delayOnly("slow")), func(r *config.Route) { r.Timeout = &timeout }),
			http.StatusGatewayTimeout, 500 * time.Millisecond},
	} {
		t.Run(c.route.Name, func(t *testing.T) {
			g := capturing(t, recording(), c.route)
			res, _, el := timedGet(t, g.URL+"/"+c.route.Name+"/x")
			if res.StatusCode != c.status {
				t.Fatalf("want %d, got %d", c.status, res.StatusCode)
			}
			if el < c.minDelay {
				t.Fatalf("the error response should arrive after the delay, it arrived in %v", el)
			}
			want := "route=" + c.route.Name + "; override=" + c.route.Name + "/slow; intervention=delayed"
			if got := res.Header.Get(HeaderGateway); got != want {
				t.Fatalf("unexpected X-Gateway: %q", got)
			}
			e := g.only(t)
			if e.Status != c.status || e.Outcome != exchange.OutcomeGateway || !slices.Equal(e.Interventions, []string{"delayed"}) {
				t.Fatalf("the exchange should record the delayed error: %+v", e)
			}
			approx(t, "injected time", e.Timing.InjectedMs, 400, 600)
		})
	}
}

// If the client gives up during the delay of a synthesized response, nothing
// was delivered: the exchange is left without a status, with the give-up
// noted.
func TestClientCancelDuringSynthesizedDelay(t *testing.T) {
	o := synth("slow", "/api/*", "never delivered")
	o.Latency = latency(2 * time.Second)
	g := capturing(t, recording(), route("payments", "", "/api/*", withOverrides(o)))
	client := &http.Client{Timeout: 200 * time.Millisecond}
	if res, err := client.Get(g.URL + "/api/x"); err == nil {
		res.Body.Close()
		t.Fatalf("the client should give up before the response, it got %d", res.StatusCode)
	}
	var e exchange.Exchange
	deadline := time.Now().Add(5 * time.Second)
	for {
		if items := g.history(t, exchange.Filter{}); len(items) == 1 {
			e = items[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the exchange was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e.Status != 0 || !strings.HasPrefix(e.Error, "client_canceled") {
		t.Fatalf("the exchange should not record a delivered response: status %d, error %q", e.Status, e.Error)
	}
	if !slices.Equal(e.Interventions, []string{"synthesized", "delayed"}) {
		t.Fatalf("the interventions should be there: %v", e.Interventions)
	}
}

// An HTTP/1 ResponseWriter with no connection hijacking does not prevent the
// drop: the handler is aborted without a response, and the capture records
// the abort mode.
func TestDropWithoutHijackerAborts(t *testing.T) {
	o := synth("drop", "/api/*", "never sent")
	o.Drop = config.Drop{On: true}
	g := capturing(t, recording(), route("payments", "", "/api/*", withOverrides(o)))
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if p := recover(); p != http.ErrAbortHandler {
				t.Fatalf("the handler should be aborted with http.ErrAbortHandler: %v", p)
			}
		}()
		g.h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	}()
	if w.Body.Len() != 0 || w.Code != http.StatusOK && w.Code != 0 {
		t.Fatalf("nothing should have been written: %d %q", w.Code, w.Body)
	}
	e := g.only(t)
	if e.Outcome != exchange.OutcomeDropped || e.DropMode != exchange.DropAbort || e.Status != 0 {
		t.Fatalf("the exchange should record the drop by abort: %+v", e)
	}
}

// Headers declared with several values on the response, such as Set-Cookie,
// are repeated on the synthesized response.
func TestSynthesizedRepeatedHeaders(t *testing.T) {
	o := synth("cookies", "/api/*", "ok")
	o.Respond.Headers = map[string]config.HeaderValues{"Set-Cookie": {"a=1", "b=2"}}
	g := capturing(t, recording(), route("payments", "", "/api/*", withOverrides(o)))
	res, _ := g.get(t, "/api/x")
	if got := res.Header.Values("Set-Cookie"); !slices.Equal(got, []string{"a=1", "b=2"}) {
		t.Fatalf("both Set-Cookie headers should reach the client: %q", got)
	}
}
