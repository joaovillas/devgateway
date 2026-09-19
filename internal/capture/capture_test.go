package capture

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gamerjp64/devgateway/internal/exchange"
	"github.com/gamerjp64/devgateway/internal/store"
)

func newRecorder(t *testing.T) (*Recorder, *store.Memory) {
	t.Helper()
	st := store.NewMemory(100)
	r := NewRecorder(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(r.Close)
	return r, st
}

func stored(t *testing.T, r *Recorder, st store.Store) []exchange.Exchange {
	t.Helper()
	if err := r.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := st.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	return res.Items
}

var on = Options{Record: true, MaxBodyBytes: 1024}

// Requirement: Latency breakdown

func TestSynthesizedResponseHasNoUpstreamTime(t *testing.T) {
	r, st := newRecorder(t)
	w := httptest.NewRecorder()
	rec := r.Begin(w, httptest.NewRequest(http.MethodGet, "/x", nil), on)
	rec.SetRoute("payments", "")
	rec.Intervene("payments/failure", "synthesized")
	if err := rec.Delay(t.Context(), "payments/failure", 60*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	rec.Writer().WriteHeader(http.StatusServiceUnavailable)
	rec.Finish()

	e := stored(t, r, st)[0]
	if e.Timing.UpstreamMs != 0 {
		t.Fatalf("a synthesized response should not count upstream time: %+v", e.Timing)
	}
	if e.Timing.InjectedMs < 60 || e.Timing.InjectedMs > 300 {
		t.Fatalf("the measured delay should show up as injected: %+v", e.Timing)
	}
	if e.Status != 503 || e.Outcome != exchange.OutcomeSynthesized || e.Override != "payments/failure" {
		t.Fatalf("status %d, outcome %s, override %q", e.Status, e.Outcome, e.Override)
	}
	if strings.Join(e.Interventions, ",") != "synthesized,delayed" {
		t.Fatalf("interventions %v", e.Interventions)
	}
}

func TestInjectedDelayIsDiscountedFromUpstream(t *testing.T) {
	r, st := newRecorder(t)
	rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), on)
	rec.UpstreamStarted()
	time.Sleep(30 * time.Millisecond)
	rec.Delay(t.Context(), "a/b", 100*time.Millisecond)
	rec.UpstreamDone()
	rec.Finish()
	tm := stored(t, r, st)[0].Timing
	if tm.UpstreamMs < 30 || tm.UpstreamMs > 90 {
		t.Fatalf("the delay should be subtracted from the upstream time: %+v", tm)
	}
	if tm.InjectedMs < 100 {
		t.Fatalf("injected time %+v", tm)
	}
}

func TestDelayStopsWhenClientGivesUp(t *testing.T) {
	r, _ := newRecorder(t)
	rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), on)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := rec.Delay(ctx, "a/b", 5*time.Second); err == nil {
		t.Fatal("want the context's error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("the delay should end when the client gives up")
	}
}

// Requirement: Recording of the HTTP exchanges

func TestSequenceIsMonotonicEvenWithoutRecording(t *testing.T) {
	r, st := newRecorder(t)
	var wg sync.WaitGroup
	seqs := make(chan uint64, 100)
	for i := range 100 {
		wg.Go(func() {
			o := on
			o.Record = i%2 == 0
			rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), o)
			seqs <- rec.Seq()
			rec.Finish()
		})
	}
	wg.Wait()
	close(seqs)
	seen := map[uint64]bool{}
	for s := range seqs {
		if s < 1 || s > 100 || seen[s] {
			t.Fatalf("sequence number %d repeated or outside 1..100", s)
		}
		seen[s] = true
	}
	if n := len(stored(t, r, st)); n != 50 {
		t.Fatalf("only the requests with recording on should be written: %d", n)
	}
}

func TestWriterKeepsFlushAndUnwrap(t *testing.T) {
	r, _ := newRecorder(t)
	w := httptest.NewRecorder()
	rec := r.Begin(w, httptest.NewRequest(http.MethodGet, "/x", nil), on)
	cw := rec.Writer()
	io.WriteString(cw, "part")
	if err := http.NewResponseController(cw).Flush(); err != nil {
		t.Fatalf("the Flush should reach the server's writer: %v", err)
	}
	if !w.Flushed {
		t.Fatal("the server's writer did not get the Flush")
	}
	cw.(http.Flusher).Flush()
	if u, ok := cw.(interface{ Unwrap() http.ResponseWriter }); !ok || u.Unwrap() != w {
		t.Fatal("the writer should expose the original one through Unwrap")
	}
	rec.Finish()
}

func TestInformationalResponsesDoNotFixStatus(t *testing.T) {
	r, st := newRecorder(t)
	rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), on)
	rec.Writer().WriteHeader(http.StatusEarlyHints)
	rec.Writer().WriteHeader(http.StatusCreated)
	rec.Finish()
	if e := stored(t, r, st)[0]; e.Status != http.StatusCreated {
		t.Fatalf("the final status should be 201, recorded %d", e.Status)
	}
}

func TestDroppedHasNoStatus(t *testing.T) {
	r, st := newRecorder(t)
	rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), on)
	rec.Dropped("a/drop", "hijack")
	rec.Finish()
	e := stored(t, r, st)[0]
	if e.Status != 0 || e.Outcome != exchange.OutcomeDropped || e.DropMode != "hijack" || e.Override != "a/drop" {
		t.Fatalf("drop recorded as status %d, outcome %s, mode %q, override %q", e.Status, e.Outcome, e.DropMode, e.Override)
	}
}

func TestRequestBodyNotFullyReadIsMarked(t *testing.T) {
	r, st := newRecorder(t)
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(strings.Repeat("a", 100)))
	rec := r.Begin(httptest.NewRecorder(), req, on)
	buf := make([]byte, 10)
	io.ReadFull(rec.Request().Body, buf)
	rec.Finish()
	e := stored(t, r, st)[0]
	if e.Request.Size != 100 || !e.Request.Truncated || string(e.Request.Body) != "aaaaaaaaaa" {
		t.Fatalf("body read only halfway: size %d, truncated %v, body %q", e.Request.Size, e.Request.Truncated, e.Request.Body)
	}
}

func TestDrainRequestCapturesUnforwardedBody(t *testing.T) {
	r, st := newRecorder(t)
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("complete body"))
	rec := r.Begin(httptest.NewRecorder(), req, on)
	rec.DrainRequest()
	rec.Finish()
	e := stored(t, r, st)[0]
	if string(e.Request.Body) != "complete body" || e.Request.Truncated || e.Request.Size != 13 {
		t.Fatalf("body that was not forwarded: %q, truncated %v, size %d", e.Request.Body, e.Request.Truncated, e.Request.Size)
	}
}

func TestZeroLimitCapturesNoBody(t *testing.T) {
	r, st := newRecorder(t)
	rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), Options{Record: true})
	io.WriteString(rec.Writer(), "abc")
	rec.Finish()
	e := stored(t, r, st)[0]
	if len(e.Response.Body) != 0 || !e.Response.Truncated || e.Response.Size != 3 {
		t.Fatalf("zero limit: body %q, truncated %v, size %d", e.Response.Body, e.Response.Truncated, e.Response.Size)
	}
}

func TestCloseWritesPendingExchanges(t *testing.T) {
	st := store.NewMemory(100)
	r := NewRecorder(st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for range 10 {
		rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), on)
		rec.Finish()
	}
	r.Close()
	res, _ := st.List(t.Context(), exchange.Filter{}, store.Page{})
	if len(res.Items) != 10 {
		t.Fatalf("shutdown should write the pending queue: %d out of 10", len(res.Items))
	}
	r.Close() // idempotent
	if err := r.Sync(t.Context()); err != nil {
		t.Fatalf("Sync after Close should not fail: %v", err)
	}
}

// Broker

func TestBrokerDeliversInOrder(t *testing.T) {
	b := NewBroker()
	s := b.Subscribe(10)
	defer s.Close()
	for i := range 3 {
		b.Publish(exchange.Exchange{Seq: uint64(i)})
	}
	for i := range 3 {
		if e := <-s.C; e.Seq != uint64(i) {
			t.Fatalf("want exchange %d, got %d", i, e.Seq)
		}
	}
}

func TestBrokerNeverBlocksOnSlowSubscriber(t *testing.T) {
	b := NewBroker()
	slow := b.Subscribe(2)
	fast := b.Subscribe(100)
	defer slow.Close()
	defer fast.Close()
	done := make(chan struct{})
	go func() {
		for i := range 50 {
			b.Publish(exchange.Exchange{Seq: uint64(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing waited on a slow subscriber")
	}
	if slow.Dropped() != 48 || len(fast.C) != 50 {
		t.Fatalf("the slow one should lose 48 and the fast one should get 50: dropped %d, received %d", slow.Dropped(), len(fast.C))
	}
	if slow.TakeDropped() != 48 || slow.Dropped() != 0 {
		t.Fatal("TakeDropped should return the count and zero it")
	}
}

func TestBrokerCloseStopsDelivery(t *testing.T) {
	b := NewBroker()
	s := b.Subscribe(1)
	s.Close()
	s.Close()
	b.Publish(exchange.Exchange{})
	if _, ok := <-s.C; ok {
		t.Fatal("a closed subscription should not get exchanges")
	}
	if b.Subscribers() != 0 {
		t.Fatalf("open subscriptions: %d", b.Subscribers())
	}
}

func TestRecorderPublishesOnlyRecorded(t *testing.T) {
	st := store.NewMemory(100)
	b := NewBroker()
	r := NewRecorder(st, b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(r.Close)
	s := b.Subscribe(10)
	defer s.Close()
	for _, o := range []Options{on, {Record: false}} {
		rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), o)
		rec.Finish()
	}
	r.Sync(t.Context())
	if len(s.C) != 1 {
		t.Fatalf("only the recorded exchange should be published: %d", len(s.C))
	}
}

// syncBuffer is a log destination that is safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
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

// An exchange finished after Close has no one left to write it: it is dropped
// with a warning in the log, instead of vanishing into the queue without a
// trace.
func TestExchangeFinishedAfterCloseIsLogged(t *testing.T) {
	st := store.NewMemory(100)
	var logs syncBuffer
	r := NewRecorder(st, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/late", nil), on)
	r.Close()
	rec.Finish()
	r.Sync(t.Context())
	if res, _ := st.List(t.Context(), exchange.Filter{}, store.Page{}); len(res.Items) != 0 {
		t.Fatalf("no exchange should be written after shutdown: %d", len(res.Items))
	}
	if l := logs.String(); !strings.Contains(l, "history recording has already been shut down") || !strings.Contains(l, "/late") {
		t.Fatalf("the dropped exchange should go to the log: %q", l)
	}
}

// Wait waits for the open records, so that shutdown writes the exchanges that
// were still in flight (such as those of hijacked connections).
func TestWaitForOpenRecords(t *testing.T) {
	r, st := newRecorder(t)
	rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/open", nil), on)
	if r.Open() != 1 {
		t.Fatalf("want one open record, there are %d", r.Open())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := r.Wait(ctx); err == nil {
		t.Fatal("with an open record, Wait should wait until the deadline")
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		rec.Finish()
	}()
	if err := r.Wait(t.Context()); err != nil {
		t.Fatalf("Wait should end when the record closes: %v", err)
	}
	r.Close()
	if res, _ := st.List(t.Context(), exchange.Filter{}, store.Page{}); len(res.Items) != 1 || res.Items[0].Path != "/open" {
		t.Fatalf("the exchange that was waited on should have been written: %v", res.Items)
	}
	// A record with writing turned off also counts until it closes.
	off := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil), Options{})
	off.Finish()
	off.Finish()
	if r.Open() != 0 {
		t.Fatalf("closing twice should not count down twice: %d", r.Open())
	}
}

// On a protocol upgrade the ReverseProxy does not call WriteHeader: the
// response noted by Upgrade holds, unless the writer has written another one.
func TestUpgradeResponseIsRecorded(t *testing.T) {
	r, st := newRecorder(t)
	rec := r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ws", nil), on)
	h := http.Header{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}}
	rec.Upgrade(http.StatusSwitchingProtocols, h)
	h.Set("Upgrade", "changed")
	rec.Finish()
	e := stored(t, r, st)[0]
	if e.Status != http.StatusSwitchingProtocols || e.Response.Headers.Get("Upgrade") != "websocket" {
		t.Fatalf("upgrade recorded with status %d and headers %v", e.Status, e.Response.Headers)
	}

	rec = r.Begin(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ws2", nil), on)
	rec.Upgrade(http.StatusSwitchingProtocols, h)
	rec.Writer().WriteHeader(http.StatusBadGateway)
	rec.Finish()
	if e := stored(t, r, st)[0]; e.Status != http.StatusBadGateway {
		t.Fatalf("the response written after the upgrade should win: %d", e.Status)
	}
}

// A body with no Content-Length larger than the limit, in a response from the
// gateway itself, is read to the end so that the real size shows up.
func TestDrainRequestCountsRealSizeOfChunkedBody(t *testing.T) {
	r, st := newRecorder(t)
	body := strings.Repeat("x", 5000)
	req := httptest.NewRequest(http.MethodPost, "/x", io.NopCloser(strings.NewReader(body)))
	req.ContentLength = -1
	o := on
	o.MaxBodyBytes = 10
	rec := r.Begin(httptest.NewRecorder(), req, o)
	rec.DrainRequest()
	rec.Finish()
	e := stored(t, r, st)[0]
	if e.Request.Size != int64(len(body)) || !e.Request.Truncated || string(e.Request.Body) != body[:10] {
		t.Fatalf("size %d (want %d), truncated %v, body %q", e.Request.Size, len(body), e.Request.Truncated, e.Request.Body)
	}
}
