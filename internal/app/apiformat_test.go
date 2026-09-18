package app

import (
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

// Os instantes da API saem em UTC (docs/api.md, Convenções), qualquer que
// seja o fuso do processo: o início das trocas e o estado vivo dos overrides.

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
			t.Fatalf("sem resposta em %s", p)
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
		t.Fatalf("esperadas duas trocas: %s", res.body)
	}
	for _, it := range page.Items {
		if !utcInstant.MatchString(it.Start) {
			t.Errorf("start fora de UTC: %q", it.Start)
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
		t.Errorf("now fora de UTC: %q", state.Now)
	}
	if len(state.Items) != 1 || state.Items[0].LastAppliedAt == nil {
		t.Fatalf("estado vivo inesperado: %+v", state)
	}
	for _, s := range []string{state.Items[0].RegisteredAt, *state.Items[0].LastAppliedAt} {
		if !utcInstant.MatchString(s) {
			t.Errorf("instante do estado vivo fora de UTC: %q", s)
		}
	}
}

// Uma resposta sintetizada sem corpo nem Content-Type declarado suprime o
// Content-Type automático do net/http com uma lista vazia. O cabeçalho não
// vai ao cliente e também não pode aparecer na troca como null: headers é
// um http.Header, e cada nome aponta uma lista.
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
		t.Fatalf("o cliente não deveria receber Content-Type: %q", ct)
	}
	e.Recorder.Wait(t.Context())

	id := e.lastExchangeID(t)
	raw := e.call(t, "GET", "/exchanges/"+id, "", "")
	if strings.Contains(string(raw.body), "null") {
		t.Fatalf("a troca não deveria ter valores null: %s", raw.body)
	}
	var x struct {
		Response struct {
			Headers map[string][]string `json:"headers"`
		} `json:"response"`
	}
	raw.decode(t, &x)
	if _, ok := x.Response.Headers["Content-Type"]; ok {
		t.Fatalf("Content-Type suprimido não deveria constar da captura: %v", x.Response.Headers)
	}
	if len(x.Response.Headers["X-Gateway"]) != 1 {
		t.Fatalf("os cabeçalhos enviados continuam capturados: %v", x.Response.Headers)
	}
}
