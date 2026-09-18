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

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
)

// countingUpstream devolve o eco da requisição e conta quantas recebeu.
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

// Requirement: Interceptação seletiva com passthrough por padrão

func TestRouteWithoutOverrideForwardsAll(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	g := capturing(t, recording(), route("payments", up.URL, "/api/payments/*"))
	for _, p := range []string{"/api/payments/a", "/api/payments/b/c", "/api/payments"} {
		res, body := g.get(t, p)
		if e := echoOf(t)(res, body); e.Path != p {
			t.Fatalf("o upstream deveria receber %s, recebeu %s", p, e.Path)
		}
		if got := res.Header.Get(HeaderGateway); got != "route=payments" {
			t.Fatalf("X-Gateway sem intervenção esperado, recebido %q", got)
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("as três requisições deveriam chegar ao upstream, chegaram %d", hits.Load())
	}
	for _, e := range g.history(t, exchange.Filter{}) {
		if e.Outcome != exchange.OutcomeUpstream || e.Intervened() {
			t.Fatalf("nenhuma resposta deveria ser sintetizada: %+v", e)
		}
	}
}

func TestOverrideInterceptsOnlyWhatMatches(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL, "/api/payments/*",
		withOverrides(synth("bilulu", "/api/payments/bilulu", "do override"))))
	res, body := get(t, gw, "/api/payments/bilulu")
	if res.StatusCode != http.StatusOK || string(body) != "do override" {
		t.Fatalf("a requisição casada deveria ser respondida pelo override: %d %q", res.StatusCode, body)
	}
	if hits.Load() != 0 {
		t.Fatalf("o upstream não deveria ser contatado, recebeu %d", hits.Load())
	}
	if e := echoOf(t)(get(t, gw, "/api/payments/charge")); e.Path != "/api/payments/charge" {
		t.Fatalf("a requisição não casada deveria seguir ao upstream: %+v", e)
	}
	if hits.Load() != 1 {
		t.Fatalf("somente a requisição não casada deveria chegar ao upstream, chegaram %d", hits.Load())
	}
}

func TestRouteWithoutUpstreamAndNoMatchingOverride(t *testing.T) {
	gw := gateway(t, route("mock", "", "/api/*",
		withOverrides(synth("bilulu", "/api/bilulu", "ok"))))
	res, body := get(t, gw, "/api/outro")
	if res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("esperado 501, recebido %d: %s", res.StatusCode, body)
	}
	var d diag
	if err := json.Unmarshal(body, &d); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	if d.Route != "mock" || !strings.Contains(d.Message, "upstream") || !strings.Contains(d.Message, "override") {
		t.Fatalf("o corpo deveria informar a rota e a ausência de upstream e de override: %+v", d)
	}
	// O override casado responde mesmo sem upstream.
	if res, body := get(t, gw, "/api/bilulu"); res.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("o override deveria responder numa rota sem upstream: %d %q", res.StatusCode, body)
	}
}

// Sem aplicação sorteada, a rota sem upstream responde 501 como se o override
// não existisse.
func TestRouteWithoutUpstreamAndOverrideNotDrawn(t *testing.T) {
	o := synth("nunca", "/api/*", "ok")
	o.Probability = ptr(0.0)
	gw := gateway(t, route("mock", "", "/api/*", withOverrides(o)))
	if res, _ := get(t, gw, "/api/x"); res.StatusCode != http.StatusNotImplemented {
		t.Fatalf("esperado 501, recebido %d", res.StatusCode)
	}
}

// Requirement: Critérios de seleção do override (de ponta a ponta)

func TestMethodRestrictionForwardsOtherMethods(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("post", "/api/payments/charge", "interceptado")
	o.Match.Method = http.MethodPost
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o)))
	if e := echoOf(t)(get(t, gw, "/api/payments/charge")); e.Method != http.MethodGet {
		t.Fatalf("o GET deveria ser encaminhado ao upstream: %+v", e)
	}
	if _, body := post(t, gw.URL+"/api/payments/charge", "x"); string(body) != "interceptado" {
		t.Fatalf("o POST deveria ser interceptado: %q", body)
	}
	if hits.Load() != 1 {
		t.Fatalf("só o GET deveria chegar ao upstream, chegaram %d", hits.Load())
	}
}

func TestJSONBodyMatchEndToEnd(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := synth("json", "/api/*", "interceptado")
	o.Match.Body = &config.Matcher{JSON: map[string]any{"a": 1, "b": 2}}
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o)))
	if _, body := post(t, gw.URL+"/api/x", `{"b":2,"a":1}`); string(body) != "interceptado" {
		t.Fatalf("o corpo JSON com chaves em outra ordem deveria casar: %q", body)
	}
	// O corpo lido para casar chega íntegro ao upstream quando não casa.
	const other = `{"a":1,"b":3}`
	if e := echoOf(t)(post(t, gw.URL+"/api/x", other)); string(e.Body) != other || e.Method != http.MethodPost {
		t.Fatalf("o corpo repassado deveria ser o original: %q", e.Body)
	}
}

// O corpo lido para casar é repassado íntegro mesmo sem Content-Length e acima
// do limite de captura, e a captura o registra uma única vez.
func TestBodyReadForMatchingIsForwardedWhole(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := synth("nunca", "/api/*", "interceptado")
	o.Match.Body = &config.Matcher{Equals: ptr("nunca")}
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
		t.Fatalf("o upstream deveria receber o corpo inteiro: %d bytes de %d", len(e.Body), len(payload))
	}
	ex := g.only(t)
	if ex.Request.Size != int64(len(payload)) || !ex.Request.Truncated || string(ex.Request.Body) != string(payload[:16]) {
		t.Fatalf("a captura deveria ver o corpo uma única vez: tamanho %d, truncado %v, %q",
			ex.Request.Size, ex.Request.Truncated, ex.Request.Body)
	}
}

func TestMissingHeaderPreventsInterception(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("acme", "/api/*", "interceptado")
	o.Match.Headers = map[string]config.Matcher{"X-Tenant": {Equals: ptr("acme")}}
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o)))
	echoOf(t)(get(t, gw, "/api/x"))
	if _, body := get(t, gw, "/api/x", "X-Tenant", "acme"); string(body) != "interceptado" {
		t.Fatalf("com o cabeçalho o override deveria casar: %q", body)
	}
	if hits.Load() != 1 {
		t.Fatalf("só a requisição sem o cabeçalho deveria chegar ao upstream, chegaram %d", hits.Load())
	}
}

// Requirement: Precedência por especificidade (de ponta a ponta)

func TestOverridePrecedenceEndToEnd(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	post := synth("post", "/api/payments/charge", "post")
	post.Match.Method = http.MethodPost
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(
		synth("api", "/api/*", "api"),
		synth("payments", "/api/payments/*", "payments"),
		synth("bilulu", "/api/payments/bilulu", "bilulu"),
		synth("charge", "/api/payments/charge", "charge"),
		post,
	)))
	for _, c := range []struct{ method, path, want string }{
		{http.MethodGet, "/api/payments/bilulu", "bilulu"},
		{http.MethodGet, "/api/payments/outro", "payments"},
		{http.MethodGet, "/api/orders", "api"},
		{http.MethodPost, "/api/payments/charge", "post"},
		{http.MethodGet, "/api/payments/charge", "charge"},
	} {
		req, _ := http.NewRequest(c.method, gw.URL+c.path, nil)
		res, body := do(t, req)
		if string(body) != c.want {
			t.Errorf("%s %s: aplicado %q, esperado %q", c.method, c.path, body, c.want)
		}
		if got, want := res.Header.Get(HeaderGateway), "route=payments; override=payments/"+c.want+"; intervention=synthesized"; got != want {
			t.Errorf("%s %s: X-Gateway %q, esperado %q", c.method, c.path, got, want)
		}
	}
}

// Requirement: Resposta declarada pelo override

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
		t.Fatalf("esperado 200, recebido %d", res.StatusCode)
	}
	if res.Header.Get("X-Source") != "override" {
		t.Fatalf("o cabeçalho declarado deveria chegar: %v", res.Header)
	}
	if string(body) != `{"id":42,"status":"paid"}` {
		t.Fatalf("corpo inesperado: %s", body)
	}
	if hits.Load() != 0 {
		t.Fatalf("o upstream não deveria ser contatado, recebeu %d", hits.Load())
	}
	// Além do declarado, só o X-Gateway e os cabeçalhos do próprio HTTP.
	for k := range res.Header {
		if !slices.Contains([]string{"X-Source", "Content-Type", HeaderGateway, "Content-Length", "Date"}, k) {
			t.Errorf("cabeçalho inesperado na resposta sintetizada: %s", k)
		}
	}
}

func TestDefaultStatusIs200(t *testing.T) {
	gw := gateway(t, route("mock", "", "/*", withOverrides(synth("o", "/*", "só corpo"))))
	res, body := get(t, gw, "/x")
	if res.StatusCode != http.StatusOK || string(body) != "só corpo" {
		t.Fatalf("esperado 200 com o corpo declarado, recebido %d %q", res.StatusCode, body)
	}
	// Corpo de texto sem tipo declarado não ganha um tipo deduzido.
	if ct, ok := res.Header["Content-Type"]; ok {
		t.Fatalf("nenhum tipo de conteúdo deveria ser deduzido do texto: %q", ct)
	}
}

func TestContentTypeInferredFromJSONBody(t *testing.T) {
	gw := gateway(t, route("mock", "", "/*", withOverrides(
		synth("json", "/json", map[string]any{"b": []any{1, 2}, "a": "x"}),
		config.Override{
			Name: "problema", Match: config.OverrideMatch{Path: "/problema"},
			Respond: &config.Respond{
				Status:  http.StatusBadRequest,
				Headers: map[string]config.HeaderValues{"Content-Type": {"application/problem+json"}},
				Body:    map[string]any{"title": "inválido"},
			},
		},
	)))
	res, body := get(t, gw, "/json")
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("esperado application/json, recebido %q", ct)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil || v["a"] != "x" {
		t.Fatalf("o corpo deveria ser o JSON serializado: %s %v", body, err)
	}
	res, _ = get(t, gw, "/problema")
	if res.StatusCode != http.StatusBadRequest || res.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("o tipo declarado deveria prevalecer: %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
}

// Identificação com intervenção (gateway-routing) e resposta sintetizada
// marcada na captura (traffic-capture), com um override real.
func TestSynthesizedResponseIdentifiedAndRecorded(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := config.Override{
		Name: "flaky", Match: config.OverrideMatch{Path: "/api/payments/*"},
		Respond: &config.Respond{Status: http.StatusServiceUnavailable, Body: "indisponível"},
	}
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	res, _ := g.get(t, "/api/payments/x")
	if got := res.Header.Values(HeaderGateway); len(got) != 1 ||
		got[0] != "route=payments; override=payments/flaky; intervention=synthesized" {
		t.Fatalf("X-Gateway deveria identificar rota, override e intervenção: %q", got)
	}
	e := g.only(t)
	if e.Status != http.StatusServiceUnavailable || e.Outcome != exchange.OutcomeSynthesized ||
		e.Override != "payments/flaky" || !slices.Contains(e.Interventions, "synthesized") {
		t.Fatalf("a troca deveria constar como sintetizada pelo override: %+v", e)
	}
	if string(e.Response.Body) != "indisponível" {
		t.Fatalf("a captura deveria registrar o corpo sintetizado: %q", e.Response.Body)
	}
}

// Cenário "Resposta sintetizada não contabiliza tempo de upstream" da spec
// traffic-capture, com a resposta sintetizada por override real.
func TestSynthesizedResponseHasZeroUpstreamTime(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	g := capturing(t, recording(), route("payments", up.URL, "/api/*",
		withOverrides(synth("o", "/api/*", "ok"))))
	g.get(t, "/api/x")
	e := g.only(t)
	if e.Timing.UpstreamMs != 0 || e.Outcome != exchange.OutcomeSynthesized {
		t.Fatalf("sem contato com o upstream o tempo de upstream deveria ser zero: %+v %s", e.Timing, e.Outcome)
	}
}

// Requirement: Probabilidade de aplicação

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
			t.Fatalf("a requisição %d deveria ser respondida pelo override: %q", i+1, body)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("nenhuma requisição deveria chegar ao upstream, chegaram %d", hits.Load())
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
				t.Fatalf("resposta do override sem identificação: %q", got)
			}
		default:
			echoOf(t)(res, body)
			if got := res.Header.Get(HeaderGateway); got != "route=payments" {
				t.Fatalf("resposta do upstream não deveria ser marcada: %q", got)
			}
		}
	}
	// Binomial(1000, 0.3): desvio padrão ≈ 14,5; a tolerância é de 4 desvios.
	if overridden < 242 || overridden > 358 {
		t.Fatalf("esperadas cerca de 300 respostas do override, houve %d", overridden)
	}
	if got := hits.Load(); got != int64(n-overridden) {
		t.Fatalf("as demais %d deveriam chegar ao upstream, chegaram %d", n-overridden, got)
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
		t.Fatalf("todas deveriam chegar ao upstream, chegaram %d", hits.Load())
	}
	if with := g.history(t, exchange.Filter{Intervened: ptr(true)}); len(with) != 0 {
		t.Fatalf("nenhuma troca deveria constar como intervenção: %v", with)
	}
}

// Um override não sorteado não esconde o upstream nem é substituído pelo
// menos específico: a requisição segue como se ele não existisse.
func TestNotDrawnBehavesAsIfAbsent(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := synth("exato", "/api/x", "exato")
	o.Probability = ptr(0.0)
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(o, synth("curinga", "/api/*", "curinga"))))
	// O mais específico é selecionado e, não sorteado, a requisição segue ao
	// upstream: o sorteio decide sobre o override resolvido no passo 3.
	if e := echoOf(t)(get(t, gw, "/api/x")); e.Path != "/api/x" {
		t.Fatalf("a requisição deveria seguir ao upstream: %+v", e)
	}
}

// Requirement: Determinismo por seed

// intercepted envia a sequência de requisições e devolve os índices das que
// foram respondidas pelo override.
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
		t.Fatalf("o mesmo seed deveria interceptar as mesmas requisições:\n%v\n%v", a, b)
	}
	if len(a) == 0 || len(a) == 100 {
		t.Fatalf("com probabilidade 0.5 deveria haver interceptadas e encaminhadas: %v", a)
	}
}

func TestDistinctSeedsDiverge(t *testing.T) {
	if a, b := intercepted(t, seeded(42), 100), intercepted(t, seeded(43), 100); slices.Equal(a, b) {
		t.Fatalf("seeds distintos deveriam interceptar conjuntos diferentes: %v", a)
	}
}

func TestNoSeedIsNotReproducible(t *testing.T) {
	if a, b := intercepted(t, recording(), 100), intercepted(t, recording(), 100); slices.Equal(a, b) {
		t.Fatalf("sem seed as execuções não deveriam coincidir: %v", a)
	}
}

// Sob requisições concorrentes, cada número de sequência recebe a mesma
// decisão nas duas execuções: o sorteio não depende da ordem de escalonamento.
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
		t.Fatalf("esperadas 100 trocas em cada execução: %d e %d", len(a), len(b))
	}
	for seq, v := range a {
		if b[seq] != v {
			t.Fatalf("a sequência %d teve decisões diferentes nas duas execuções", seq)
		}
	}
}
