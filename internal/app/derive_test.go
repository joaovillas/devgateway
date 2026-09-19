package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/joaovillas/devgateway/internal/config"
)

// Requirement: Deriving an override from a captured exchange

// chargeUpstream answers like a payments service, with headers of its own, a
// repeated header and a compact JSON body.
func chargeUpstream(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/payments/charge":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-Cost", "7")
			w.Header().Add("Set-Cookie", "a=1")
			w.Header().Add("Set-Cookie", "b=2")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"amount":100,"id":"ch_1","status":"paid"}`)
		case "/payments/receipt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "receipt 42\nthank you")
		default:
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, strings.Repeat("abcdefghij", 10))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

type deriveDraft struct {
	Route    string          `json:"route"`
	Override config.Override `json:"override"`
	Warnings []string        `json:"warnings"`
}

// lastExchangeID returns the identifier of the newest exchange in the
// history.
func (e *adminEnv) lastExchangeID(t *testing.T) string {
	t.Helper()
	var l listBody
	if r := e.call(t, "GET", "/exchanges?limit=1", "", ""); r.status != 200 {
		t.Fatalf("listing: %d %s", r.status, r.body)
	} else {
		r.decode(t, &l)
	}
	if len(l.Items) == 0 {
		t.Fatal("the history should hold the exchange")
	}
	return l.Items[0].ID
}

type observed struct {
	status int
	header http.Header
	body   string
}

func post(t *testing.T, url, body string) observed {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return observed{res.StatusCode, res.Header, string(b)}
}

// Scenario: A derived override replays the observed exchange — with a review
// before it takes effect: the draft writes nothing, and the override created
// from it goes on to answer exactly as the upstream did.

func TestDerivedOverrideReproducesExchange(t *testing.T) {
	var hits atomic.Int64
	up := chargeUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{"payments.yaml": routeDoc("payments", up.URL, "/payments/*")})
	doc := filepath.Join(e.routes, "payments.yaml")

	real := post(t, e.traffic+"/payments/charge", `{"amount":100}`)
	id := e.lastExchangeID(t)
	before := stateOf(t, doc)

	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`"}`)
	var d deriveDraft
	r.decode(t, &d)
	o := d.Override
	if r.status != 200 || d.Route != "payments" || len(d.Warnings) != 0 {
		t.Fatalf("draft: %d %s", r.status, r.body)
	}
	if o.Name != "post-payments-charge" || o.Match.Path != "/payments/charge" || o.Match.Method != "POST" ||
		o.Respond == nil || o.Respond.Status != 201 || o.Enabled() != true {
		t.Fatalf("criteria and response of the draft: %+v", o)
	}
	if o.Source == nil || o.Source.Kind != config.SourceDerived || o.Source.Exchange != id || o.Source.At.IsZero() || o.Source.BodyIncomplete {
		t.Fatalf("origin of the draft: %+v", o.Source)
	}
	if !slices.Equal(o.Respond.Headers["Set-Cookie"], []string{"a=1", "b=2"}) || o.Respond.Headers["X-Request-Cost"][0] != "7" {
		t.Fatalf("headers of the draft: %v", o.Respond.Headers)
	}
	for _, k := range []string{"Date", "Content-Length", "X-Gateway"} {
		if _, ok := o.Respond.Headers[k]; ok {
			t.Fatalf("%s should not make it into the declared response", k)
		}
	}
	if stateOf(t, doc) != before {
		t.Fatal("the draft should write nothing")
	}
	// Until it is created, the draft does not apply: the upstream keeps
	// answering.
	if post(t, e.traffic+"/payments/charge", `{}`); hits.Load() != 2 {
		t.Fatalf("the draft should not intercept: %d calls to the upstream", hits.Load())
	}

	// Review: the name is changed and the override is created.
	o.Name = "charge-ok"
	body, _ := json.Marshal(o)
	if r := e.call(t, "POST", "/routes/payments/overrides", "", string(body)); r.status != 201 {
		t.Fatalf("creating the reviewed override: %d %s", r.status, r.body)
	}
	got := post(t, e.traffic+"/payments/charge", `{"amount":100}`)
	if hits.Load() != 2 {
		t.Fatalf("the override should answer without the upstream: %d calls", hits.Load())
	}
	if got.status != real.status || got.body != real.body {
		t.Fatalf("the response should replay the observed one:\nreal:    %d %s\nderived: %d %s", real.status, real.body, got.status, got.body)
	}
	for _, k := range []string{"Content-Type", "X-Request-Cost", "Set-Cookie", "Content-Length"} {
		if !slices.Equal(got.header.Values(k), real.header.Values(k)) {
			t.Fatalf("header %s: real %q, derived %q", k, real.header.Values(k), got.header.Values(k))
		}
	}
	if gw := got.header.Get("X-Gateway"); !strings.Contains(gw, "override=payments/charge-ok") || !strings.Contains(gw, "intervention=synthesized") {
		t.Fatalf("the response should identify the intervention: %q", gw)
	}
}

// save writes straight away; a text body is replayed byte for byte.
func TestDeriveAndSave(t *testing.T) {
	var hits atomic.Int64
	up := chargeUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{"payments.yaml": routeDoc("payments", up.URL, "/payments/*")})

	real := fetch(e.traffic + "/payments/receipt")
	id := e.lastExchangeID(t)
	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`","name":"receipt","save":true}`)
	var res struct {
		overrideRes
		Warnings []string `json:"warnings"`
	}
	r.decode(t, &res)
	if r.status != 201 || r.header.Get("Location") != "/api/routes/payments/overrides/receipt" || res.Override.Name != "receipt" ||
		res.Override.Source == nil || res.Override.Source.Kind != config.SourceDerived || res.Warnings == nil {
		t.Fatalf("derivation written: %d %s", r.status, r.body)
	}
	data, _ := os.ReadFile(filepath.Join(e.routes, "payments.yaml"))
	if !strings.Contains(string(data), "kind: derived") || !strings.Contains(string(data), "exchange: "+id) {
		t.Fatalf("the document should record the origin:\n%s", data)
	}
	got := fetch(e.traffic + "/payments/receipt")
	if hits.Load() != 1 || got.status != real.status || got.body != real.body {
		t.Fatalf("the written override should replay the exchange: %+v, real %+v", got, real)
	}
	// A name already in use is a conflict.
	r = e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`","name":"receipt","save":true}`)
	if r.status != 409 || r.err(t).Error != "conflict" {
		t.Fatalf("repeated name: %d %s", r.status, r.body)
	}
}

// Scenario: Unknown exchange

func TestDeriveUnknownExchange(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	before := snapshotDir(t, e.routes)
	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"01K5E3V3C8Q2M4Z8N6P0R2T4W6","save":true}`)
	if ae := r.err(t); r.status != 404 || ae.Error != "not_found" || !strings.Contains(ae.Message, "01K5E3V3C8Q2M4Z8N6P0R2T4W6") ||
		!strings.Contains(ae.Message, "not found") {
		t.Fatalf("unknown exchange: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes/none/overrides/derive", "", `{"exchange":"x"}`); r.status != 404 {
		t.Fatalf("unknown route: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{}`); r.status != 422 || r.err(t).Field != "exchange" {
		t.Fatalf("no exchange given: %d %s", r.status, r.body)
	}
	if after := snapshotDir(t, e.routes); !mapsEqual(before, after) {
		t.Fatal("no document should change")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Scenario: Deriving from a truncated exchange — the override is created
// flagging explicitly that the body is incomplete.

func TestDeriveTruncatedExchange(t *testing.T) {
	var hits atomic.Int64
	up := chargeUpstream(t, &hits)
	e := startAdmin(t, `{"ports":{"traffic":0,"admin":0},"capture":{"maxBodyBytes":16}}`,
		map[string]string{"payments.yaml": routeDoc("payments", up.URL, "/payments/*")})
	getBody(t, e.traffic+"/payments/long")
	id := e.lastExchangeID(t)

	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`"}`)
	var d deriveDraft
	r.decode(t, &d)
	if r.status != 200 || d.Override.Source == nil || !d.Override.Source.BodyIncomplete || len(d.Warnings) != 1 ||
		!strings.Contains(d.Warnings[0], "truncated") || !strings.Contains(d.Warnings[0], "16 of 100") {
		t.Fatalf("draft of a truncated exchange: %d %s", r.status, r.body)
	}
	r = e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`","save":true}`)
	if r.status != 201 {
		t.Fatalf("writing a truncated exchange: %d %s", r.status, r.body)
	}
	data, _ := os.ReadFile(filepath.Join(e.routes, "payments.yaml"))
	if !strings.Contains(string(data), "bodyIncomplete: true") {
		t.Fatalf("the document should flag the incomplete body:\n%s", data)
	}
}

// Only an upstream response can be replayed; and without history exposure the
// exchange is not read at all.

func TestDeriveRefusesExchangeWithoutUpstreamResponse(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	getBody(t, e.traffic+"/no-route")
	id := e.lastExchangeID(t)
	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`"}`)
	if ae := r.err(t); r.status != 422 || ae.Field != "exchange" || !strings.Contains(ae.Message, "gateway") {
		t.Fatalf("exchange answered by the gateway: %d %s", r.status, r.body)
	}
}

func TestDeriveWithHistoryHidden(t *testing.T) {
	e := startAdmin(t, `{"ports":{"traffic":0,"admin":0},"history":{"expose":false}}`, adminRoutes(t))
	getBody(t, e.traffic+"/payments/x")
	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"01K5E3V3C8Q2M4Z8N6P0R2T4W6"}`)
	if r.status != 403 || r.err(t).Error != "history_disabled" {
		t.Fatalf("derivation without exposure: %d %s", r.status, r.body)
	}
}
