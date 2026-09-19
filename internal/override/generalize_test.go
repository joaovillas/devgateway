package override

import (
	"net/http"
	"slices"
	"testing"

	"github.com/joaovillas/devgateway/internal/config"
)

func TestGeneralize(t *testing.T) {
	for path, want := range map[string]string{
		"/zip/40415345/json": "/zip/:id/json",
		"/api/users/me":      "/api/users/me",
		"/api/users/42":      "/api/users/:id",
		"/api/charge":        "/api/charge",
		"/api/v1/json":       "/api/v1/json",
		"/users/42/orders/7": "/users/:id/orders/:id2",
		"/obj/3f2a9c1e":      "/obj/:id",
		"/obj/a1b2c3d":       "/obj/a1b2c3d",
		"/obj/deadbeefcafe":  "/obj/deadbeefcafe",
		"/obj/Abc12345XyZ":   "/obj/:id",
		"/obj/123e4567-e89b-12d3-a456-426614174000": "/obj/:id",
		"/obj/123e4567-e89b-12d3-a456-42661417400g": "/obj/123e4567-e89b-12d3-a456-42661417400g",
		"/payments/charges/ch_123":                  "/payments/charges/ch_123",
		"/posts/my-title-2024":                      "/posts/my-title-2024",
		"/":                                         "/",
		"/files/*/42":                               "/files/*/42",
		"/v1/:batch/42":                             "/v1/:batch/42",
		"/v1/projects:batch/42":                     "/v1/projects:batch/:id",
		"/api//42":                                  "/api//:id",
		"/2026/09/18":                               "/:id/:id2/:id3",
		"/a/0000000000000000000000000000000000000000000000000": "/a/:id",
	} {
		if got := Generalize(path); got != want {
			t.Errorf("Generalize(%q) = %q, want %q", path, got, want)
		}
	}
}

// The generalized path is always accepted as an override path and matches the
// path it came from.
func TestGeneralizedPathIsValidAndMatches(t *testing.T) {
	for _, path := range []string{"/zip/40415345/json", "/users/42/orders/7", "/v1/projects:batch/42", "/api//42"} {
		m := LearnMatch(http.MethodGet, path)
		o := config.Override{Name: "o", Match: m, Respond: respond("x")}
		if err := config.ValidateOverride("t.yaml", o); err != nil {
			t.Fatalf("%s: the learned criterion should be valid: %v", path, err)
		}
		if got := selected(t, compiled(t, o), request("GET", path, "")); got != "o" {
			t.Fatalf("%s: the learned criterion %+v should match the path it came from", path, m)
		}
	}
	// A segment starting with : cannot be written as a literal.
	if m := LearnMatch(http.MethodGet, "/v1/:batch"); m.Path != "" || m.PathRegex != `^/v1/:batch$` {
		t.Fatalf("a segment starting with : should become an exact regular expression: %+v", m)
	}
}

func TestKnownByGeneralizedPathOrMatch(t *testing.T) {
	r := config.Route{Overrides: []config.Override{
		{Name: "a", Match: config.OverrideMatch{Path: "/zip/:id/json", Method: http.MethodGet}},
		{Name: "b", Match: config.OverrideMatch{Path: "/users/:user", Method: http.MethodGet}},
		{Name: "c", Match: config.OverrideMatch{Path: "/orders/:id/items", Method: http.MethodGet}},
		{Name: "d", Match: config.OverrideMatch{Path: "/api/*", Method: http.MethodGet}},
		{Name: "e", Match: config.OverrideMatch{PathRegex: `^/re/\d+$`, Method: http.MethodGet}},
	}}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/zip/01001000/json", true},
		{http.MethodPost, "/zip/01001000/json", false},
		{http.MethodGet, "/zip/abc/json", true}, // matches the criterion, even though it is not an identifier
		{http.MethodGet, "/users/me", true},
		{http.MethodGet, "/users/42", true},
		{http.MethodGet, "/orders/42/items", true},
		{http.MethodGet, "/orders/42/items/1", false},
		{http.MethodGet, "/api/x", false}, // a wildcard does not count
		{http.MethodGet, "/re/1", false},  // a free regular expression does not count
	} {
		if got := Known(r, c.method, c.path); got != c.want {
			t.Errorf("%s %s: known %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

func learnedExact(name, method, path string) config.Override {
	return config.Override{
		Name: name, On: new(bool), Match: config.OverrideMatch{Path: path, Method: method},
		Respond: respond(name), Source: &config.OverrideSource{Kind: config.SourceLearned},
	}
}

func TestAbsorbReplacesCoveredLearnedExact(t *testing.T) {
	on := learnedExact("enabled", http.MethodGet, "/zip/3/json")
	on.On = nil
	derived := learnedExact("derived", http.MethodGet, "/zip/4/json")
	derived.Source.Kind = config.SourceDerived
	header := learnedExact("header", http.MethodGet, "/zip/5/json")
	header.Match.Headers = map[string]config.Matcher{"X": eq("1")}
	list := []config.Override{
		learnedExact("other", http.MethodGet, "/other"),
		learnedExact("one", http.MethodGet, "/zip/1/json"),
		learnedExact("literal", http.MethodGet, "/zip/abc/json"),
		learnedExact("post", http.MethodPost, "/zip/2/json"),
		learnedExact("two", http.MethodGet, "/zip/2/json"),
		on, derived, header,
		learnedExact("xml", http.MethodGet, "/zip/6/xml"),
	}
	before := slices.Clone(list)
	g := config.Override{Match: config.OverrideMatch{Path: "/zip/:id/json", Method: http.MethodGet}}
	out, at := Absorb(list, g)
	var names []string
	for _, o := range out {
		names = append(names, o.Name)
	}
	want := []string{"other", "", "literal", "post", "enabled", "derived", "header", "xml"}
	if !slices.Equal(names, want) || at != 1 {
		t.Fatalf("unexpected absorption: %q at position %d, want %q at 1", names, at, want)
	}
	if !slices.EqualFunc(list, before, func(a, b config.Override) bool { return a.Name == b.Name }) {
		t.Fatal("the given list should not be changed")
	}
	// With nothing covered, the override goes to the end.
	if out, at := Absorb(list[:1], g); len(out) != 2 || at != 1 {
		t.Fatalf("with no coverage the override should go to the end: %d %d", len(out), at)
	}
	// An exact criterion absorbs nothing.
	if out, _ := Absorb(list, config.Override{Match: config.OverrideMatch{Path: "/zip/1/json", Method: http.MethodGet}}); len(out) != len(list)+1 {
		t.Fatalf("an exact path should not absorb: %d", len(out))
	}
}
