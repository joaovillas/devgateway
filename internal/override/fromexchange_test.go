package override

import (
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
)

func upstreamExchange(ct string, body string) exchange.Exchange {
	h := http.Header{}
	if ct != "" {
		h.Set("Content-Type", ct)
	}
	return exchange.Exchange{
		ID: "01K5E3V3C8Q2M4Z8N6P0R2T4W6", Method: http.MethodGet, Path: "/api/teste",
		Route: "payments", Outcome: exchange.OutcomeUpstream, Status: http.StatusCreated,
		Response: exchange.Message{Headers: h, Body: []byte(body), Size: int64(len(body))},
	}
}

func TestFromExchangeFillsCriteriaResponseAndSource(t *testing.T) {
	e := upstreamExchange("text/plain", "olá")
	e.Response.Headers.Set("Date", "Fri, 18 Sep 2026 15:00:00 GMT")
	e.Response.Headers.Set("Content-Length", "4")
	e.Response.Headers.Set("Connection", "X-Hop")
	e.Response.Headers.Set("X-Hop", "só nesta conexão")
	e.Response.Headers.Set("Keep-Alive", "timeout=5")
	e.Response.Headers.Set("Transfer-Encoding", "chunked")
	e.Response.Headers.Set("X-Gateway", "route=payments")
	e.Response.Headers.Set("X-Request-Id", "abc")
	e.Response.Headers.Add("Vary", "Accept")
	e.Response.Headers.Add("Vary", "Origin")
	at := time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC)
	o := FromExchange(e, config.SourceLearned, at)

	if o.Match.Path != "/api/teste" || o.Match.Method != http.MethodGet || o.Match.PathRegex != "" {
		t.Fatalf("critério deveria ser path exato e método: %+v", o.Match)
	}
	want := map[string]config.HeaderValues{"Content-Type": {"text/plain"}, "X-Request-Id": {"abc"}, "Vary": {"Accept", "Origin"}}
	if !reflect.DeepEqual(o.Respond.Headers, want) {
		t.Fatalf("cabeçalhos pré-preenchidos inesperados:\n%v\nesperado\n%v", o.Respond.Headers, want)
	}
	if o.Respond.Status != http.StatusCreated || o.Respond.Body != "olá" {
		t.Fatalf("status e corpo deveriam ser os observados: %d %v", o.Respond.Status, o.Respond.Body)
	}
	s := o.Source
	if s == nil || s.Kind != config.SourceLearned || s.Exchange != e.ID || !s.At.Equal(at) || s.BodyIncomplete {
		t.Fatalf("origem inesperada: %+v", s)
	}
}

func TestFromExchangeJSONBodyIsStructured(t *testing.T) {
	o := FromExchange(upstreamExchange("application/json; charset=utf-8", `{"id":9007199254740993,"v":1.5,"l":[1,"a"]}`), config.SourceLearned, time.Now())
	want := map[string]any{"id": int64(9007199254740993), "v": 1.5, "l": []any{int64(1), "a"}}
	if !reflect.DeepEqual(o.Respond.Body, want) {
		t.Fatalf("o corpo JSON deveria virar estrutura, com inteiros preservados: %#v", o.Respond.Body)
	}
	// O tipo +json também é JSON.
	if o := FromExchange(upstreamExchange("application/problem+json", `{"a":1}`), config.SourceLearned, time.Now()); reflect.TypeOf(o.Respond.Body).Kind() != reflect.Map {
		t.Fatalf("application/problem+json deveria virar estrutura: %#v", o.Respond.Body)
	}
	// JSON inválido com tipo JSON fica como texto.
	if o := FromExchange(upstreamExchange("application/json", `{"a":`), config.SourceLearned, time.Now()); o.Respond.Body != `{"a":` {
		t.Fatalf("corpo inválido deveria ficar como texto: %#v", o.Respond.Body)
	}
	// Sem tipo JSON, mesmo um corpo JSON fica como texto.
	if o := FromExchange(upstreamExchange("text/plain", `{"a":1}`), config.SourceLearned, time.Now()); o.Respond.Body != `{"a":1}` {
		t.Fatalf("corpo de texto deveria ficar como texto: %#v", o.Respond.Body)
	}
	if o := FromExchange(upstreamExchange("", ""), config.SourceLearned, time.Now()); o.Respond.Body != nil {
		t.Fatalf("corpo vazio não deveria ser declarado: %#v", o.Respond.Body)
	}
}

func TestFromExchangeTruncatedBodyFlagged(t *testing.T) {
	e := upstreamExchange("application/json", `{"a":`)
	e.Response.Truncated, e.Response.Size = true, 1<<20
	o := FromExchange(e, config.SourceLearned, time.Now())
	if !o.Source.BodyIncomplete || o.Respond.Body != `{"a":` {
		t.Fatalf("o corpo truncado deveria ser sinalizado e mantido como texto: %+v %#v", o.Source, o.Respond.Body)
	}
}

// O override montado é válido como documento de rota.
func TestFromExchangeProducesValidOverride(t *testing.T) {
	o := FromExchange(upstreamExchange("application/json", `{"a":1}`), config.SourceLearned, time.Now())
	o.Name = "get-api-teste"
	o.On = new(bool)
	rt := compiled(t, o)
	if string(rt.Overrides[0].RespondBody) != `{"a":1}` {
		t.Fatalf("a resposta pré-preenchida deveria reproduzir o corpo: %s", rt.Overrides[0].RespondBody)
	}
}

func TestKnownRequiresExactPathAndMethod(t *testing.T) {
	r := config.Route{Overrides: []config.Override{
		{Name: "a", Match: config.OverrideMatch{Path: "/api/teste", Method: http.MethodGet}},
		{Name: "b", Match: config.OverrideMatch{Path: "/api/*", Method: http.MethodGet}},
		{Name: "c", Match: config.OverrideMatch{Path: "/api/sem-metodo"}},
		{Name: "d", Match: config.OverrideMatch{PathRegex: "^/api/re$", Method: http.MethodGet}},
	}}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/api/teste", true},
		{http.MethodPost, "/api/teste", false},
		{http.MethodGet, "/api/outro", false},
		{http.MethodGet, "/api/sem-metodo", false},
		{http.MethodGet, "/api/re", false},
	} {
		if got := Known(r, c.method, c.path); got != c.want {
			t.Errorf("%s %s: conhecido %v, esperado %v", c.method, c.path, got, c.want)
		}
	}
}

func TestNameDerivedAndUnique(t *testing.T) {
	r := config.Route{}
	if n := Name(r, http.MethodGet, "/api/teste"); n != "get-api-teste" {
		t.Fatalf("nome inesperado: %s", n)
	}
	if n := Name(r, http.MethodPost, "/API/v1/Users.json"); n != "post-api-v1-users-json" {
		t.Fatalf("nome inesperado: %s", n)
	}
	if n := Name(r, http.MethodGet, "/"); n != "get-root" {
		t.Fatalf("nome inesperado para a raiz: %s", n)
	}
	r.Overrides = []config.Override{{Name: "get-api-teste"}, {Name: "get-api-teste-2"}}
	if n := Name(r, http.MethodGet, "/api/teste"); n != "get-api-teste-3" {
		t.Fatalf("o nome deveria ser único na rota: %s", n)
	}
}

// Cabeçalhos repetidos, como vários Set-Cookie, são pré-preenchidos com todos
// os valores observados, na ordem, e a resposta sintetizada os repete.
func TestFromExchangeKeepsRepeatedHeaders(t *testing.T) {
	e := upstreamExchange("text/plain", "ok")
	e.Response.Headers.Add("Set-Cookie", "a=1; Path=/")
	e.Response.Headers.Add("Set-Cookie", "b=2; Expires=Wed, 21 Oct 2026 07:28:00 GMT")
	o := FromExchange(e, config.SourceLearned, time.Now())
	want := config.HeaderValues{"a=1; Path=/", "b=2; Expires=Wed, 21 Oct 2026 07:28:00 GMT"}
	if got := o.Respond.Headers["Set-Cookie"]; !slices.Equal(got, want) {
		t.Fatalf("os dois Set-Cookie deveriam ser mantidos: %q", got)
	}
	o.Name = "cookies"
	rt := compiled(t, o)
	h := http.Header{}
	SetHeaders(h, rt.Overrides[0])
	if got := h.Values("Set-Cookie"); !slices.Equal(got, want) {
		t.Fatalf("a resposta sintetizada deveria repetir os Set-Cookie observados: %q", got)
	}
}

// Números que o float64 não guarda sem perda mantêm o corpo como texto,
// idêntico ao observado; inteiros até uint64 e decimais representáveis viram
// estrutura.
func TestFromExchangeLargeNumbersNotRounded(t *testing.T) {
	for _, body := range []string{
		`{"id":12345678901234567890123}`,
		`{"v":0.12345678901234567890123}`,
		`[1e400]`,
	} {
		o := FromExchange(upstreamExchange("application/json", body), config.SourceLearned, time.Now())
		if o.Respond.Body != body {
			t.Errorf("%s: o corpo deveria ficar como texto, sem arredondamento: %#v", body, o.Respond.Body)
		}
		o.Name = "grande"
		if rt := compiled(t, o); string(rt.Overrides[0].RespondBody) != body {
			t.Errorf("%s: a resposta pré-preenchida deveria reproduzir o corpo: %s", body, rt.Overrides[0].RespondBody)
		}
	}
	o := FromExchange(upstreamExchange("application/json", `{"id":18446744073709551615,"v":0.1,"e":1e3}`), config.SourceLearned, time.Now())
	want := map[string]any{"id": uint64(18446744073709551615), "v": 0.1, "e": 1000.0}
	if !reflect.DeepEqual(o.Respond.Body, want) {
		t.Fatalf("números representáveis deveriam virar estrutura: %#v", o.Respond.Body)
	}
	o.Name = "uint"
	if got := string(compiled(t, o).Overrides[0].RespondBody); got != `{"e":1000,"id":18446744073709551615,"v":0.1}` {
		t.Fatalf("o uint64 deveria ser reproduzido exato: %s", got)
	}
}

// Um * literal no path não pode virar curinga: o critério aprendido é uma
// expressão regular ancorada que casa só com aquele path, e Known a reconhece.
func TestFromExchangeLiteralStarPath(t *testing.T) {
	for _, path := range []string{"/api/files/*", "/api/a*b"} {
		e := upstreamExchange("text/plain", "ok")
		e.Path = path
		o := FromExchange(e, config.SourceLearned, time.Now())
		if o.Match.Path != "" || o.Match.PathRegex != "^"+regexp.QuoteMeta(path)+"$" || o.Match.Method != http.MethodGet {
			t.Fatalf("%s: o critério deveria ser uma expressão regular exata: %+v", path, o.Match)
		}
		o.Name = "estrela"
		o.On = new(bool)
		ov := compiled(t, o).Overrides[0]
		o.On = nil
		sel := compiled(t, o)
		for _, c := range []struct {
			path string
			want bool
		}{{path, true}, {"/api/files/outro", false}, {"/api/ab", false}} {
			req, _ := http.NewRequest(http.MethodGet, "http://gw"+c.path, nil)
			got := Select(sel, NewRequest(req)) != nil
			if got != c.want {
				t.Errorf("%s: seleção de %s = %v, esperado %v", path, c.path, got, c.want)
			}
		}
		if ov.PathRegex == nil {
			t.Fatalf("%s: o override deveria compilar com expressão regular", path)
		}
		r := config.Route{Overrides: []config.Override{o}}
		if !Known(r, http.MethodGet, path) {
			t.Fatalf("%s: o override aprendido deveria tornar o endpoint conhecido", path)
		}
		// Um curinga declarado não torna conhecido o path com * literal.
		if path == "/api/files/*" && Known(config.Route{Overrides: []config.Override{
			{Name: "c", Match: config.OverrideMatch{Path: "/api/files/*", Method: http.MethodGet}},
		}}, http.MethodGet, path) {
			t.Fatal("o curinga não deveria contar como o path literal")
		}
	}
}
