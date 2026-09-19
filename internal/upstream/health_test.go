package upstream

import (
	"testing"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
)

func routes(pairs ...string) []*config.CompiledRoute {
	var out []*config.CompiledRoute
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, &config.CompiledRoute{Doc: config.Route{Name: pairs[i], Upstream: pairs[i+1]}})
	}
	return out
}

const (
	a = "http://localhost:9001"
	b = "http://localhost:9002"
)

func find(t *testing.T, items []Item, u string) Item {
	t.Helper()
	for _, it := range items {
		if it.Upstream == u {
			return it
		}
	}
	t.Fatalf("upstream %s missing from %+v", u, items)
	return Item{}
}

func TestReportGroupsRoutesByUpstream(t *testing.T) {
	h := New()
	items := h.Report(routes("payments", a, "catalog", b, "catalog-admin", b, "mock", ""))
	if len(items) != 2 {
		t.Fatalf("want two upstreams (a route without an upstream is left out): %+v", items)
	}
	if items[0].Upstream != a || items[1].Upstream != b {
		t.Fatalf("precedence order not respected: %+v", items)
	}
	if got := items[1].Routes; len(got) != 2 || got[0] != "catalog" || got[1] != "catalog-admin" {
		t.Fatalf("routes for %s: %v", b, got)
	}
	for _, it := range items {
		if it.Status != StatusUnknown || it.LastSuccessAt != nil || it.LastFailureAt != nil {
			t.Fatalf("with no attempts an upstream is unknown: %+v", it)
		}
	}
}

func TestDownAfterThreeFailuresAndUpOnResponse(t *testing.T) {
	h := New()
	rs := routes("catalog", b)
	h.Success(b)
	h.Failure(b, "connection refused")
	h.Failure(b, "connection refused")
	if it := find(t, h.Report(rs), b); it.Status != StatusUp || it.Recent != (Recent{3, 2}) {
		t.Fatalf("two consecutive failures are still not enough: %+v", it)
	}
	h.Failure(b, "dial tcp 127.0.0.1:9002: connect: connection refused")
	it := find(t, h.Report(rs), b)
	if it.Status != StatusDown {
		t.Fatalf("three consecutive failures should mark it down: %+v", it)
	}
	if it.LastError != "dial tcp 127.0.0.1:9002: connect: connection refused" || it.LastFailureAt == nil || it.LastSuccessAt == nil {
		t.Fatalf("details of the last failure: %+v", it)
	}
	h.Success(b)
	if it := find(t, h.Report(rs), b); it.Status != StatusUp {
		t.Fatalf("a single response brings it back up: %+v", it)
	}
}

func TestFewerThanThreeAttemptsNeverDown(t *testing.T) {
	h := New()
	h.Failure(a, "x")
	h.Failure(a, "x")
	if it := find(t, h.Report(routes("p", a)), a); it.Status != StatusUp || it.Recent.Failures != 2 {
		t.Fatalf("%+v", it)
	}
}

func TestRecentWindowKeepsLastTwenty(t *testing.T) {
	h := New()
	for range 25 {
		h.Failure(a, "x")
	}
	for range 5 {
		h.Success(a)
	}
	it := find(t, h.Report(routes("p", a)), a)
	if it.Recent != (Recent{Window, Window - 5}) || it.Status != StatusUp {
		t.Fatalf("window of the last %d attempts: %+v", Window, it)
	}
}

func TestUndeclaredUpstreamIsForgotten(t *testing.T) {
	h := New()
	for range 3 {
		h.Failure(b, "x")
	}
	h.Report(routes("p", a))
	if it := find(t, h.Report(routes("p", a, "c", b)), b); it.Status != StatusUnknown || it.Recent.Attempts != 0 {
		t.Fatalf("an upstream that stopped being declared comes back unknown: %+v", it)
	}
}

func TestNilHealthIgnoresAttempts(t *testing.T) {
	var h *Health
	h.Success(a)
	h.Failure(a, "x")
}

func TestTimesAreUTC(t *testing.T) {
	h := New()
	h.now = func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.FixedZone("BRT", -3*3600)) }
	h.Success(a)
	it := find(t, h.Report(routes("p", a)), a)
	if it.LastSuccessAt.Location() != time.UTC {
		t.Fatalf("instants must come out in UTC: %v", it.LastSuccessAt)
	}
}
