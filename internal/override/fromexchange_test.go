package override

import (
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/exchange"
)

func upstreamExchange(ct string, body string) exchange.Exchange {
	h := http.Header{}
	if ct != "" {
		h.Set("Content-Type", ct)
	}
	return exchange.Exchange{
		ID: "01K5E3V3C8Q2M4Z8N6P0R2T4W6", Method: http.MethodGet, Path: "/api/test",
		Route: "payments", Outcome: exchange.OutcomeUpstream, Status: http.StatusCreated,
		Response: exchange.Message{Headers: h, Body: []byte(body), Size: int64(len(body))},
	}
}

func TestFromExchangeFillsCriteriaResponseAndSource(t *testing.T) {
	e := upstreamExchange("text/plain", "hello")
	e.Response.Headers.Set("Date", "Fri, 18 Sep 2026 15:00:00 GMT")
	e.Response.Headers.Set("Content-Length", "5")
	e.Response.Headers.Set("Connection", "X-Hop")
	e.Response.Headers.Set("X-Hop", "this connection only")
	e.Response.Headers.Set("Keep-Alive", "timeout=5")
	e.Response.Headers.Set("Transfer-Encoding", "chunked")
	e.Response.Headers.Set("X-Gateway", "route=payments")
	e.Response.Headers.Set("X-Request-Id", "abc")
	e.Response.Headers.Add("Vary", "Accept")
	e.Response.Headers.Add("Vary", "Origin")
	at := time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC)
	o := FromExchange(e, config.SourceLearned, at)

	if o.Match.Path != "/api/test" || o.Match.Method != http.MethodGet || o.Match.PathRegex != "" {
		t.Fatalf("the criterion should be the exact path and the method: %+v", o.Match)
	}
	want := map[string]config.HeaderValues{"Content-Type": {"text/plain"}, "X-Request-Id": {"abc"}, "Vary": {"Accept", "Origin"}}
	if !reflect.DeepEqual(o.Respond.Headers, want) {
		t.Fatalf("unexpected pre-filled headers:\n%v\nwant\n%v", o.Respond.Headers, want)
	}
	if o.Respond.Status != http.StatusCreated || o.Respond.Body != "hello" {
		t.Fatalf("status and body should be the observed ones: %d %v", o.Respond.Status, o.Respond.Body)
	}
	s := o.Source
	if s == nil || s.Kind != config.SourceLearned || s.Exchange != e.ID || !s.At.Equal(at) || s.BodyIncomplete {
		t.Fatalf("unexpected source: %+v", s)
	}
}

func TestFromExchangeJSONBodyIsStructured(t *testing.T) {
	o := FromExchange(upstreamExchange("application/json; charset=utf-8", `{"id":9007199254740993,"v":1.5,"l":[1,"a"]}`), config.SourceLearned, time.Now())
	want := map[string]any{"id": int64(9007199254740993), "v": 1.5, "l": []any{int64(1), "a"}}
	if !reflect.DeepEqual(o.Respond.Body, want) {
		t.Fatalf("the JSON body should become a structure, with integers preserved: %#v", o.Respond.Body)
	}
	// A +json type is JSON as well.
	if o := FromExchange(upstreamExchange("application/problem+json", `{"a":1}`), config.SourceLearned, time.Now()); reflect.TypeOf(o.Respond.Body).Kind() != reflect.Map {
		t.Fatalf("application/problem+json should become a structure: %#v", o.Respond.Body)
	}
	// Invalid JSON with a JSON type stays as text.
	if o := FromExchange(upstreamExchange("application/json", `{"a":`), config.SourceLearned, time.Now()); o.Respond.Body != `{"a":` {
		t.Fatalf("an invalid body should stay as text: %#v", o.Respond.Body)
	}
	// Without a JSON type, even a JSON body stays as text.
	if o := FromExchange(upstreamExchange("text/plain", `{"a":1}`), config.SourceLearned, time.Now()); o.Respond.Body != `{"a":1}` {
		t.Fatalf("a text body should stay as text: %#v", o.Respond.Body)
	}
	if o := FromExchange(upstreamExchange("", ""), config.SourceLearned, time.Now()); o.Respond.Body != nil {
		t.Fatalf("an empty body should not be declared: %#v", o.Respond.Body)
	}
}

func TestFromExchangeTruncatedBodyFlagged(t *testing.T) {
	e := upstreamExchange("application/json", `{"a":`)
	e.Response.Truncated, e.Response.Size = true, 1<<20
	o := FromExchange(e, config.SourceLearned, time.Now())
	if !o.Source.BodyIncomplete || o.Respond.Body != `{"a":` {
		t.Fatalf("the truncated body should be flagged and kept as text: %+v %#v", o.Source, o.Respond.Body)
	}
}

// The override that is built is valid as a route document.
func TestFromExchangeProducesValidOverride(t *testing.T) {
	o := FromExchange(upstreamExchange("application/json", `{"a":1}`), config.SourceLearned, time.Now())
	o.Name = "get-api-test"
	o.On = new(bool)
	rt := compiled(t, o)
	if string(rt.Overrides[0].RespondBody) != `{"a":1}` {
		t.Fatalf("the pre-filled response should reproduce the body: %s", rt.Overrides[0].RespondBody)
	}
}

func TestKnownRequiresExactPathAndMethod(t *testing.T) {
	r := config.Route{Overrides: []config.Override{
		{Name: "a", Match: config.OverrideMatch{Path: "/api/test", Method: http.MethodGet}},
		{Name: "b", Match: config.OverrideMatch{Path: "/api/*", Method: http.MethodGet}},
		{Name: "c", Match: config.OverrideMatch{Path: "/api/no-method"}},
		{Name: "d", Match: config.OverrideMatch{PathRegex: "^/api/re$", Method: http.MethodGet}},
	}}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/test", true},
		{http.MethodPost, "/api/test", false},
		{http.MethodGet, "/api/other", false},
		{http.MethodGet, "/api/no-method", false},
		{http.MethodGet, "/api/re", false},
	} {
		if got := Known(r, c.method, c.path); got != c.want {
			t.Errorf("%s %s: known %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func TestNameDerivedAndUnique(t *testing.T) {
	r := config.Route{}
	if n := Name(r, http.MethodGet, "/api/test"); n != "get-api-test" {
		t.Fatalf("unexpected name: %s", n)
	}
	if n := Name(r, http.MethodPost, "/API/v1/Users.json"); n != "post-api-v1-users-json" {
		t.Fatalf("unexpected name: %s", n)
	}
	if n := Name(r, http.MethodGet, "/"); n != "get-root" {
		t.Fatalf("unexpected name for the root path: %s", n)
	}
	r.Overrides = []config.Override{{Name: "get-api-test"}, {Name: "get-api-test-2"}}
	if n := Name(r, http.MethodGet, "/api/test"); n != "get-api-test-3" {
		t.Fatalf("the name should be unique within the route: %s", n)
	}
}

// Repeated headers, such as several Set-Cookie, are pre-filled with every
// observed value, in order, and the synthesized response repeats them.
func TestFromExchangeKeepsRepeatedHeaders(t *testing.T) {
	e := upstreamExchange("text/plain", "ok")
	e.Response.Headers.Add("Set-Cookie", "a=1; Path=/")
	e.Response.Headers.Add("Set-Cookie", "b=2; Expires=Wed, 21 Oct 2026 07:28:00 GMT")
	o := FromExchange(e, config.SourceLearned, time.Now())
	want := config.HeaderValues{"a=1; Path=/", "b=2; Expires=Wed, 21 Oct 2026 07:28:00 GMT"}
	if got := o.Respond.Headers["Set-Cookie"]; !slices.Equal(got, want) {
		t.Fatalf("both Set-Cookie values should be kept: %q", got)
	}
	o.Name = "cookies"
	rt := compiled(t, o)
	h := http.Header{}
	SetHeaders(h, rt.Overrides[0])
	if got := h.Values("Set-Cookie"); !slices.Equal(got, want) {
		t.Fatalf("the synthesized response should repeat the observed Set-Cookie values: %q", got)
	}
}

// Numbers that a float64 cannot hold without loss keep the body as text,
// identical to the observed one; integers up to uint64 and representable
// decimals become a structure.
func TestFromExchangeLargeNumbersNotRounded(t *testing.T) {
	for _, body := range []string{
		`{"id":12345678901234567890123}`,
		`{"v":0.12345678901234567890123}`,
		`[1e400]`,
	} {
		o := FromExchange(upstreamExchange("application/json", body), config.SourceLearned, time.Now())
		if o.Respond.Body != body {
			t.Errorf("%s: the body should stay as text, without rounding: %#v", body, o.Respond.Body)
		}
		o.Name = "large"
		if rt := compiled(t, o); string(rt.Overrides[0].RespondBody) != body {
			t.Errorf("%s: the pre-filled response should reproduce the body: %s", body, rt.Overrides[0].RespondBody)
		}
	}
	o := FromExchange(upstreamExchange("application/json", `{"id":18446744073709551615,"v":0.1,"e":1e3}`), config.SourceLearned, time.Now())
	want := map[string]any{"id": uint64(18446744073709551615), "v": 0.1, "e": 1000.0}
	if !reflect.DeepEqual(o.Respond.Body, want) {
		t.Fatalf("representable numbers should become a structure: %#v", o.Respond.Body)
	}
	o.Name = "uint"
	if got := string(compiled(t, o).Overrides[0].RespondBody); got != `{"e":1000,"id":18446744073709551615,"v":0.1}` {
		t.Fatalf("the uint64 should be reproduced exactly: %s", got)
	}
}

// A literal * in the path cannot become a wildcard: the learned criterion is
// an anchored regular expression that matches only that path, and Known
// recognizes it.
func TestFromExchangeLiteralStarPath(t *testing.T) {
	for _, path := range []string{"/api/files/*", "/api/a*b"} {
		e := upstreamExchange("text/plain", "ok")
		e.Path = path
		o := FromExchange(e, config.SourceLearned, time.Now())
		if o.Match.Path != "" || o.Match.PathRegex != "^"+regexp.QuoteMeta(path)+"$" || o.Match.Method != http.MethodGet {
			t.Fatalf("%s: the criterion should be an exact regular expression: %+v", path, o.Match)
		}
		o.Name = "star"
		o.On = new(bool)
		ov := compiled(t, o).Overrides[0]
		o.On = nil
		sel := compiled(t, o)
		for _, c := range []struct {
			path string
			want bool
		}{{path, true}, {"/api/files/other", false}, {"/api/ab", false}} {
			req, _ := http.NewRequest(http.MethodGet, "http://gw"+c.path, nil)
			got := Select(sel, NewRequest(req)) != nil
			if got != c.want {
				t.Errorf("%s: selection of %s = %v, want %v", path, c.path, got, c.want)
			}
		}
		if ov.PathRegex == nil {
			t.Fatalf("%s: the override should compile with a regular expression", path)
		}
		r := config.Route{Overrides: []config.Override{o}}
		if !Known(r, http.MethodGet, path) {
			t.Fatalf("%s: the learned override should make the endpoint known", path)
		}
		// A declared wildcard does not make the path with a literal * known.
		if path == "/api/files/*" && Known(config.Route{Overrides: []config.Override{
			{Name: "c", Match: config.OverrideMatch{Path: "/api/files/*", Method: http.MethodGet}},
		}}, http.MethodGet, path) {
			t.Fatal("the wildcard should not count as the literal path")
		}
	}
}
