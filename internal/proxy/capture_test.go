package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joaovillas/devgateway/internal/capture"
	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
)

// capGW is a test gateway with access to the history it writes into.
type capGW struct {
	*httptest.Server
	h    *Handler
	rec  *capture.Recorder
	hist store.Store
}

func capturing(t *testing.T, s config.Settings, routes ...config.Route) *capGW {
	t.Helper()
	return capturingInto(t, store.NewMemory(1000), s, routes...)
}

func capturingInto(t *testing.T, hist store.Store, s config.Settings, routes ...config.Route) *capGW {
	t.Helper()
	rec := capture.NewRecorder(hist, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(rec.Close)
	h := NewHandler(liveWith(t, s, routes...), rec)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &capGW{Server: srv, h: h, rec: rec, hist: hist}
}

// history returns the recorded exchanges, newest to oldest, after waiting for
// the ones already answered to be written.
func (g *capGW) history(t *testing.T, f exchange.Filter) []exchange.Exchange {
	t.Helper()
	if err := g.rec.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	var all []exchange.Exchange
	p := store.Page{Limit: store.MaxLimit}
	for {
		res, err := g.hist.List(t.Context(), f, p)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, res.Items...)
		if res.Next == "" {
			return all
		}
		p.Cursor = res.Next
	}
}

// only returns the single recorded exchange, in full.
func (g *capGW) only(t *testing.T) exchange.Exchange {
	t.Helper()
	items := g.history(t, exchange.Filter{})
	if len(items) != 1 {
		t.Fatalf("want one exchange in the history, got %d", len(items))
	}
	e, err := g.hist.Get(t.Context(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (g *capGW) get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, g.URL+path, nil)
	return do(t, req)
}

func approx(t *testing.T, name string, got, min, max float64) {
	t.Helper()
	if got < min || got > max {
		t.Errorf("%s = %.1fms, want between %.0fms and %.0fms", name, got, min, max)
	}
}

// Requirement: Recording of HTTP exchanges

func TestExchangeRecordedCompletely(t *testing.T) {
	up := echoUpstream(t, "payments")
	g := capturing(t, recording(), route("payments", up.URL, "/api/*"))

	req, _ := http.NewRequest(http.MethodPost, g.URL+"/api/charge?amount=100&retry=1", strings.NewReader(`{"card":"4242"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "abc-123")
	res, body := do(t, req)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}

	e := g.only(t)
	if len(e.ID) != 26 || e.Seq == 0 || e.Start.IsZero() {
		t.Errorf("incomplete identity: id %q, seq %d, start %v", e.ID, e.Seq, e.Start)
	}
	if e.Route != "payments" || e.Upstream != up.URL {
		t.Errorf("route %q and upstream %q, want payments and %s", e.Route, e.Upstream, up.URL)
	}
	if e.Method != http.MethodPost || e.Path != "/api/charge" || e.Query != "amount=100&retry=1" {
		t.Errorf("request recorded as %s %s ? %s", e.Method, e.Path, e.Query)
	}
	if e.Host == "" || e.ClientAddr == "" {
		t.Errorf("host %q and client address %q should be there", e.Host, e.ClientAddr)
	}
	if e.Status != http.StatusOK || e.Outcome != exchange.OutcomeUpstream || e.Intervened() {
		t.Errorf("status %d, outcome %s, interventions %v", e.Status, e.Outcome, e.Interventions)
	}
	if got := e.Request.Headers.Get("X-Request-Id"); got != "abc-123" {
		t.Errorf("request header recorded as %q", got)
	}
	if string(e.Request.Body) != `{"card":"4242"}` || e.Request.Size != 15 || e.Request.Truncated {
		t.Errorf("request body %q, size %d, truncated %v", e.Request.Body, e.Request.Size, e.Request.Truncated)
	}
	if e.Response.Headers.Get("Content-Type") != "application/json" || e.Response.Headers.Get(HeaderGateway) != "route=payments" {
		t.Errorf("response headers recorded: %v", e.Response.Headers)
	}
	if !bytes.Equal(e.Response.Body, body) || e.Response.Size != int64(len(body)) || e.Response.Truncated {
		t.Errorf("the recorded response body differs from the one delivered to the client (%d of %d bytes)", len(e.Response.Body), len(body))
	}
	tm := e.Timing
	if tm.TotalMs < 0 || tm.InjectedMs != 0 || tm.UpstreamMs+tm.GatewayMs < tm.TotalMs-0.001 || tm.UpstreamMs+tm.GatewayMs > tm.TotalMs+0.001 {
		t.Errorf("inconsistent broken-down timings: %+v", tm)
	}
}

func TestResponseBodyAboveLimitIsTruncated(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789"), 100)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(big) }))
	t.Cleanup(up.Close)
	s := recording()
	s.CaptureMaxBodyBytes = 16
	g := capturing(t, s, route("a", up.URL, "/*"))

	_, body := g.get(t, "/x")
	if !bytes.Equal(body, big) {
		t.Fatalf("the client should receive the whole body, it got %d bytes", len(body))
	}
	e := g.only(t)
	if !e.Response.Truncated || e.Response.Size != int64(len(big)) || !bytes.Equal(e.Response.Body, big[:16]) {
		t.Fatalf("want a body truncated at 16 bytes with real size %d: truncated %v, size %d, body %q",
			len(big), e.Response.Truncated, e.Response.Size, e.Response.Body)
	}
}

func TestRequestBodyAboveLimitIsForwardedWhole(t *testing.T) {
	up := echoUpstream(t, "a")
	s := recording()
	s.CaptureMaxBodyBytes = 8
	g := capturing(t, s, route("a", up.URL, "/*"))

	payload := bytes.Repeat([]byte("abcdefghij"), 500)
	req, _ := http.NewRequest(http.MethodPut, g.URL+"/upload", bytes.NewReader(payload))
	if e := echoOf(t)(do(t, req)); !bytes.Equal(e.Body, payload) {
		t.Fatalf("the upstream should receive the whole body, it got %d of %d bytes", len(e.Body), len(payload))
	}
	e := g.only(t)
	if !e.Request.Truncated || e.Request.Size != int64(len(payload)) || string(e.Request.Body) != "abcdefgh" {
		t.Fatalf("request body: truncated %v, size %d, body %q", e.Request.Truncated, e.Request.Size, e.Request.Body)
	}
}

func TestChunkedRequestBodyCaptured(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	pr, pw := io.Pipe()
	go func() {
		for i := range 3 {
			fmt.Fprintf(pw, "part %d;", i)
		}
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, g.URL+"/stream", pr)
	if e := echoOf(t)(do(t, req)); string(e.Body) != "part 0;part 1;part 2;" {
		t.Fatalf("a body with no declared length was changed: %q", e.Body)
	}
	if e := g.only(t); string(e.Request.Body) != "part 0;part 1;part 2;" || e.Request.Size != 21 || e.Request.Truncated {
		t.Fatalf("a body with no declared length was recorded as %q (%d bytes, truncated %v)", e.Request.Body, e.Request.Size, e.Request.Truncated)
	}
}

func TestNoRouteExchangeIsRecorded(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/api/*"))
	req, _ := http.NewRequest(http.MethodPost, g.URL+"/other?x=1", strings.NewReader("body"))
	if res, _ := do(t, req); res.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", res.StatusCode)
	}
	e := g.only(t)
	if e.Route != "" || e.Upstream != "" {
		t.Errorf("an exchange without a route was recorded with route %q and upstream %q", e.Route, e.Upstream)
	}
	if e.Status != http.StatusNotFound || e.Outcome != exchange.OutcomeGateway || !strings.Contains(e.Error, "no_route") {
		t.Errorf("status %d, outcome %s, error %q", e.Status, e.Outcome, e.Error)
	}
	if e.Path != "/other" || e.Query != "x=1" || string(e.Request.Body) != "body" {
		t.Errorf("request recorded as %s ? %s with body %q", e.Path, e.Query, e.Request.Body)
	}
	if !strings.Contains(string(e.Response.Body), "no_route") {
		t.Errorf("the diagnostic body should be part of the exchange: %q", e.Response.Body)
	}
	if e.Timing.UpstreamMs != 0 || e.Timing.InjectedMs != 0 {
		t.Errorf("with no upstream, the upstream and injected timings should be zero: %+v", e.Timing)
	}
}

func TestGatewayErrorsRecordedAsGateway(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	timeout := config.Duration(100 * time.Millisecond)
	dead := closedAddr(t)
	g := capturing(t, recording(),
		route("empty", "", "/empty/*"),
		route("dead", dead, "/dead/*"),
		route("slow", slow.URL, "/slow/*", func(r *config.Route) { r.Timeout = &timeout }),
	)
	for _, c := range []struct {
		path, route, upstream, code string
		status                      int
	}{
		{"/empty/x", "empty", "", "no_upstream", http.StatusNotImplemented},
		{"/dead/x", "dead", dead, "upstream_unavailable", http.StatusBadGateway},
		{"/slow/x", "slow", slow.URL, "upstream_timeout", http.StatusGatewayTimeout},
	} {
		if res, _ := g.get(t, c.path); res.StatusCode != c.status {
			t.Fatalf("%s: want %d, got %d", c.path, c.status, res.StatusCode)
		}
		items := g.history(t, exchange.Filter{Route: c.route})
		if len(items) != 1 {
			t.Fatalf("%s: want one exchange for route %s, got %d", c.path, c.route, len(items))
		}
		e := items[0]
		if e.Status != c.status || e.Outcome != exchange.OutcomeGateway || !strings.Contains(e.Error, c.code) {
			t.Errorf("%s: status %d, outcome %s, error %q", c.path, e.Status, e.Outcome, e.Error)
		}
		if e.Upstream != c.upstream || e.Intervened() {
			t.Errorf("%s: upstream %q, interventions %v", c.path, e.Upstream, e.Interventions)
		}
	}
	// The time spent waiting on the slow upstream counts as upstream time.
	e := g.history(t, exchange.Filter{Route: "slow"})[0]
	approx(t, "upstream time of the 504", e.Timing.UpstreamMs, 90, 2000)
}

// Requirement: Telling an upstream response apart from a gateway intervention

func TestUpstreamErrorIsNotIntervention(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "failed", http.StatusInternalServerError)
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	g.get(t, "/x")
	e := g.only(t)
	if e.Status != 500 || e.Outcome != exchange.OutcomeUpstream || e.Intervened() || e.Override != "" || e.Error != "" {
		t.Fatalf("a 500 from the upstream was recorded as status %d, outcome %s, interventions %v, override %q, error %q",
			e.Status, e.Outcome, e.Interventions, e.Override, e.Error)
	}
}

func TestStreamingStaysIncrementalWhileCaptured(t *testing.T) {
	next := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-next:
			case <-time.After(5 * time.Second):
				return
			}
		}
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("sse", up.URL, "/*"))
	res, err := http.Get(g.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(res.Body)
	for i := range 3 {
		line, err := rd.ReadString('\n')
		if err != nil || line != fmt.Sprintf("data: %d\n", i) {
			t.Fatalf("event %d did not arrive incrementally: %q %v", i, line, err)
		}
		rd.ReadString('\n')
		next <- struct{}{}
	}
	io.ReadAll(rd)
	res.Body.Close()
	e := g.only(t)
	if string(e.Response.Body) != "data: 0\n\ndata: 1\n\ndata: 2\n\n" {
		t.Fatalf("the captured stream differs from the one delivered: %q", e.Response.Body)
	}
}

// Requirement: Latency breakdown

func TestInjectedTimeSeparatedFromUpstream(t *testing.T) {
	// The values of the scenario: a 2s delay and an upstream that answers in
	// 150ms. The delay goes in through the same injection point the
	// overrides will use (step 8), triggered here by a test hook; 6.7
	// repeats the scenario with a real override.
	const upstreamDelay, injected = 150 * time.Millisecond, 2 * time.Second
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(upstreamDelay)
		io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("payments", up.URL, "/*"))
	g.h.delayFor = func(*http.Request) (string, time.Duration) { return "payments/slow", injected }

	start := time.Now()
	if _, body := g.get(t, "/x"); string(body) != "ok" {
		t.Fatalf("body %q", body)
	}
	if el := time.Since(start); el < upstreamDelay+injected {
		t.Fatalf("the delay should add to the upstream time; the request took %v", el)
	}
	e := g.only(t)
	tm := e.Timing
	approx(t, "injected time", tm.InjectedMs, 2000, 2200)
	approx(t, "upstream time", tm.UpstreamMs, 150, 300)
	if tm.GatewayMs < 0 {
		t.Errorf("negative overhead: %+v", tm)
	}
	if sum := tm.UpstreamMs + tm.InjectedMs + tm.GatewayMs; sum < tm.TotalMs-0.001 || sum > tm.TotalMs+0.001 {
		t.Errorf("the total should be the sum of upstream, injected and overhead: %+v", tm)
	}
	if e.Override != "payments/slow" || !e.Intervened() || e.Outcome != exchange.OutcomeUpstream {
		t.Errorf("the delay should show up as an override intervention without changing the outcome: %q %v %s",
			e.Override, e.Interventions, e.Outcome)
	}
}

func TestNoOverrideMeansZeroInjected(t *testing.T) {
	// The upstream takes long enough to be measured even with a coarse clock.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	g.get(t, "/x")
	e := g.only(t)
	if e.Timing.InjectedMs != 0 || e.Intervened() {
		t.Fatalf("with no override the injected time should be zero: %+v %v", e.Timing, e.Interventions)
	}
	if e.Timing.UpstreamMs <= 0 {
		t.Fatalf("a forwarded exchange should have upstream time: %+v", e.Timing)
	}
}

func TestResponseWithoutUpstreamHasZeroUpstreamTime(t *testing.T) {
	g := capturing(t, recording(), route("empty", "", "/*"))
	g.get(t, "/x")
	if e := g.only(t); e.Timing.UpstreamMs != 0 {
		t.Fatalf("with no contact with the upstream the upstream time should be zero: %+v", e.Timing)
	}
}

// Requirement: Configurable history exposure

func TestRecordingDisabledStillForwards(t *testing.T) {
	up := echoUpstream(t, "a")
	s := recording()
	s.HistoryRecord = false
	g := capturing(t, s, route("a", up.URL, "/api/*"))
	payload := []byte("body that has to get through")
	for range 5 {
		req, _ := http.NewRequest(http.MethodPost, g.URL+"/api/x", bytes.NewReader(payload))
		if e := echoOf(t)(do(t, req)); !bytes.Equal(e.Body, payload) {
			t.Fatalf("with recording off, forwarding should work exactly the same: %q", e.Body)
		}
	}
	if res, _ := g.get(t, "/outside"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", res.StatusCode)
	}
	if items := g.history(t, exchange.Filter{}); len(items) != 0 {
		t.Fatalf("with recording off no exchange should be recorded, got %d", len(items))
	}
}

func TestClearOnDemand(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	for range 3 {
		g.get(t, "/before")
	}
	if err := g.rec.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	if items := g.history(t, exchange.Filter{}); len(items) != 0 {
		t.Fatalf("after clearing, the history should be empty, got %d", len(items))
	}
	g.get(t, "/after")
	if e := g.only(t); e.Path != "/after" {
		t.Fatalf("the next exchange should be recorded again, recorded %s", e.Path)
	}
}

// failingStore refuses every write; blockingStore hangs on it.
type failingStore struct{ store.Store }

func (failingStore) Record(context.Context, *exchange.Exchange) error {
	return errors.New("disk full")
}

type blockingStore struct {
	store.Store
	release chan struct{}
}

func (b blockingStore) Record(ctx context.Context, e *exchange.Exchange) error {
	<-b.release
	return b.Store.Record(ctx, e)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRecordFailureOnlyLogs(t *testing.T) {
	up := echoUpstream(t, "a")
	var logs syncBuffer
	rec := capture.NewRecorder(failingStore{store.NewMemory(10)}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(rec.Close)
	srv := httptest.NewServer(NewHandler(liveOf(t, route("a", up.URL, "/*")), rec))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
	if e := echoOf(t)(do(t, req)); e.Upstream != "a" {
		t.Fatalf("a write failure should not affect forwarding")
	}
	rec.Sync(t.Context())
	if !strings.Contains(logs.String(), "disk full") {
		t.Fatalf("the write failure should reach the log: %q", logs.String())
	}
}

func TestRecordingNeverBlocksForwarding(t *testing.T) {
	up := echoUpstream(t, "a")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	hist := store.NewMemory(2 * capture.QueueSize)
	var logs syncBuffer
	rec := capture.NewRecorder(blockingStore{hist, release}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(rec.Close)
	t.Cleanup(unblock)
	srv := httptest.NewServer(NewHandler(liveOf(t, route("a", up.URL, "/*")), rec))
	t.Cleanup(srv.Close)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// More requests than the queue holds: the excess ones are dropped.
		for range capture.QueueSize + 50 {
			res, err := http.Get(srv.URL + "/x")
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("forwarding ended up waiting on the history")
	}
	// With the write blocked, QueueSize exchanges fit in the queue, plus the
	// one the writer goroutine is holding; the rest are dropped with a
	// warning.
	if !strings.Contains(logs.String(), "the history write queue is full") {
		t.Fatalf("the drop caused by a full queue should reach the log")
	}
	unblock()
	if err := rec.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := hist.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	n := len(res.Items)
	for res.Next != "" {
		if res, err = hist.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit, Cursor: res.Next}); err != nil {
			t.Fatal(err)
		}
		n += len(res.Items)
	}
	if n == 0 || n > capture.QueueSize+1 {
		t.Fatalf("at most %d exchanges should have been written, %d were", capture.QueueSize+1, n)
	}
}

func TestRecordsIntoCurrentBackend(t *testing.T) {
	up := echoUpstream(t, "a")
	first := store.NewMemory(10)
	hist := store.NewSwitchable("memory", first)
	g := capturingInto(t, hist, recording(), route("a", up.URL, "/*"))
	g.get(t, "/before")
	g.rec.Sync(t.Context())

	second := store.NewMemory(10)
	if err := hist.Swap(t.Context(), "memory", second); err != nil {
		t.Fatal(err)
	}
	g.get(t, "/after")
	g.rec.Sync(t.Context())
	res, _ := second.List(t.Context(), exchange.Filter{}, store.Page{})
	if len(res.Items) != 1 || res.Items[0].Path != "/after" {
		t.Fatalf("the exchange after the backend swap should go into the new one: %v", res.Items)
	}
}

func TestRecordedExchangesArePublished(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	sub := g.rec.Broker().Subscribe(4)
	defer sub.Close()
	g.get(t, "/x")
	select {
	case e := <-sub.C:
		if e.Path != "/x" || e.Route != "a" {
			t.Fatalf("the published exchange differs from the recorded one: %s %s", e.Route, e.Path)
		}
		if stored := g.only(t); stored.ID != e.ID {
			t.Fatalf("published %s, recorded %s", e.ID, stored.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the recorded exchange was not published")
	}
}

func TestConcurrentExchangesHaveDistinctIdentity(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			res, err := http.Get(fmt.Sprintf("%s/r/%d", g.URL, i))
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
	ids, seqs := map[string]bool{}, map[uint64]bool{}
	for _, e := range items {
		ids[e.ID], seqs[e.Seq] = true, true
	}
	if len(ids) != n || len(seqs) != n {
		t.Fatalf("repeated ids or sequences: %d distinct ids and %d distinct sequences", len(ids), len(seqs))
	}
}

// upgradeUpstream accepts a protocol upgrade and echoes back the lines it
// receives over the swapped connection.
func upgradeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "upgrade expected", http.StatusBadRequest)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\nX-Echo: yes\r\n\r\n")
		brw.Flush()
		for {
			line, err := brw.ReadString('\n')
			if err != nil {
				return
			}
			brw.WriteString("echo: " + line)
			brw.Flush()
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// A protocol upgrade is recorded with the 101 and the upstream response's
// headers, even though the ReverseProxy writes that response straight to the
// hijacked connection.
func TestUpgradeExchangeRecordedWith101(t *testing.T) {
	up := upgradeUpstream(t)
	g := capturing(t, recording(), route("ws", up.URL, "/*"))

	conn, err := net.Dial("tcp", g.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /channel HTTP/1.1\r\nHost: gw.local\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("the client should receive 101, it got %d", res.StatusCode)
	}
	io.WriteString(conn, "hello\n")
	if line, err := br.ReadString('\n'); err != nil || line != "echo: hello\n" {
		t.Fatalf("the swapped connection should echo: %q %v", line, err)
	}
	conn.Close()

	// The record closes when the tunnel ends, after the connection is over.
	if err := g.rec.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := g.only(t)
	if e.Status != http.StatusSwitchingProtocols || e.Outcome != exchange.OutcomeUpstream {
		t.Fatalf("upgrade recorded with status %d and outcome %s", e.Status, e.Outcome)
	}
	h := e.Response.Headers
	if h.Get("Upgrade") != "echo" || h.Get("X-Echo") != "yes" || h.Get(HeaderGateway) != "route=ws" {
		t.Fatalf("headers of the upgrade response recorded: %v", h)
	}
}

// On a response produced by the gateway itself, a body with no Content-Length
// that is larger than the capture limit keeps its real size.
func TestNoRouteChunkedBodyKeepsRealSize(t *testing.T) {
	s := recording()
	s.CaptureMaxBodyBytes = 8
	g := capturing(t, s, route("a", "http://127.0.0.1:1", "/api/*"))
	payload := bytes.Repeat([]byte("abcdefghij"), 300)
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < len(payload); i += 100 {
			pw.Write(payload[i : i+100])
		}
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, g.URL+"/outside", pr)
	if res, _ := do(t, req); res.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", res.StatusCode)
	}
	e := g.only(t)
	if e.Request.Size != int64(len(payload)) || !e.Request.Truncated || string(e.Request.Body) != "abcdefgh" {
		t.Fatalf("request body: size %d (want %d), truncated %v, body %q",
			e.Request.Size, len(payload), e.Request.Truncated, e.Request.Body)
	}
}
