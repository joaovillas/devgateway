package override

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gamerjp64/devgateway/internal/config"
)

func str(s string) *string { return &s }

func eq(s string) config.Matcher       { return config.Matcher{Equals: str(s)} }
func re(s string) config.Matcher       { return config.Matcher{Regex: str(s)} }
func contains(s string) config.Matcher { return config.Matcher{Contains: str(s)} }
func jsonEq(v any) config.Matcher      { return config.Matcher{JSON: v} }

func respond(body any) *config.Respond { return &config.Respond{Body: body} }

// compiled validates and compiles a route with the given overrides, in
// declaration order.
func compiled(t *testing.T, overrides ...config.Override) *config.CompiledRoute {
	t.Helper()
	r := config.Route{
		SchemaVersion: 1, Name: "payments", Upstream: "http://upstream.invalid",
		Match: config.RouteMatch{Path: "/api/*"}, Overrides: overrides,
	}
	routes, err := config.BuildRoutes([]config.RouteDoc{config.NewRouteDoc("payments.yaml", r)})
	if err != nil {
		t.Fatal(err)
	}
	return routes[0]
}

func ov(name string, m config.OverrideMatch) config.Override {
	return config.Override{Name: name, Match: m, Respond: respond(name)}
}

func request(method, target, body string, hdr ...string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	return r
}

// selected returns the name of the override chosen for the request, or "".
func selected(t *testing.T, rt *config.CompiledRoute, r *http.Request) string {
	t.Helper()
	if o := Select(rt, NewRequest(r)); o != nil {
		return o.Doc.Name
	}
	return ""
}

// Requirement: Override selection criteria

func TestExactPath(t *testing.T) {
	rt := compiled(t, ov("bilulu", config.OverrideMatch{Path: "/api/payments/bilulu"}))
	if got := selected(t, rt, request("GET", "/api/payments/bilulu", "")); got != "bilulu" {
		t.Fatalf("the exact path should match, chosen %q", got)
	}
	for _, p := range []string{"/api/payments/bilulu/x", "/api/payments/bilul", "/api/payments"} {
		if got := selected(t, rt, request("GET", p, "")); got != "" {
			t.Errorf("the exact path should not match %s, chosen %q", p, got)
		}
	}
}

func TestWildcardPath(t *testing.T) {
	rt := compiled(t, ov("all", config.OverrideMatch{Path: "/api/payments/*"}))
	if got := selected(t, rt, request("GET", "/api/payments/charge/42", "")); got != "all" {
		t.Fatalf("the wildcard should match a deeper path, chosen %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/paymentsx", "")); got != "" {
		t.Fatalf("the wildcard should not match a partial segment prefix, chosen %q", got)
	}
}

func TestSegmentParamPath(t *testing.T) {
	rt := compiled(t, ov("zipcode", config.OverrideMatch{Path: "/zip/:id/json"}))
	if got := selected(t, rt, request("GET", "/zip/40415345/json", "")); got != "zipcode" {
		t.Fatalf("the segment parameter should match one segment, chosen %q", got)
	}
	for _, p := range []string{"/zip/40415345/extra/json", "/zip//json", "/zip/40415345", "/zip/40415345/json/", "/zip/40415345/xml"} {
		if got := selected(t, rt, request("GET", p, "")); got != "" {
			t.Errorf("the segment parameter should not match %s, chosen %q", p, got)
		}
	}
}

// Segment parameters in the fixed part of a wildcard: the parameter matches
// one segment and the wildcard matches whatever comes after it.
func TestSegmentParamBeforeWildcard(t *testing.T) {
	rt := compiled(t, ov("user", config.OverrideMatch{Path: "/api/users/:id/*"}))
	for p, want := range map[string]string{
		"/api/users/42":          "user",
		"/api/users/42/orders/1": "user",
		"/api/users//orders":     "",
		"/api/users":             "",
	} {
		if got := selected(t, rt, request("GET", p, "")); got != want {
			t.Errorf("%s: chosen %q, want %q", p, got, want)
		}
	}
}

func TestRegexPath(t *testing.T) {
	rt := compiled(t, ov("charge", config.OverrideMatch{PathRegex: `^/api/payments/charge/\d+$`}))
	if got := selected(t, rt, request("GET", "/api/payments/charge/42", "")); got != "charge" {
		t.Fatalf("the regular expression should match, chosen %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/payments/charge/abc", "")); got != "" {
		t.Fatalf("the regular expression should not match, chosen %q", got)
	}
}

func TestMethodRestriction(t *testing.T) {
	rt := compiled(t, ov("post", config.OverrideMatch{Path: "/api/payments/charge", Method: "POST"}))
	if got := selected(t, rt, request("GET", "/api/payments/charge", "")); got != "" {
		t.Fatalf("a GET should not match an override restricted to POST, chosen %q", got)
	}
	if got := selected(t, rt, request("POST", "/api/payments/charge", "")); got != "post" {
		t.Fatalf("a POST should match, chosen %q", got)
	}
}

func TestHeaderCriteria(t *testing.T) {
	rt := compiled(t, ov("acme", config.OverrideMatch{
		Path:    "/api/*",
		Headers: map[string]config.Matcher{"x-tenant": eq("acme")},
	}))
	if got := selected(t, rt, request("GET", "/api/x", "")); got != "" {
		t.Fatalf("without the required header the override should not match, chosen %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/x", "", "X-Tenant", "other")); got != "" {
		t.Fatalf("with another value the override should not match, chosen %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/x", "", "X-Tenant", "other", "X-Tenant", "acme")); got != "acme" {
		t.Fatalf("one of the repeated values matching should be enough, chosen %q", got)
	}
}

func TestHeaderOperators(t *testing.T) {
	for _, c := range []struct {
		name  string
		m     config.Matcher
		value string
		want  bool
	}{
		{"equality matches", eq("Bearer abc"), "Bearer abc", true},
		{"equality is exact", eq("Bearer abc"), "Bearer abcd", false},
		{"regex matches", re(`^Bearer [a-z]+$`), "Bearer abc", true},
		{"regex does not match", re(`^Bearer [a-z]+$`), "Bearer 123", false},
		{"substring matches", contains("abc"), "Bearer xabcx", true},
		{"substring does not match", contains("abc"), "Bearer xyz", false},
		{"JSON matches ignoring whitespace and order", jsonEq(map[string]any{"a": 1, "b": "x"}), `{ "b": "x", "a": 1 }`, true},
		{"different JSON does not match", jsonEq(map[string]any{"a": 1}), `{"a":2}`, false},
		{"a value that is not JSON does not match", jsonEq(map[string]any{"a": 1}), `a=1`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := compiled(t, ov("o", config.OverrideMatch{
				Path: "/api/*", Headers: map[string]config.Matcher{"Authorization": c.m},
			}))
			got := selected(t, rt, request("GET", "/api/x", "", "Authorization", c.value)) == "o"
			if got != c.want {
				t.Fatalf("matched = %v, want %v", got, c.want)
			}
		})
	}
}

func TestHostHeaderCriterion(t *testing.T) {
	rt := compiled(t, ov("host", config.OverrideMatch{
		Path: "/api/*", Headers: map[string]config.Matcher{"Host": eq("payments.local")},
	}))
	r := request("GET", "/api/x", "")
	r.Host = "payments.local"
	if got := selected(t, rt, r); got != "host" {
		t.Fatalf("the Host criterion should use the request's host, chosen %q", got)
	}
}

func TestQueryCriteria(t *testing.T) {
	rt := compiled(t, ov("q", config.OverrideMatch{
		Path: "/api/*",
		Query: map[string]config.Matcher{
			"status": eq("failed"),
			"id":     re(`^\d+$`),
			"tag":    contains("vip"),
		},
	}))
	for target, want := range map[string]string{
		"/api/x?status=failed&id=42&tag=customer-vip":      "q",
		"/api/x?status=failed&id=42&tag=a&tag=vip":         "q",
		"/api/x?status=failed&id=abc&tag=vip":              "",
		"/api/x?status=ok&id=42&tag=vip":                   "",
		"/api/x?status=failed&id=42":                       "",
		"/api/x?STATUS=failed&id=42&tag=vip":               "",
		"/api/x?status=failed&id=42&tag=vip&other=ignored": "q",
	} {
		if got := selected(t, rt, request("GET", target, "")); got != want {
			t.Errorf("%s: chosen %q, want %q", target, got, want)
		}
	}
}

func TestBodyOperators(t *testing.T) {
	for _, c := range []struct {
		name string
		m    config.Matcher
		body string
		want bool
	}{
		{"equality matches", eq(`amount=10`), `amount=10`, true},
		{"equality is exact", eq(`amount=10`), `amount=100`, false},
		{"regex matches", re(`"amount":\s*\d{3,}`), `{"amount": 1500}`, true},
		{"regex does not match", re(`"amount":\s*\d{3,}`), `{"amount": 15}`, false},
		{"substring matches", contains(`"currency":"BRL"`), `{"amount":1,"currency":"BRL"}`, true},
		{"substring does not match", contains(`"currency":"BRL"`), `{"currency":"USD"}`, false},
		{"JSON ignores key order", jsonEq(map[string]any{"a": 1, "b": 2}), `{"b":2,"a":1}`, true},
		{"nested JSON", jsonEq(map[string]any{"a": []any{1, map[string]any{"c": true}}}), `{"a":[1,{"c":true}]}`, true},
		{"different JSON does not match", jsonEq(map[string]any{"a": 1, "b": 2}), `{"a":1,"b":3}`, false},
		{"JSON with an extra key does not match", jsonEq(map[string]any{"a": 1}), `{"a":1,"b":2}`, false},
		{"an empty body does not match JSON", jsonEq(map[string]any{"a": 1}), ``, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := compiled(t, ov("o", config.OverrideMatch{Path: "/api/*", Body: &c.m}))
			got := selected(t, rt, request("POST", "/api/x", c.body)) == "o"
			if got != c.want {
				t.Fatalf("matched = %v, want %v", got, c.want)
			}
		})
	}
}

// The scenario from the spec: JSON equality on the body ignores key order.
func TestJSONBodyEqualityIgnoresKeyOrder(t *testing.T) {
	m := jsonEq(map[string]any{"a": 1, "b": 2})
	rt := compiled(t, ov("json", config.OverrideMatch{Path: "/api/*", Body: &m}))
	if got := selected(t, rt, request("POST", "/api/x", `{"b":2,"a":1}`)); got != "json" {
		t.Fatalf("a body with the keys in another order should match, chosen %q", got)
	}
}

func TestAllCriteriaMustMatch(t *testing.T) {
	body := contains("urgent")
	rt := compiled(t, ov("all", config.OverrideMatch{
		Path:    "/api/payments/*",
		Method:  "POST",
		Headers: map[string]config.Matcher{"X-Tenant": eq("acme")},
		Query:   map[string]config.Matcher{"v": eq("2")},
		Body:    &body,
	}))
	full := func() *http.Request {
		return request("POST", "/api/payments/x?v=2", "urgent order", "X-Tenant", "acme")
	}
	if got := selected(t, rt, full()); got != "all" {
		t.Fatalf("with every criterion satisfied the override should match, chosen %q", got)
	}
	for name, r := range map[string]*http.Request{
		"missing header": request("POST", "/api/payments/x?v=2", "urgent order"),
		"wrong method":   request("PUT", "/api/payments/x?v=2", "urgent order", "X-Tenant", "acme"),
		"wrong query":    request("POST", "/api/payments/x?v=3", "urgent order", "X-Tenant", "acme"),
		"wrong body":     request("POST", "/api/payments/x?v=2", "normal order", "X-Tenant", "acme"),
		"path outside":   request("POST", "/api/orders/x?v=2", "urgent order", "X-Tenant", "acme"),
	} {
		if got := selected(t, rt, r); got != "" {
			t.Errorf("%s: a criterion that does not match should prevent the interception, chosen %q", name, got)
		}
	}
}

// The body read for matching comes back intact in the request to forward,
// whether the override matches or not.
func TestBodyReadForMatchingIsReplayed(t *testing.T) {
	m := eq("never")
	rt := compiled(t, ov("o", config.OverrideMatch{Path: "/api/*", Body: &m}))
	const payload = `{"order": 1}`
	r := request("POST", "/api/x", payload)
	q := NewRequest(r)
	if Select(rt, q) != nil {
		t.Fatal("the override should not match")
	}
	got, err := io.ReadAll(q.Request().Body)
	if err != nil || string(got) != payload {
		t.Fatalf("the forwarded body should be the original one: %q %v", got, err)
	}
	if q.Request().ContentLength != int64(len(payload)) {
		t.Fatalf("the Content-Length should be preserved: %d", q.Request().ContentLength)
	}
}

// Without a body criterion, the body is not read: the request goes on
// unchanged.
func TestBodyNotReadWithoutBodyCriterion(t *testing.T) {
	rt := compiled(t, ov("o", config.OverrideMatch{Path: "/api/*", Method: "PUT"}))
	r := request("POST", "/api/x", "body")
	q := NewRequest(r)
	Select(rt, q)
	if q.Request() != r {
		t.Fatal("without a body criterion the request should not be touched")
	}
}

// A body over the read limit does not match a body criterion and goes on in
// full to whoever forwards it.
func TestOversizedBodyDoesNotMatchAndIsReplayed(t *testing.T) {
	m := contains("x")
	rt := compiled(t, ov("o", config.OverrideMatch{Path: "/api/*", Body: &m}))
	payload := bytes.Repeat([]byte("x"), MaxBodyMatchBytes+100)
	r := httptest.NewRequest("POST", "/api/x", bytes.NewReader(payload))
	q := NewRequest(r)
	if Select(rt, q) != nil {
		t.Fatal("a body over the limit should not match a body criterion")
	}
	got, _ := io.ReadAll(q.Request().Body)
	if !bytes.Equal(got, payload) {
		t.Fatalf("the forwarded body should be the whole original one: %d bytes out of %d", len(got), len(payload))
	}
}

// The body is read only once, even with several overrides that examine it.
func TestBodyReadOnceAcrossOverrides(t *testing.T) {
	a, b := eq("a"), eq("b")
	rt := compiled(t,
		ov("a", config.OverrideMatch{Path: "/api/*", Body: &a}),
		ov("b", config.OverrideMatch{Path: "/api/*", Body: &b}),
	)
	r := request("POST", "/api/x", "b")
	q := NewRequest(r)
	if o := Select(rt, q); o == nil || o.Doc.Name != "b" {
		t.Fatalf("the second override should match the body already read: %v", o)
	}
	got, _ := io.ReadAll(q.Request().Body)
	if string(got) != "b" {
		t.Fatalf("the forwarded body should be the original one: %q", got)
	}
}

// Requirement: Precedence by specificity

func TestExactPathBeatsWildcard(t *testing.T) {
	rt := compiled(t,
		ov("wildcard", config.OverrideMatch{Path: "/api/payments/*"}),
		ov("exact", config.OverrideMatch{Path: "/api/payments/bilulu"}),
	)
	if got := selected(t, rt, request("GET", "/api/payments/bilulu", "")); got != "exact" {
		t.Fatalf("the exact path should beat the wildcard, chosen %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/payments/other", "")); got != "wildcard" {
		t.Fatalf("outside the exact path the wildcard holds, chosen %q", got)
	}
}

func TestSegmentParamBetweenExactAndWildcard(t *testing.T) {
	rt := compiled(t,
		ov("wildcard", config.OverrideMatch{Path: "/zip/*"}),
		ov("param", config.OverrideMatch{Path: "/zip/:id/json"}),
		ov("exact", config.OverrideMatch{Path: "/zip/01001000/json"}),
	)
	for p, want := range map[string]string{
		"/zip/01001000/json": "exact",
		"/zip/40415345/json": "param",
		"/zip/40415345/xml":  "wildcard",
	} {
		if got := selected(t, rt, request("GET", p, "")); got != want {
			t.Errorf("%s: chosen %q, want %q", p, got, want)
		}
	}
}

// Among paths with parameters, the one with more literal segments wins; the
// segment parameter comes before the regular expression.
func TestSegmentParamPrecedence(t *testing.T) {
	rt := compiled(t,
		ov("regex", config.OverrideMatch{PathRegex: `^/api/users/[^/]+/orders/[^/]+$`}),
		ov("two", config.OverrideMatch{Path: "/api/:a/:b/orders/:c"}),
		ov("three", config.OverrideMatch{Path: "/api/users/:u/orders/:o"}),
	)
	if got := selected(t, rt, request("GET", "/api/users/42/orders/7", "")); got != "three" {
		t.Fatalf("the path with more literals should win, chosen %q", got)
	}
	rt = compiled(t,
		ov("regex", config.OverrideMatch{PathRegex: `^/api/users/[^/]+$`}),
		ov("param", config.OverrideMatch{Path: "/api/users/:id"}),
	)
	if got := selected(t, rt, request("GET", "/api/users/42", "")); got != "param" {
		t.Fatalf("the segment parameter should beat the regular expression, chosen %q", got)
	}
}

func TestLongerWildcardBeatsShorter(t *testing.T) {
	rt := compiled(t,
		ov("api", config.OverrideMatch{Path: "/api/*"}),
		ov("payments", config.OverrideMatch{Path: "/api/payments/*"}),
	)
	if got := selected(t, rt, request("GET", "/api/payments/charge", "")); got != "payments" {
		t.Fatalf("the longer wildcard should win, chosen %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/orders", "")); got != "api" {
		t.Fatalf("outside the longer wildcard the shorter one holds, chosen %q", got)
	}
}

func TestMoreCriteriaWinsBetweenEqualPaths(t *testing.T) {
	rt := compiled(t,
		ov("any", config.OverrideMatch{Path: "/api/payments/charge"}),
		ov("post", config.OverrideMatch{Path: "/api/payments/charge", Method: "POST"}),
	)
	if got := selected(t, rt, request("POST", "/api/payments/charge", "")); got != "post" {
		t.Fatalf("the override with more criteria should win, chosen %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/payments/charge", "")); got != "any" {
		t.Fatalf("without the required method the less restrictive one holds, chosen %q", got)
	}
}

func TestDeclarationOrderBreaksTies(t *testing.T) {
	rt := compiled(t,
		ov("first", config.OverrideMatch{Path: "/api/payments/*"}),
		ov("second", config.OverrideMatch{Path: "/api/payments/*"}),
	)
	if got := selected(t, rt, request("GET", "/api/payments/x", "")); got != "first" {
		t.Fatalf("on a tie the declaration order should hold, chosen %q", got)
	}
}

func TestDisabledOverrideIsSkipped(t *testing.T) {
	off := false
	exact := ov("exact", config.OverrideMatch{Path: "/api/payments/bilulu"})
	exact.On = &off
	rt := compiled(t, ov("wildcard", config.OverrideMatch{Path: "/api/payments/*"}), exact)
	if got := selected(t, rt, request("GET", "/api/payments/bilulu", "")); got != "wildcard" {
		t.Fatalf("the disabled override should not take part in the precedence, chosen %q", got)
	}
}
