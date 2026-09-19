package app

import (
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

// The API's instants come out in UTC (docs/api.md, Conventions), whatever the
// process's time zone: the start of the exchanges and the live state of the
// overrides.

var utcInstant = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z$`)

func TestAPIInstantsAreUTC(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{
		"payments.yaml": routeDoc("payments", up.URL, "/payments/*") +
			"overrides:\n  - name: flaky\n    match:\n      path: /payments/flaky\n    respond:\n      status: 503\n    ttl: 1m\n    maxApplications: 5\n",
	})
	for _, p := range []string{"/payments/ok", "/payments/flaky"} {
		if st, _ := getBody(t, e.traffic+p); st == 0 {
			t.Fatalf("no response on %s", p)
		}
	}
	e.Recorder.Wait(t.Context())

	var page struct {
		Items []struct {
			Start string `json:"start"`
		} `json:"items"`
	}
	res := e.call(t, "GET", "/exchanges", "", "")
	if res.status != http.StatusOK {
		t.Fatalf("GET /api/exchanges: %d %s", res.status, res.body)
	}
	res.decode(t, &page)
	if len(page.Items) != 2 {
		t.Fatalf("want two exchanges: %s", res.body)
	}
	for _, it := range page.Items {
		if !utcInstant.MatchString(it.Start) {
			t.Errorf("start is not UTC: %q", it.Start)
		}
	}

	var state struct {
		Now   string `json:"now"`
		Items []struct {
			RegisteredAt  string  `json:"registeredAt"`
			LastAppliedAt *string `json:"lastAppliedAt"`
		} `json:"items"`
	}
	e.call(t, "GET", "/overrides/state", "", "").decode(t, &state)
	if !utcInstant.MatchString(state.Now) {
		t.Errorf("now is not UTC: %q", state.Now)
	}
	if len(state.Items) != 1 || state.Items[0].LastAppliedAt == nil {
		t.Fatalf("unexpected live state: %+v", state)
	}
	for _, s := range []string{state.Items[0].RegisteredAt, *state.Items[0].LastAppliedAt} {
		if !utcInstant.MatchString(s) {
			t.Errorf("live-state instant is not UTC: %q", s)
		}
	}
}

// A synthesized response with neither a body nor a declared Content-Type
// suppresses net/http's automatic Content-Type with an empty list. The header
// does not reach the client, and it must not show up in the exchange as null
// either: headers is an http.Header, and every name maps to a list.
func TestSuppressedHeaderNotCapturedAsNull(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{
		"payments.yaml": routeDoc("payments", up.URL, "/payments/*") +
			"overrides:\n  - name: down\n    match:\n      path: /payments/down\n    respond:\n      status: 503\n",
	})
	res, err := http.Get(e.traffic + "/payments/down")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if ct, ok := res.Header["Content-Type"]; ok {
		t.Fatalf("the client should not receive a Content-Type: %q", ct)
	}
	e.Recorder.Wait(t.Context())

	id := e.lastExchangeID(t)
	raw := e.call(t, "GET", "/exchanges/"+id, "", "")
	if strings.Contains(string(raw.body), "null") {
		t.Fatalf("the exchange should have no null values: %s", raw.body)
	}
	var x struct {
		Response struct {
			Headers map[string][]string `json:"headers"`
		} `json:"response"`
	}
	raw.decode(t, &x)
	if _, ok := x.Response.Headers["Content-Type"]; ok {
		t.Fatalf("a suppressed Content-Type should not show up in the capture: %v", x.Response.Headers)
	}
	if len(x.Response.Headers["X-Gateway"]) != 1 {
		t.Fatalf("the headers that were sent are still captured: %v", x.Response.Headers)
	}
}
