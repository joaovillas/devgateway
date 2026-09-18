package override

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gamerjp64/gateway/internal/config"
)

func str(s string) *string { return &s }

func eq(s string) config.Matcher       { return config.Matcher{Equals: str(s)} }
func re(s string) config.Matcher       { return config.Matcher{Regex: str(s)} }
func contains(s string) config.Matcher { return config.Matcher{Contains: str(s)} }
func jsonEq(v any) config.Matcher      { return config.Matcher{JSON: v} }

func respond(body any) *config.Respond { return &config.Respond{Body: body} }

// compiled valida e compila uma rota com os overrides dados, na ordem de
// declaração.
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

// selected devolve o nome do override escolhido para a requisição, ou "".
func selected(t *testing.T, rt *config.CompiledRoute, r *http.Request) string {
	t.Helper()
	if o := Select(rt, NewRequest(r)); o != nil {
		return o.Doc.Name
	}
	return ""
}

// Requirement: Critérios de seleção do override

func TestExactPath(t *testing.T) {
	rt := compiled(t, ov("bilulu", config.OverrideMatch{Path: "/api/payments/bilulu"}))
	if got := selected(t, rt, request("GET", "/api/payments/bilulu", "")); got != "bilulu" {
		t.Fatalf("o path exato deveria casar, escolhido %q", got)
	}
	for _, p := range []string{"/api/payments/bilulu/x", "/api/payments/bilul", "/api/payments"} {
		if got := selected(t, rt, request("GET", p, "")); got != "" {
			t.Errorf("o path exato não deveria casar com %s, escolhido %q", p, got)
		}
	}
}

func TestWildcardPath(t *testing.T) {
	rt := compiled(t, ov("todos", config.OverrideMatch{Path: "/api/payments/*"}))
	if got := selected(t, rt, request("GET", "/api/payments/charge/42", "")); got != "todos" {
		t.Fatalf("o curinga deveria casar com um path mais fundo, escolhido %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/paymentsx", "")); got != "" {
		t.Fatalf("o curinga não deveria casar com um prefixo parcial de segmento, escolhido %q", got)
	}
}

func TestRegexPath(t *testing.T) {
	rt := compiled(t, ov("cobranca", config.OverrideMatch{PathRegex: `^/api/payments/charge/\d+$`}))
	if got := selected(t, rt, request("GET", "/api/payments/charge/42", "")); got != "cobranca" {
		t.Fatalf("a expressão regular deveria casar, escolhido %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/payments/charge/abc", "")); got != "" {
		t.Fatalf("a expressão regular não deveria casar, escolhido %q", got)
	}
}

func TestMethodRestriction(t *testing.T) {
	rt := compiled(t, ov("post", config.OverrideMatch{Path: "/api/payments/charge", Method: "POST"}))
	if got := selected(t, rt, request("GET", "/api/payments/charge", "")); got != "" {
		t.Fatalf("um GET não deveria casar com override restrito a POST, escolhido %q", got)
	}
	if got := selected(t, rt, request("POST", "/api/payments/charge", "")); got != "post" {
		t.Fatalf("um POST deveria casar, escolhido %q", got)
	}
}

func TestHeaderCriteria(t *testing.T) {
	rt := compiled(t, ov("acme", config.OverrideMatch{
		Path:    "/api/*",
		Headers: map[string]config.Matcher{"x-tenant": eq("acme")},
	}))
	if got := selected(t, rt, request("GET", "/api/x", "")); got != "" {
		t.Fatalf("sem o cabeçalho exigido o override não deveria casar, escolhido %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/x", "", "X-Tenant", "outra")); got != "" {
		t.Fatalf("com outro valor o override não deveria casar, escolhido %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/x", "", "X-Tenant", "outra", "X-Tenant", "acme")); got != "acme" {
		t.Fatalf("um dos valores repetidos casando deveria bastar, escolhido %q", got)
	}
}

func TestHeaderOperators(t *testing.T) {
	for _, c := range []struct {
		name  string
		m     config.Matcher
		value string
		want  bool
	}{
		{"igualdade casa", eq("Bearer abc"), "Bearer abc", true},
		{"igualdade é exata", eq("Bearer abc"), "Bearer abcd", false},
		{"regex casa", re(`^Bearer [a-z]+$`), "Bearer abc", true},
		{"regex não casa", re(`^Bearer [a-z]+$`), "Bearer 123", false},
		{"substring casa", contains("abc"), "Bearer xabcx", true},
		{"substring não casa", contains("abc"), "Bearer xyz", false},
		{"JSON casa ignorando espaços e ordem", jsonEq(map[string]any{"a": 1, "b": "x"}), `{ "b": "x", "a": 1 }`, true},
		{"JSON distinto não casa", jsonEq(map[string]any{"a": 1}), `{"a":2}`, false},
		{"valor que não é JSON não casa", jsonEq(map[string]any{"a": 1}), `a=1`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := compiled(t, ov("o", config.OverrideMatch{
				Path: "/api/*", Headers: map[string]config.Matcher{"Authorization": c.m},
			}))
			got := selected(t, rt, request("GET", "/api/x", "", "Authorization", c.value)) == "o"
			if got != c.want {
				t.Fatalf("casou = %v, esperado %v", got, c.want)
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
		t.Fatalf("o critério de Host deveria usar o host da requisição, escolhido %q", got)
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
		"/api/x?status=failed&id=42&tag=cliente-vip":      "q",
		"/api/x?status=failed&id=42&tag=a&tag=vip":        "q",
		"/api/x?status=failed&id=abc&tag=vip":             "",
		"/api/x?status=ok&id=42&tag=vip":                  "",
		"/api/x?status=failed&id=42":                      "",
		"/api/x?STATUS=failed&id=42&tag=vip":              "",
		"/api/x?status=failed&id=42&tag=vip&outro=ignora": "q",
	} {
		if got := selected(t, rt, request("GET", target, "")); got != want {
			t.Errorf("%s: escolhido %q, esperado %q", target, got, want)
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
		{"igualdade casa", eq(`amount=10`), `amount=10`, true},
		{"igualdade é exata", eq(`amount=10`), `amount=100`, false},
		{"regex casa", re(`"amount":\s*\d{3,}`), `{"amount": 1500}`, true},
		{"regex não casa", re(`"amount":\s*\d{3,}`), `{"amount": 15}`, false},
		{"substring casa", contains(`"currency":"BRL"`), `{"amount":1,"currency":"BRL"}`, true},
		{"substring não casa", contains(`"currency":"BRL"`), `{"currency":"USD"}`, false},
		{"JSON ignora a ordem das chaves", jsonEq(map[string]any{"a": 1, "b": 2}), `{"b":2,"a":1}`, true},
		{"JSON aninhado", jsonEq(map[string]any{"a": []any{1, map[string]any{"c": true}}}), `{"a":[1,{"c":true}]}`, true},
		{"JSON distinto não casa", jsonEq(map[string]any{"a": 1, "b": 2}), `{"a":1,"b":3}`, false},
		{"JSON com chave a mais não casa", jsonEq(map[string]any{"a": 1}), `{"a":1,"b":2}`, false},
		{"corpo vazio não casa com JSON", jsonEq(map[string]any{"a": 1}), ``, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			rt := compiled(t, ov("o", config.OverrideMatch{Path: "/api/*", Body: &c.m}))
			got := selected(t, rt, request("POST", "/api/x", c.body)) == "o"
			if got != c.want {
				t.Fatalf("casou = %v, esperado %v", got, c.want)
			}
		})
	}
}

// O cenário da spec: igualdade JSON no corpo ignora a ordem das chaves.
func TestJSONBodyEqualityIgnoresKeyOrder(t *testing.T) {
	m := jsonEq(map[string]any{"a": 1, "b": 2})
	rt := compiled(t, ov("json", config.OverrideMatch{Path: "/api/*", Body: &m}))
	if got := selected(t, rt, request("POST", "/api/x", `{"b":2,"a":1}`)); got != "json" {
		t.Fatalf("o corpo com as chaves em outra ordem deveria casar, escolhido %q", got)
	}
}

func TestAllCriteriaMustMatch(t *testing.T) {
	body := contains("urgente")
	rt := compiled(t, ov("todos", config.OverrideMatch{
		Path:    "/api/payments/*",
		Method:  "POST",
		Headers: map[string]config.Matcher{"X-Tenant": eq("acme")},
		Query:   map[string]config.Matcher{"v": eq("2")},
		Body:    &body,
	}))
	full := func() *http.Request {
		return request("POST", "/api/payments/x?v=2", "pedido urgente", "X-Tenant", "acme")
	}
	if got := selected(t, rt, full()); got != "todos" {
		t.Fatalf("com todos os critérios satisfeitos o override deveria casar, escolhido %q", got)
	}
	for name, r := range map[string]*http.Request{
		"sem o cabeçalho": request("POST", "/api/payments/x?v=2", "pedido urgente"),
		"método errado":   request("PUT", "/api/payments/x?v=2", "pedido urgente", "X-Tenant", "acme"),
		"query errada":    request("POST", "/api/payments/x?v=3", "pedido urgente", "X-Tenant", "acme"),
		"corpo errado":    request("POST", "/api/payments/x?v=2", "pedido normal", "X-Tenant", "acme"),
		"path fora":       request("POST", "/api/orders/x?v=2", "pedido urgente", "X-Tenant", "acme"),
	} {
		if got := selected(t, rt, r); got != "" {
			t.Errorf("%s: um critério que não casa deveria impedir a interceptação, escolhido %q", name, got)
		}
	}
}

// O corpo lido para casar volta íntegro na requisição a encaminhar, case o
// override ou não.
func TestBodyReadForMatchingIsReplayed(t *testing.T) {
	m := eq("nunca")
	rt := compiled(t, ov("o", config.OverrideMatch{Path: "/api/*", Body: &m}))
	const payload = `{"pedido": 1}`
	r := request("POST", "/api/x", payload)
	q := NewRequest(r)
	if Select(rt, q) != nil {
		t.Fatal("o override não deveria casar")
	}
	got, err := io.ReadAll(q.Request().Body)
	if err != nil || string(got) != payload {
		t.Fatalf("o corpo repassado deveria ser o original: %q %v", got, err)
	}
	if q.Request().ContentLength != int64(len(payload)) {
		t.Fatalf("o Content-Length deveria ser preservado: %d", q.Request().ContentLength)
	}
}

// Sem critério de corpo, o corpo não é lido: a requisição segue a mesma.
func TestBodyNotReadWithoutBodyCriterion(t *testing.T) {
	rt := compiled(t, ov("o", config.OverrideMatch{Path: "/api/*", Method: "PUT"}))
	r := request("POST", "/api/x", "corpo")
	q := NewRequest(r)
	Select(rt, q)
	if q.Request() != r {
		t.Fatal("sem critério de corpo a requisição não deveria ser tocada")
	}
}

// Um corpo acima do limite de leitura não casa com critério de corpo e segue
// inteiro para quem encaminhar.
func TestOversizedBodyDoesNotMatchAndIsReplayed(t *testing.T) {
	m := contains("x")
	rt := compiled(t, ov("o", config.OverrideMatch{Path: "/api/*", Body: &m}))
	payload := bytes.Repeat([]byte("x"), MaxBodyMatchBytes+100)
	r := httptest.NewRequest("POST", "/api/x", bytes.NewReader(payload))
	q := NewRequest(r)
	if Select(rt, q) != nil {
		t.Fatal("um corpo acima do limite não deveria casar com critério de corpo")
	}
	got, _ := io.ReadAll(q.Request().Body)
	if !bytes.Equal(got, payload) {
		t.Fatalf("o corpo repassado deveria ser o original inteiro: %d bytes de %d", len(got), len(payload))
	}
}

// O corpo é lido uma única vez, mesmo com vários overrides que o examinam.
func TestBodyReadOnceAcrossOverrides(t *testing.T) {
	a, b := eq("a"), eq("b")
	rt := compiled(t,
		ov("a", config.OverrideMatch{Path: "/api/*", Body: &a}),
		ov("b", config.OverrideMatch{Path: "/api/*", Body: &b}),
	)
	r := request("POST", "/api/x", "b")
	q := NewRequest(r)
	if o := Select(rt, q); o == nil || o.Doc.Name != "b" {
		t.Fatalf("o segundo override deveria casar com o corpo já lido: %v", o)
	}
	got, _ := io.ReadAll(q.Request().Body)
	if string(got) != "b" {
		t.Fatalf("o corpo repassado deveria ser o original: %q", got)
	}
}

// Requirement: Precedência por especificidade

func TestExactPathBeatsWildcard(t *testing.T) {
	rt := compiled(t,
		ov("curinga", config.OverrideMatch{Path: "/api/payments/*"}),
		ov("exato", config.OverrideMatch{Path: "/api/payments/bilulu"}),
	)
	if got := selected(t, rt, request("GET", "/api/payments/bilulu", "")); got != "exato" {
		t.Fatalf("o path exato deveria vencer o curinga, escolhido %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/payments/outro", "")); got != "curinga" {
		t.Fatalf("fora do path exato vale o curinga, escolhido %q", got)
	}
}

func TestLongerWildcardBeatsShorter(t *testing.T) {
	rt := compiled(t,
		ov("api", config.OverrideMatch{Path: "/api/*"}),
		ov("payments", config.OverrideMatch{Path: "/api/payments/*"}),
	)
	if got := selected(t, rt, request("GET", "/api/payments/charge", "")); got != "payments" {
		t.Fatalf("o curinga mais longo deveria vencer, escolhido %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/orders", "")); got != "api" {
		t.Fatalf("fora do curinga longo vale o curto, escolhido %q", got)
	}
}

func TestMoreCriteriaWinsBetweenEqualPaths(t *testing.T) {
	rt := compiled(t,
		ov("qualquer", config.OverrideMatch{Path: "/api/payments/charge"}),
		ov("post", config.OverrideMatch{Path: "/api/payments/charge", Method: "POST"}),
	)
	if got := selected(t, rt, request("POST", "/api/payments/charge", "")); got != "post" {
		t.Fatalf("o override com mais critérios deveria vencer, escolhido %q", got)
	}
	if got := selected(t, rt, request("GET", "/api/payments/charge", "")); got != "qualquer" {
		t.Fatalf("sem o método exigido vale o menos restrito, escolhido %q", got)
	}
}

func TestDeclarationOrderBreaksTies(t *testing.T) {
	rt := compiled(t,
		ov("primeiro", config.OverrideMatch{Path: "/api/payments/*"}),
		ov("segundo", config.OverrideMatch{Path: "/api/payments/*"}),
	)
	if got := selected(t, rt, request("GET", "/api/payments/x", "")); got != "primeiro" {
		t.Fatalf("no empate deveria valer a ordem de declaração, escolhido %q", got)
	}
}

func TestDisabledOverrideIsSkipped(t *testing.T) {
	off := false
	exact := ov("exato", config.OverrideMatch{Path: "/api/payments/bilulu"})
	exact.On = &off
	rt := compiled(t, ov("curinga", config.OverrideMatch{Path: "/api/payments/*"}), exact)
	if got := selected(t, rt, request("GET", "/api/payments/bilulu", "")); got != "curinga" {
		t.Fatalf("o override desligado não deveria participar da precedência, escolhido %q", got)
	}
}
