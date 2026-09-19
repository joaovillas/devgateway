package proxy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joaovillas/devgateway/internal/capture"
	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
)

// The query, read and navigation scenarios run over real exchanges, captured
// by the proxy, not over exchanges assembled by hand.

// statusUpstream answers with the status asked for in ?status=, 200 by
// default.
func statusUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusOK
		if v := r.URL.Query().Get("status"); v != "" {
			code, _ = strconv.Atoi(v)
		}
		w.WriteHeader(code)
		io.WriteString(w, "response from "+r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

// historyGateway sets up two routes, payments and orders, and uses a test
// override to delay the requests whose path contains "/slow".
func historyGateway(t *testing.T) *capGW {
	t.Helper()
	up := statusUpstream(t)
	g := capturing(t, recording(),
		route("payments", up.URL, "/api/payments/*"),
		route("orders", up.URL, "/api/orders/*"),
	)
	g.h.delayFor = func(r *http.Request) (string, time.Duration) {
		if strings.Contains(r.URL.Path, "/slow") {
			return "payments/slow", time.Millisecond
		}
		return "", 0
	}
	return g
}

// Requirement: History querying and filtering

func TestHistoryFilterByRoute(t *testing.T) {
	g := historyGateway(t)
	for i := range 3 {
		g.get(t, fmt.Sprintf("/api/payments/%d", i))
		g.get(t, fmt.Sprintf("/api/orders/%d", i))
	}
	items := g.history(t, exchange.Filter{Route: "payments"})
	if len(items) != 3 {
		t.Fatalf("want 3 payments exchanges, got %d", len(items))
	}
	for _, e := range items {
		if e.Route != "payments" {
			t.Fatalf("exchange from route %s showed up in the payments filter", e.Route)
		}
	}
}

func TestHistoryFilterByStatusRange(t *testing.T) {
	g := historyGateway(t)
	for _, code := range []int{200, 404, 500, 503, 599, 201} {
		g.get(t, fmt.Sprintf("/api/orders/x?status=%d", code))
	}
	g.get(t, "/no-route") // a 404 from the gateway itself
	items := g.history(t, exchange.Filter{StatusMin: 500, StatusMax: 599})
	var got []int
	for _, e := range items {
		got = append(got, e.Status)
	}
	if fmt.Sprint(got) != "[599 503 500]" {
		t.Fatalf("want the 5xx statuses from newest to oldest, got %v", got)
	}
}

// synthesize records in the same history an exchange answered by an
// override, going through the capture path. Synthesis itself belongs to the
// overrides phase (6.4); 6.10 repeats this filter with a real synthesis.
func (g *capGW) synthesize(t *testing.T, path, override string, status int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := g.rec.Begin(httptest.NewRecorder(), req, capture.Options{Record: true, MaxBodyBytes: 1024})
	rec.SetRoute("payments", "")
	rec.Intervene(override, "synthesized")
	rec.Writer().WriteHeader(status)
	rec.Finish()
}

func TestHistoryFilterByIntervention(t *testing.T) {
	g := historyGateway(t)
	g.get(t, "/api/payments/normal")
	g.get(t, "/api/payments/slow")
	g.synthesize(t, "/api/payments/synthesized", "payments/flaky", http.StatusServiceUnavailable)
	g.get(t, "/api/orders/normal?status=500")
	yes, no := true, false
	with := g.history(t, exchange.Filter{Intervened: &yes})
	if len(with) != 2 {
		t.Fatalf("the intervention filter should return the synthesized and the delayed exchange: %v", with)
	}
	synth, delayed := with[0], with[1]
	if synth.Path != "/api/payments/synthesized" || synth.Outcome != exchange.OutcomeSynthesized || synth.Override != "payments/flaky" {
		t.Fatalf("the synthesized exchange should show up in the filter: %+v", synth)
	}
	if delayed.Path != "/api/payments/slow" || delayed.Override != "payments/slow" {
		t.Fatalf("the delayed exchange should show up in the filter: %+v", delayed)
	}
	if without := g.history(t, exchange.Filter{Intervened: &no}); len(without) != 2 {
		t.Fatalf("want 2 exchanges without intervention, got %d", len(without))
	}
}

// The history order is arrival order: a slow exchange that arrived earlier
// and finished later does not jump ahead of the ones that arrived after it.
func TestHistoryOrderFollowsArrivalNotCompletion(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			close(arrived)
			<-release
		}
		io.WriteString(w, r.URL.Path)
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("a", up.URL, "/*"))

	slow := make(chan struct{})
	go func() {
		defer close(slow)
		res, err := http.Get(g.URL + "/slow")
		if err != nil {
			t.Error(err)
			return
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}()
	<-arrived
	g.get(t, "/fast")
	g.rec.Sync(t.Context())
	close(release)
	<-slow

	items := g.history(t, exchange.Filter{})
	if len(items) != 2 || items[0].Path != "/fast" || items[1].Path != "/slow" {
		var got []string
		for _, e := range items {
			got = append(got, e.Path)
		}
		t.Fatalf("newest to oldest by arrival, want [/fast /slow], got %v", got)
	}
	if items[0].Start.Before(items[1].Start) || items[1].Seq > items[0].Seq {
		t.Fatalf("the slow one arrived first: start %v and %v, sequence %d and %d", items[1].Start, items[0].Start, items[1].Seq, items[0].Seq)
	}
	next, err := g.hist.Neighbor(t.Context(), items[1].ID, store.Newer, exchange.Filter{})
	if err != nil || next.Path != "/fast" {
		t.Fatalf("the one after the slow should be the fast one: %s %v", next.Path, err)
	}
}

func TestHistoryCombinedFilters(t *testing.T) {
	g := historyGateway(t)
	g.get(t, "/api/payments/a?status=500")
	g.get(t, "/api/payments/b?status=200")
	g.get(t, "/api/orders/c?status=500")
	items := g.history(t, exchange.Filter{Route: "payments", StatusMin: 500, StatusMax: 599, Method: "get"})
	if len(items) != 1 || items[0].Path != "/api/payments/a" {
		t.Fatalf("combined filters should apply together: %v", items)
	}
}

func TestHistoryOrderAndPagination(t *testing.T) {
	g := historyGateway(t)
	for i := range 150 {
		g.get(t, fmt.Sprintf("/api/orders/%d", i))
	}
	g.rec.Sync(t.Context())
	page, err := g.hist.List(t.Context(), exchange.Filter{}, store.Page{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 50 || page.Next == "" {
		t.Fatalf("want a page of 50 with a continuation, got %d (next %q)", len(page.Items), page.Next)
	}
	for i, e := range page.Items {
		if want := fmt.Sprintf("/api/orders/%d", 149-i); e.Path != want {
			t.Fatalf("item %d = %s, want %s (newest to oldest)", i, e.Path, want)
		}
	}
}

// Requirement: Individual reads and cursor navigation

func TestHistoryGetByID(t *testing.T) {
	g := historyGateway(t)
	g.get(t, "/api/payments/slow")
	items := g.history(t, exchange.Filter{})
	e, err := g.hist.Get(t.Context(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != items[0].ID || e.Method != http.MethodGet || e.Status != 200 {
		t.Fatalf("the exchange came back incomplete: %+v", e)
	}
	if string(e.Response.Body) != "response from /api/payments/slow" || e.Response.Headers.Get(HeaderGateway) == "" {
		t.Fatalf("the response should come back complete: %q %v", e.Response.Body, e.Response.Headers)
	}
	if tm := e.Timing; tm.InjectedMs <= 0 || tm.UpstreamMs+tm.InjectedMs+tm.GatewayMs < tm.TotalMs-0.001 {
		t.Fatalf("the timings should come back broken down and filled in: %+v", e.Timing)
	}
}

func TestHistoryGetMissingID(t *testing.T) {
	g := historyGateway(t)
	g.get(t, "/api/payments/x")
	g.rec.Sync(t.Context())
	if _, err := g.hist.Get(t.Context(), exchange.NewID(time.Now())); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestHistoryNavigateItemByItem(t *testing.T) {
	g := historyGateway(t)
	for i := range 3 {
		g.get(t, fmt.Sprintf("/api/orders/%d", i))
	}
	items := g.history(t, exchange.Filter{}) // 2, 1, 0
	next, err := g.hist.Neighbor(t.Context(), items[2].ID, store.Newer, exchange.Filter{})
	if err != nil || next.ID != items[1].ID {
		t.Fatalf("the one after /0 should be /1: %s %v", next.Path, err)
	}
	if _, err := g.hist.Neighbor(t.Context(), items[0].ID, store.Newer, exchange.Filter{}); !errors.Is(err, store.ErrNoMore) {
		t.Fatalf("there is nothing past the newest one: %v", err)
	}
	prev, err := g.hist.Neighbor(t.Context(), items[1].ID, store.Older, exchange.Filter{})
	if err != nil || prev.ID != items[2].ID {
		t.Fatalf("the one before /1 should be /0: %s %v", prev.Path, err)
	}
}

func TestHistoryNavigationRespectsFilter(t *testing.T) {
	g := historyGateway(t)
	for _, code := range []int{500, 200, 201, 503, 200} {
		g.get(t, fmt.Sprintf("/api/orders/%d?status=%d", code, code))
	}
	items := g.history(t, exchange.Filter{}) // 200, 503, 201, 200, 500
	f := exchange.Filter{StatusMin: 500, StatusMax: 599}
	first := items[4]
	next, err := g.hist.Neighbor(t.Context(), first.ID, store.Newer, f)
	if err != nil || next.Status != 503 {
		t.Fatalf("with the 5xx filter the next one should skip the 2xx and land on the 503: %d %v", next.Status, err)
	}
	if _, err := g.hist.Neighbor(t.Context(), next.ID, store.Newer, f); !errors.Is(err, store.ErrNoMore) {
		t.Fatalf("there is no newer 5xx exchange: %v", err)
	}
}
