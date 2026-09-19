package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/exchange"
)

// countingUpstream echoes the request back and counts how many it received.
func countingUpstream(t *testing.T, name string) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(echo{name, r.Method, r.URL.Path, r.URL.RawQuery, r.Host, r.Header, body})
	}))
	t.Cleanup(s.Close)
	return s, &n
}

func withOverrides(os ...config.Override) func(*config.Route) {
	return func(r *config.Route) { r.Overrides = append(r.Overrides, os...) }
}

func synth(name, path string, body any) config.Override {
	return config.Override{Name: name, Match: config.OverrideMatch{Path: path}, Respond: &config.Respond{Body: body}}
}

func ptr[T any](v T) *T { return &v }

func post(t *testing.T, url, body string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Add(hdr[i], hdr[i+1])
	}
	return do(t, req)
}

// Requirement: Selective interception with passthrough by default

func TestRouteWithoutOverrideForwardsAll(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	g := capturing(t, recording(), route("payments", up.URL, "/api/payments/*"))
	for _, p := range []string{"/api/payments/a", "/api/payments/b/c", "/api/payments"} {
		res, body := g.get(t, p)
		if e := echoOf(t)(res, body); e.Path != p {
			t.Fatalf("the upstream should have received %s, it got %s", p, e.Path)
		}
		if got := res.Header.Get(HeaderGateway); got != "route=payments" {
			t.Fatalf("want an X-Gateway with no intervention, got %q", got)
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("all three requests should reach the upstream, %d did", hits.Load())
	}
	for _, e := range g.history(t, exchange.Filter{}) {
		if e.Outcome != exchange.OutcomeUpstream || e.Intervened() {
			t.Fatalf("no response should be synthesized: %+v", e)
		}
	}
}

func TestOverrideInterceptsOnlyWhatMatches(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL, "/api/payments/*",
		withOverrides(synth("widget", "/api/payments/widget", "from the override"))))
	res, body := get(t, gw, "/api/payments/widget")
	if res.StatusCode != http.StatusOK || string(body) != "from the override" {
		t.Fatalf("the matching request should be answered by the override: %d %q", res.StatusCode, body)
	}
	if hits.Load() != 0 {
		t.Fatalf("the upstream should not be contacted, it got %d", hits.Load())
	}
	if e := echoOf(t)(get(t, gw, "/api/payments/charge")); e.Path != "/api/payments/charge" {
		t.Fatalf("the non-matching request should go on to the upstream: %+v", e)
	}
	if hits.Load() != 1 {
		t.Fatalf("only the non-matching request should reach the upstream, %d did", hits.Load())
	}
}

func TestRouteWithoutUpstreamAndNoMatchingOverride(t *testing.T) {
	gw := gateway(t, route("mock", "", "/api/*",
		withOverrides(synth("widget", "/api/widget", "ok"))))
	res, body := get(t, gw, "/api/other")
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d: %s", res.StatusCode, body)
	}
	var d diag
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if d.Route != "mock" || !strings.Contains(d.Message, "upstream") || !strings.Contains(d.Message, "override") {
		t.Fatalf("the body should report the route and the missing upstream and override: %+v", d)
	}
	// A matching override answers even without an upstream.
	if res, body := get(t, gw, "/api/widget"); res.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("the override should answer on a route with no upstream: %d %q", res.StatusCode, body)
	}
}

// With no application drawn, the route without an upstream answers 501 as if
// the override did not exist.
func TestRouteWithoutUpstreamAndOverrideNotDrawn(t *testing.T) {
	o := synth("never", "/api/*", "ok")
	o.Probability = ptr(0.0)
	gw := gateway(t, route("mock", "", "/api/*", withOverrides(o)))
	if res, _ := get(t, gw, "/api/x"); res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", res.StatusCode)
	}
}

// Requirement: Override selection criteria (end to end)

func TestMethodRestrictionForwardsOtherMethods(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("post", "/api/payments/charge", "intercepted")
	o.Match.Method = http.MethodPost
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o)))
	if e := echoOf(t)(get(t, gw, "/api/payments/charge")); e.Method != http.MethodGet {
		t.Fatalf("the GET should be forwarded to the upstream: %+v", e)
	}
	if _, body := post(t, gw.URL+"/api/payments/charge", "x"); string(body) != "intercepted" {
		t.Fatalf("the POST should be intercepted: %q", body)
	}
	if hits.Load() != 1 {
		t.Fatalf("only the GET should reach the upstream, %d did", hits.Load())
	}
}

func TestJSONBodyMatchEndToEnd(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := synth("json", "/api/*", "intercepted")
	o.Match.Body = &config.Matcher{JSON: map[string]any{"a": 1, "b": 2}}
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o)))
	if _, body := post(t, gw.URL+"/api/x", `{"b":2,"a":1}`); string(body) != "intercepted" {
		t.Fatalf("a JSON body with the keys in another order should match: %q", body)
	}
	// The body read for matching reaches the upstream intact when it does not
	// match.
	const other = `{"a":1,"b":3}`
	if e := echoOf(t)(post(t, gw.URL+"/api/x", other)); string(e.Body) != other || e.Method != http.MethodPost {
		t.Fatalf("the forwarded body should be the original one: %q", e.Body)
	}
}

// The body read for matching is forwarded intact even with no Content-Length
// and above the capture limit, and the capture records it exactly once.
func TestBodyReadForMatchingIsForwardedWhole(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := synth("never", "/api/*", "intercepted")
	o.Match.Body = &config.Matcher{Equals: ptr("never")}
	s := recording()
	s.CaptureMaxBodyBytes = 16
	g := capturing(t, s, route("payments", up.URL, "/api/*", withOverrides(o)))
	payload := bytes.Repeat([]byte("0123456789"), 1000)
	pr, pw := io.Pipe()
	go func() {
		pw.Write(payload[:5000])
		pw.Write(payload[5000:])
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, g.URL+"/api/x", pr)
	if e := echoOf(t)(do(t, req)); !bytes.Equal(e.Body, payload) {
		t.Fatalf("the upstream should receive the whole body: %d bytes out of %d", len(e.Body), len(payload))
	}
	ex := g.only(t)
	if ex.Request.Size != int64(len(payload)) || !ex.Request.Truncated || string(ex.Request.Body) != string(payload[:16]) {
		t.Fatalf("the capture should see the body exactly once: size %d, truncated %v, %q",
			ex.Request.Size, ex.Request.Truncated, ex.Request.Body)
	}
}

func TestMissingHeaderPreventsInterception(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("acme", "/api/*", "intercepted")
	o.Match.Headers = map[string]config.Matcher{"X-Tenant": {Equals: ptr("acme")}}
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o)))
	echoOf(t)(get(t, gw, "/api/x"))
	if _, body := get(t, gw, "/api/x", "X-Tenant", "acme"); string(body) != "intercepted" {
		t.Fatalf("with the header present the override should match: %q", body)
	}
	if hits.Load() != 1 {
		t.Fatalf("only the request without the header should reach the upstream, %d did", hits.Load())
	}
}

// Requirement: Precedence by specificity (end to end)

func TestOverridePrecedenceEndToEnd(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	post := synth("post", "/api/payments/charge", "post")
	post.Match.Method = http.MethodPost
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(
		synth("api", "/api/*", "api"),
		synth("payments", "/api/payments/*", "payments"),
		synth("widget", "/api/payments/widget", "widget"),
		synth("charge", "/api/payments/charge", "charge"),
		post,
	)))
	for _, c := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/payments/widget", "widget"},
		{http.MethodGet, "/api/payments/other", "payments"},
		{http.MethodGet, "/api/orders", "api"},
		{http.MethodPost, "/api/payments/charge", "post"},
		{http.MethodGet, "/api/payments/charge", "charge"},
	} {
		req, _ := http.NewRequest(c.method, gw.URL+c.path, nil)
		res, body := do(t, req)
		if string(body) != c.want {
			t.Errorf("%s %s: applied %q, want %q", c.method, c.path, body, c.want)
		}
		if got, want := res.Header.Get(HeaderGateway), "route=payments; override=payments/"+c.want+"; intervention=synthesized"; got != want {
			t.Errorf("%s %s: X-Gateway %q, want %q", c.method, c.path, got, want)
		}
	}
}

// Requirement: Response declared by the override

func TestDeclaredResponseReturnedExactly(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := config.Override{
		Name:  "flaky",
		Match: config.OverrideMatch{Path: "/api/payments/*"},
		Respond: &config.Respond{
			Status:  http.StatusOK,
			Headers: map[string]config.HeaderValues{"X-Source": {"override"}},
			Body:    map[string]any{"id": 42, "status": "paid"},
		},
	}
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o)))
	res, body := rawGet(t, gw, "/api/payments/x")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	if res.Header.Get("X-Source") != "override" {
		t.Fatalf("the declared header should get through: %v", res.Header)
	}
	if string(body) != `{"id":42,"status":"paid"}` {
		t.Fatalf("unexpected body: %s", body)
	}
	if hits.Load() != 0 {
		t.Fatalf("the upstream should not be contacted, it got %d", hits.Load())
	}
	// Besides what was declared, only X-Gateway and HTTP's own headers.
	for k := range res.Header {
		if !slices.Contains([]string{"X-Source", "Content-Type", HeaderGateway, "Content-Length", "Date"}, k) {
			t.Errorf("unexpected header on the synthesized response: %s", k)
		}
	}
}

func TestDefaultStatusIs200(t *testing.T) {
	gw := gateway(t, route("mock", "", "/*", withOverrides(synth("o", "/*", "body only"))))
	res, body := get(t, gw, "/x")
	if res.StatusCode != http.StatusOK || string(body) != "body only" {
		t.Fatalf("want 200 with the declared body, got %d %q", res.StatusCode, body)
	}
	// A text body with no declared type does not get an inferred one.
	if ct, ok := res.Header["Content-Type"]; ok {
		t.Fatalf("no content type should be inferred from text: %q", ct)
	}
}

func TestContentTypeInferredFromJSONBody(t *testing.T) {
	gw := gateway(t, route("mock", "", "/*", withOverrides(
		synth("json", "/json", map[string]any{"b": []any{1, 2}, "a": "x"}),
		config.Override{
			Name: "problem", Match: config.OverrideMatch{Path: "/problem"},
			Respond: &config.Respond{
				Status:  http.StatusBadRequest,
				Headers: map[string]config.HeaderValues{"Content-Type": {"application/problem+json"}},
				Body:    map[string]any{"title": "invalid"},
			},
		},
	)))
	res, body := get(t, gw, "/json")
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil || v["a"] != "x" {
		t.Fatalf("the body should be the serialized JSON: %s %v", body, err)
	}
	res, _ = get(t, gw, "/problem")
	if res.StatusCode != http.StatusBadRequest || res.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("the declared type should win: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

// Identification with an intervention (gateway-routing) and a synthesized
// response marked in the capture (traffic-capture), with a real override.
func TestSynthesizedResponseIdentifiedAndRecorded(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := config.Override{
		Name: "flaky", Match: config.OverrideMatch{Path: "/api/payments/*"},
		Respond: &config.Respond{Status: http.StatusServiceUnavailable, Body: "unavailable"},
	}
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	res, _ := g.get(t, "/api/payments/x")
	if got := res.Header.Values(HeaderGateway); len(got) != 1 ||
		got[0] != "route=payments; override=payments/flaky; intervention=synthesized" {
		t.Fatalf("X-Gateway should identify route, override and intervention: %q", got)
	}
	e := g.only(t)
	if e.Status != http.StatusServiceUnavailable || e.Outcome != exchange.OutcomeSynthesized ||
		e.Override != "payments/flaky" || !slices.Contains(e.Interventions, "synthesized") {
		t.Fatalf("the exchange should show up as synthesized by the override: %+v", e)
	}
	if string(e.Response.Body) != "unavailable" {
		t.Fatalf("the capture should record the synthesized body: %q", e.Response.Body)
	}
}

// The "A synthesized response counts no upstream time" scenario from the
// traffic-capture spec, with the response synthesized by a real override.
func TestSynthesizedResponseHasZeroUpstreamTime(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	g := capturing(t, recording(), route("payments", up.URL, "/api/*",
		withOverrides(synth("o", "/api/*", "ok"))))
	g.get(t, "/api/x")
	e := g.only(t)
	if e.Timing.UpstreamMs != 0 || e.Outcome != exchange.OutcomeSynthesized {
		t.Fatalf("with no contact with the upstream the upstream time should be zero: %+v %s", e.Timing, e.Outcome)
	}
}

// Requirement: Application probability

func seeded(seed uint64) config.Settings {
	s := recording()
	s.Seed = &seed
	return s
}

func TestProbabilityAbsentAppliesAlways(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(synth("o", "/api/*", "override"))))
	for i := range 20 {
		if _, body := get(t, gw, "/api/x"); string(body) != "override" {
			t.Fatalf("request %d should be answered by the override: %q", i+1, body)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("no request should reach the upstream, %d did", hits.Load())
	}
}

func TestFractionalProbabilitySplits(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("o", "/api/*", "override")
	o.Probability = ptr(0.3)
	g := capturing(t, seeded(42), route("payments", up.URL, "/api/*", withOverrides(o)))
	const n = 1000
	overridden := 0
	for range n {
		res, body := g.get(t, "/api/x")
		switch {
		case string(body) == "override":
			overridden++
			if got := res.Header.Get(HeaderGateway); !strings.Contains(got, "intervention=synthesized") {
				t.Fatalf("override response with no identification: %q", got)
			}
		default:
			echoOf(t)(res, body)
			if got := res.Header.Get(HeaderGateway); got != "route=payments" {
				t.Fatalf("an upstream response should not be marked: %q", got)
			}
		}
	}
	// Binomial(1000, 0.3): standard deviation about 14.5; the tolerance is 4
	// deviations.
	if overridden < 242 || overridden > 358 {
		t.Fatalf("want around 300 override responses, got %d", overridden)
	}
	if got := hits.Load(); got != int64(n-overridden) {
		t.Fatalf("the other %d should reach the upstream, %d did", n-overridden, got)
	}
}

func TestProbabilityZeroNeverApplies(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("o", "/api/*", "override")
	o.Probability = ptr(0.0)
	g := capturing(t, seeded(1), route("payments", up.URL, "/api/*", withOverrides(o)))
	for range 50 {
		echoOf(t)(g.get(t, "/api/x"))
	}
	if hits.Load() != 50 {
		t.Fatalf("all of them should reach the upstream, %d did", hits.Load())
	}
	if with := g.history(t, exchange.Filter{Intervened: ptr(true)}); len(with) != 0 {
		t.Fatalf("no exchange should show up as an intervention: %v", with)
	}
}

// An override that is not drawn neither hides the upstream nor is replaced by
// a less specific one: the request carries on as if it did not exist.
func TestNotDrawnBehavesAsIfAbsent(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := synth("exact", "/api/x", "exact")
	o.Probability = ptr(0.0)
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o, synth("wildcard", "/api/*", "wildcard"))))
	// The most specific one is selected and, not being drawn, the request
	// goes on to the upstream: the draw decides about the override resolved
	// in step 3.
	if e := echoOf(t)(get(t, gw, "/api/x")); e.Path != "/api/x" {
		t.Fatalf("the request should go on to the upstream: %+v", e)
	}
}

// Requirement: Determinism by seed

// intercepted sends the sequence of requests and returns the indexes of the
// ones answered by the override.
func intercepted(t *testing.T, s config.Settings, n int) []int {
	t.Helper()
	up, _ := countingUpstream(t, "payments")
	o := synth("o", "/api/*", "override")
	o.Probability = ptr(0.5)
	g := capturing(t, s, route("payments", up.URL, "/api/*", withOverrides(o)))
	var out []int
	for i := range n {
		if _, body := g.get(t, fmt.Sprintf("/api/req/%d", i)); string(body) == "override" {
			out = append(out, i)
		}
	}
	return out
}

func TestSameSeedReproducesSequence(t *testing.T) {
	a, b := intercepted(t, seeded(42), 100), intercepted(t, seeded(42), 100)
	if !slices.Equal(a, b) {
		t.Fatalf("the same seed should intercept the same requests:\n%v\n%v", a, b)
	}
	if len(a) == 0 || len(a) == 100 {
		t.Fatalf("with probability 0.5 there should be both intercepted and forwarded ones: %v", a)
	}
}

func TestDistinctSeedsDiverge(t *testing.T) {
	if a, b := intercepted(t, seeded(42), 100), intercepted(t, seeded(43), 100); slices.Equal(a, b) {
		t.Fatalf("distinct seeds should intercept different sets: %v", a)
	}
}

func TestNoSeedIsNotReproducible(t *testing.T) {
	if a, b := intercepted(t, recording(), 100), intercepted(t, recording(), 100); slices.Equal(a, b) {
		t.Fatalf("with no seed the runs should not line up: %v", a)
	}
}

// Under concurrent requests, each sequence number gets the same decision on
// both runs: the draw does not depend on scheduling order.
func TestSeedDeterministicUnderConcurrency(t *testing.T) {
	run := func() map[uint64]bool {
		up, _ := countingUpstream(t, "payments")
		o := synth("o", "/api/*", "override")
		o.Probability = ptr(0.5)
		g := capturing(t, seeded(7), route("payments", up.URL, "/api/*", withOverrides(o)))
		var wg sync.WaitGroup
		for i := range 100 {
			wg.Go(func() {
				req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/req/%d", g.URL, i), nil)
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			})
		}
		wg.Wait()
		out := map[uint64]bool{}
		for _, e := range g.history(t, exchange.Filter{}) {
			out[e.Seq] = e.Outcome == exchange.OutcomeSynthesized
		}
		return out
	}
	a, b := run(), run()
	if len(a) != 100 || len(b) != 100 {
		t.Fatalf("want 100 exchanges on each run: %d and %d", len(a), len(b))
	}
	for seq, v := range a {
		if b[seq] != v {
			t.Fatalf("sequence %d got different decisions on the two runs", seq)
		}
	}
}
