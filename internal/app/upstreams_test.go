package app

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/gamerjp64/devgateway/internal/upstream"
)

// Upstream availability for the map (Requirement: Topology map, scenario
// "An unavailable upstream is flagged").

type upstreamsData struct {
	Items []upstream.Item `json:"items"`
}

func (e *adminEnv) upstreams(t *testing.T) map[string]upstream.Item {
	t.Helper()
	res := e.call(t, "GET", "/upstreams", "", "")
	if res.status != http.StatusOK {
		t.Fatalf("GET /api/upstreams: %d %s", res.status, res.body)
	}
	var d upstreamsData
	res.decode(t, &d)
	out := map[string]upstream.Item{}
	for _, it := range d.Items {
		out[it.Upstream] = it
	}
	return out
}

func TestUpstreamRefusingConnectionsIsDown(t *testing.T) {
	failing := httpServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	closed := fmt.Sprintf("http://127.0.0.1:%d", freePort(t))
	e := startAdmin(t, freePorts, map[string]string{
		"payments.yaml": routeDoc("payments", failing, "/payments/*"),
		"catalog.yaml":  routeDoc("catalog", closed, "/catalog/*"),
		"stock.yaml":    routeDoc("stock", closed, "/stock/*"),
	})
	s := openEvents(t, e.api+"/events")
	s.next(t, "hello")

	got := e.upstreams(t)
	if len(got) != 2 {
		t.Fatalf("want two upstreams: %+v", got)
	}
	if it := got[closed]; it.Status != upstream.StatusUnknown || len(it.Routes) != 2 {
		t.Fatalf("before any attempt: unknown and carrying both routes: %+v", it)
	}

	for range 3 {
		if st, _ := getBody(t, e.traffic+"/payments/x"); st != http.StatusInternalServerError {
			t.Fatalf("the upstream's 500 should reach the client, got %d", st)
		}
		if st, _ := getBody(t, e.traffic+"/catalog/x"); st != http.StatusBadGateway {
			t.Fatalf("a refused connection should give a 502, got %d", st)
		}
	}

	got = e.upstreams(t)
	if it := got[failing]; it.Status != upstream.StatusUp || it.Recent.Attempts != 3 || it.Recent.Failures != 0 {
		t.Fatalf("a 5xx from the upstream itself does not make it unavailable: %+v", it)
	}
	it := got[closed]
	if it.Status != upstream.StatusDown || it.Recent != (upstream.Recent{Attempts: 3, Failures: 3}) {
		t.Fatalf("three refused connections should mark it as down: %+v", it)
	}
	if it.LastError == "" || it.LastFailureAt == nil || it.LastSuccessAt != nil {
		t.Fatalf("failure details are missing: %+v", it)
	}

	// The stream announces the status change without the panel having to ask.
	for {
		var d upstreamsData
		s.next(t, "upstreams").decode(t, &d)
		down := false
		for _, it := range d.Items {
			down = down || (it.Upstream == closed && it.Status == upstream.StatusDown)
		}
		if down {
			break
		}
	}
}
