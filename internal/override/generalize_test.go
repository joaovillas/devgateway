package override

import (
	"net/http"
	"slices"
	"testing"

	"github.com/gamerjp64/gateway/internal/config"
)

func TestGeneralize(t *testing.T) {
	for path, want := range map[string]string{
		"/viacep/40415345/json": "/viacep/:id/json",
		"/api/users/me":         "/api/users/me",
		"/api/users/42":         "/api/users/:id",
		"/api/charge":           "/api/charge",
		"/api/v1/json":          "/api/v1/json",
		"/users/42/orders/7":    "/users/:id/orders/:id2",
		"/obj/3f2a9c1e":         "/obj/:id",
		"/obj/a1b2c3d":          "/obj/a1b2c3d",
		"/obj/deadbeefcafe":     "/obj/deadbeefcafe",
		"/obj/Abc12345XyZ":      "/obj/:id",
		"/obj/123e4567-e89b-12d3-a456-426614174000": "/obj/:id",
		"/obj/123e4567-e89b-12d3-a456-42661417400g": "/obj/123e4567-e89b-12d3-a456-42661417400g",
		"/payments/charges/ch_123":                  "/payments/charges/ch_123",
		"/posts/meu-titulo-2024":                    "/posts/meu-titulo-2024",
		"/":                                         "/",
		"/files/*/42":                               "/files/*/42",
		"/v1/:batch/42":                             "/v1/:batch/42",
		"/v1/projects:batch/42":                     "/v1/projects:batch/:id",
		"/api//42":                                  "/api//:id",
		"/2026/09/18":                               "/:id/:id2/:id3",
		"/a/0000000000000000000000000000000000000000000000000": "/a/:id",
	} {
		if got := Generalize(path); got != want {
			t.Errorf("Generalize(%q) = %q, esperado %q", path, got, want)
		}
	}
}

// O path generalizado é sempre aceito como path de override e casa com o
// path de origem.
func TestGeneralizedPathIsValidAndMatches(t *testing.T) {
	for _, path := range []string{"/viacep/40415345/json", "/users/42/orders/7", "/v1/projects:batch/42", "/api//42"} {
		m := LearnMatch(http.MethodGet, path)
		o := config.Override{Name: "o", Match: m, Respond: respond("x")}
		if err := config.ValidateOverride("t.yaml", o); err != nil {
			t.Fatalf("%s: o critério aprendido deveria ser válido: %v", path, err)
		}
		if got := selected(t, compiled(t, o), request("GET", path, "")); got != "o" {
			t.Fatalf("%s: o critério aprendido %+v deveria casar com o path de origem", path, m)
		}
	}
	// Um segmento começado por : não pode ser escrito como literal.
	if m := LearnMatch(http.MethodGet, "/v1/:batch"); m.Path != "" || m.PathRegex != `^/v1/:batch$` {
		t.Fatalf("um segmento começado por : deveria virar expressão regular exata: %+v", m)
	}
}

func TestKnownByGeneralizedPathOrMatch(t *testing.T) {
	r := config.Route{Overrides: []config.Override{
		{Name: "a", Match: config.OverrideMatch{Path: "/viacep/:id/json", Method: http.MethodGet}},
		{Name: "b", Match: config.OverrideMatch{Path: "/users/:user", Method: http.MethodGet}},
		{Name: "c", Match: config.OverrideMatch{Path: "/orders/:id/items", Method: http.MethodGet}},
		{Name: "d", Match: config.OverrideMatch{Path: "/api/*", Method: http.MethodGet}},
		{Name: "e", Match: config.OverrideMatch{PathRegex: `^/re/\d+$`, Method: http.MethodGet}},
	}}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/viacep/01001000/json", true},
		{http.MethodPost, "/viacep/01001000/json", false},
		{http.MethodGet, "/viacep/abc/json", true}, // casa com o critério, mesmo sem ser identificador
		{http.MethodGet, "/users/me", true},
		{http.MethodGet, "/users/42", true},
		{http.MethodGet, "/orders/42/items", true},
		{http.MethodGet, "/orders/42/items/1", false},
		{http.MethodGet, "/api/x", false}, // curinga não conta
		{http.MethodGet, "/re/1", false},  // expressão regular livre não conta
	} {
		if got := Known(r, c.method, c.path); got != c.want {
			t.Errorf("%s %s: conhecido %v, esperado %v", c.method, c.path, got, c.want)
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
	on := learnedExact("ligado", http.MethodGet, "/viacep/3/json")
	on.On = nil
	derived := learnedExact("derivado", http.MethodGet, "/viacep/4/json")
	derived.Source.Kind = config.SourceDerived
	header := learnedExact("cabecalho", http.MethodGet, "/viacep/5/json")
	header.Match.Headers = map[string]config.Matcher{"X": eq("1")}
	list := []config.Override{
		learnedExact("outro", http.MethodGet, "/outro"),
		learnedExact("um", http.MethodGet, "/viacep/1/json"),
		learnedExact("literal", http.MethodGet, "/viacep/abc/json"),
		learnedExact("post", http.MethodPost, "/viacep/2/json"),
		learnedExact("dois", http.MethodGet, "/viacep/2/json"),
		on, derived, header,
		learnedExact("xml", http.MethodGet, "/viacep/6/xml"),
	}
	before := slices.Clone(list)
	g := config.Override{Match: config.OverrideMatch{Path: "/viacep/:id/json", Method: http.MethodGet}}
	out, at := Absorb(list, g)
	var names []string
	for _, o := range out {
		names = append(names, o.Name)
	}
	want := []string{"outro", "", "literal", "post", "ligado", "derivado", "cabecalho", "xml"}
	if !slices.Equal(names, want) || at != 1 {
		t.Fatalf("absorção inesperada: %q na posição %d, esperado %q na 1", names, at, want)
	}
	if !slices.EqualFunc(list, before, func(a, b config.Override) bool { return a.Name == b.Name }) {
		t.Fatal("a lista recebida não deveria ser alterada")
	}
	// Sem nada coberto, o override vai para o fim.
	if out, at := Absorb(list[:1], g); len(out) != 2 || at != 1 {
		t.Fatalf("sem cobertura o override deveria ir para o fim: %d %d", len(out), at)
	}
	// Um critério exato não absorve nada.
	if out, _ := Absorb(list, config.Override{Match: config.OverrideMatch{Path: "/viacep/1/json", Method: http.MethodGet}}); len(out) != len(list)+1 {
		t.Fatalf("um path exato não deveria absorver: %d", len(out))
	}
}
