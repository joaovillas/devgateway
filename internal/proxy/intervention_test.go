package proxy

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
)

func latency(d time.Duration) *config.Latency {
	c := config.Duration(d)
	return &config.Latency{Fixed: &c}
}

func latencyRange(lo, hi time.Duration) *config.Latency {
	a, b := config.Duration(lo), config.Duration(hi)
	return &config.Latency{Min: &a, Max: &b}
}

// timedGet mede quanto a resposta completa levou para chegar.
func timedGet(t *testing.T, url string) (*http.Response, []byte, time.Duration) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	start := time.Now()
	res, body := do(t, req)
	return res, body, time.Since(start)
}

// swapRoutes publica um snapshot novo com as rotas dadas, como uma recarga.
func (g *capGW) swapRoutes(t *testing.T, routes ...config.Route) {
	t.Helper()
	var docs []config.RouteDoc
	for _, r := range routes {
		docs = append(docs, config.NewRouteDoc(r.Name+".yaml", r))
	}
	compiled, err := config.BuildRoutes(docs)
	if err != nil {
		t.Fatal(err)
	}
	g.h.live.Swap(config.NewSnapshot(g.h.live.Load().Settings, compiled, nil))
}

// Requirement: Latência e queda de conexão

func TestFixedLatency(t *testing.T) {
	o := synth("lento", "/api/*", "atrasado")
	o.Latency = latency(2 * time.Second)
	g := capturing(t, recording(), route("payments", "", "/api/*", withOverrides(o)))
	res, body, el := timedGet(t, g.URL+"/api/x")
	if string(body) != "atrasado" {
		t.Fatalf("corpo inesperado: %q", body)
	}
	if el < 2*time.Second {
		t.Fatalf("a resposta deveria chegar ao menos 2s depois, chegou em %v", el)
	}
	// A resposta foi sintetizada e atrasada: o cabeçalho e a troca nomeiam as
	// duas intervenções.
	if got := res.Header.Get(HeaderGateway); got != "route=payments; override=payments/lento; intervention=synthesized,delayed" {
		t.Fatalf("X-Gateway inesperado: %q", got)
	}
	e := g.only(t)
	approx(t, "tempo injetado", e.Timing.InjectedMs, 2000, 2300)
	if !slices.Equal(e.Interventions, []string{"synthesized", "delayed"}) {
		t.Fatalf("a troca deveria registrar síntese e atraso: %v", e.Interventions)
	}
}

func TestLatencyDrawnWithinRange(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	o := synth("variavel", "/api/*", "ok")
	o.Latency = latencyRange(100*time.Millisecond, 500*time.Millisecond)
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	const n = 50
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() {
			res, err := http.Get(g.URL + "/api/x")
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		})
	}
	wg.Wait()
	items := g.history(t, exchange.Filter{})
	if len(items) != n {
		t.Fatalf("esperadas %d trocas, há %d", n, len(items))
	}
	seen := map[float64]bool{}
	for _, e := range items {
		// O atraso medido é o sorteado mais a imprecisão do timer.
		if e.Timing.InjectedMs < 100 || e.Timing.InjectedMs > 600 {
			t.Errorf("atraso %.1fms fora do intervalo de 100ms a 500ms", e.Timing.InjectedMs)
		}
		seen[e.Timing.InjectedMs] = true
	}
	if len(seen) < 2 {
		t.Fatalf("os atrasos sorteados não deveriam ser todos iguais: %v", seen)
	}
}

func TestDropEndsWithoutResponse(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	// A queda tem precedência sobre a resposta declarada.
	o := synth("queda", "/api/*", "nunca enviado")
	o.Respond.Status = http.StatusServiceUnavailable
	o.Drop = true
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	res, err := http.Get(g.URL + "/api/x")
	if err == nil {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("o cliente não deveria receber resposta, recebeu %d %q", res.StatusCode, body)
	}
	if hits.Load() != 0 {
		t.Fatalf("o upstream não deveria ser contatado, recebeu %d", hits.Load())
	}
	// Queda de conexão é registrada (traffic-capture).
	e := g.only(t)
	if e.Outcome != exchange.OutcomeDropped || e.Status != 0 || e.DropMode != exchange.DropHijack ||
		e.Override != "payments/queda" || !slices.Equal(e.Interventions, []string{"dropped"}) || e.Error != "" {
		t.Fatalf("a troca deveria constar como encerrada por queda, sem status: %+v", e)
	}
	if len(e.Response.Body) != 0 {
		t.Fatalf("nenhum corpo deveria constar da resposta: %q", e.Response.Body)
	}
	// O servidor continua atendendo depois da queda.
	if _, body := g.get(t, "/fora"); len(body) == 0 {
		t.Fatal("o gateway deveria continuar respondendo")
	}
}

// Em HTTP/2 não há socket a sequestrar: o stream é cancelado abruptamente e a
// captura registra esse modo.
func TestDropOverHTTP2ResetsStream(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("queda", "/api/*", "nunca enviado")
	o.Drop = true
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	srv := httptest.NewUnstartedServer(g.h)
	srv.EnableHTTP2 = true
	srv.Config.ErrorLog = discardLog()
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client := srv.Client()

	// Uma requisição sem override confirma que a conexão é HTTP/2.
	res, err := client.Get(srv.URL + "/fora")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.ProtoMajor != 2 {
		t.Fatalf("o teste exige HTTP/2, a conexão é %s", res.Proto)
	}

	res, err = client.Get(srv.URL + "/api/x")
	if err == nil {
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Fatalf("o cliente não deveria receber resposta, recebeu %d %q", res.StatusCode, body)
	}
	if hits.Load() != 0 {
		t.Fatalf("o upstream não deveria ser contatado, recebeu %d", hits.Load())
	}
	items := g.history(t, exchange.Filter{Override: "payments/queda"})
	if len(items) != 1 {
		t.Fatalf("esperada uma troca derrubada, há %d", len(items))
	}
	if e := items[0]; e.Outcome != exchange.OutcomeDropped || e.Status != 0 || e.DropMode != exchange.DropStreamReset || e.Error != "" {
		t.Fatalf("a troca deveria constar como stream cancelado, sem status: %+v", e)
	}
}

func TestLatencyOnlyDelaysUpstreamResponse(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := config.Override{Name: "lento", Match: config.OverrideMatch{Path: "/api/*"}, Latency: latency(300 * time.Millisecond)}
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	res, body, el := timedGet(t, g.URL+"/api/x")
	if e := echoOf(t)(res, body); e.Path != "/api/x" {
		t.Fatalf("a requisição deveria ser encaminhada normalmente: %+v", e)
	}
	if hits.Load() != 1 {
		t.Fatalf("o upstream deveria receber a requisição, recebeu %d", hits.Load())
	}
	if el < 300*time.Millisecond {
		t.Fatalf("a resposta do upstream deveria chegar depois do atraso, chegou em %v", el)
	}
	if got := res.Header.Get(HeaderGateway); got != "route=payments; override=payments/lento; intervention=delayed" {
		t.Fatalf("X-Gateway deveria identificar o atraso: %q", got)
	}
	e := g.only(t)
	if e.Outcome != exchange.OutcomeUpstream || !slices.Equal(e.Interventions, []string{"delayed"}) || e.Override != "payments/lento" {
		t.Fatalf("a troca deveria vir do upstream com o atraso anotado: %+v", e)
	}
	approx(t, "tempo injetado", e.Timing.InjectedMs, 300, 500)
}

// Cenário "Tempo injetado separado do tempo real" da spec traffic-capture,
// agora com um override real.
func TestInjectedTimeSeparatedFromUpstreamWithOverride(t *testing.T) {
	const upstreamDelay = 150 * time.Millisecond
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(upstreamDelay)
		io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	o := config.Override{Name: "lenta", Match: config.OverrideMatch{Path: "/*"}, Latency: latency(2 * time.Second)}
	g := capturing(t, recording(), route("payments", up.URL, "/*", withOverrides(o)))
	_, body, el := timedGet(t, g.URL+"/x")
	if string(body) != "ok" {
		t.Fatalf("corpo %q", body)
	}
	if el < upstreamDelay+2*time.Second {
		t.Fatalf("o atraso deveria somar ao tempo do upstream; a requisição levou %v", el)
	}
	tm := g.only(t).Timing
	approx(t, "tempo injetado", tm.InjectedMs, 2000, 2200)
	approx(t, "tempo de upstream", tm.UpstreamMs, 150, 300)
	if sum := tm.UpstreamMs + tm.InjectedMs + tm.GatewayMs; sum < tm.TotalMs-0.001 || sum > tm.TotalMs+0.001 {
		t.Errorf("o total deveria ser a soma de upstream, injetado e overhead: %+v", tm)
	}
}

// Requirement: Expiração por tempo e por contagem (de ponta a ponta)

func TestOverrideExpiresByCountEndToEnd(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("duas", "/api/*", "override")
	o.MaxApplications = ptr(2)
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	var got []string
	for range 3 {
		_, body := g.get(t, "/api/x")
		got = append(got, string(body))
	}
	if got[0] != "override" || got[1] != "override" || got[2] == "override" {
		t.Fatalf("as duas primeiras deveriam vir do override e a terceira do upstream: %q", got)
	}
	if hits.Load() != 1 {
		t.Fatalf("só a terceira deveria chegar ao upstream, chegaram %d", hits.Load())
	}
	s, ok := g.h.Tracker().State("payments", "duas")
	if !ok || s.Applications != 2 || s.Active || s.Expired == nil || *s.Expired != "applications" {
		t.Fatalf("a consulta deveria informar duas aplicações e a expiração: %+v", s)
	}
}

func TestOverrideExpiresByTimeEndToEnd(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("breve", "/api/*", "override")
	o.TTL = ptr(config.Duration(300 * time.Millisecond))
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	if _, body := g.get(t, "/api/x"); string(body) != "override" {
		t.Fatalf("dentro do tempo de vida o override deveria valer: %q", body)
	}
	s, _ := g.h.Tracker().State("payments", "breve")
	if s.TTLRemainingMs == nil || *s.TTLRemainingMs <= 0 || *s.TTLRemainingMs > 300 {
		t.Fatalf("o restante do tempo de vida deveria ser consultável: %+v", s)
	}
	time.Sleep(400 * time.Millisecond)
	echoOf(t)(g.get(t, "/api/x"))
	if hits.Load() != 1 {
		t.Fatalf("depois do tempo de vida a requisição deveria voltar ao upstream, chegaram %d", hits.Load())
	}
}

// O limite de aplicações vale sob concorrência, e o override esgotado sai da
// precedência: o menos específico passa a valer.
func TestCountLimitUnderConcurrencyFallsBackToLessSpecific(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	exact := synth("exato", "/api/x", "exato")
	exact.MaxApplications = ptr(5)
	g := capturing(t, recording(), route("payments", up.URL, "/api/*",
		withOverrides(exact, synth("curinga", "/api/*", "curinga"))))
	var mu sync.Mutex
	count := map[string]int{}
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			res, err := http.Get(g.URL + "/api/x")
			if err != nil {
				t.Error(err)
				return
			}
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			mu.Lock()
			count[string(b)]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if count["exato"] != 5 || count["curinga"] != 35 {
		t.Fatalf("esperadas 5 respostas do exato e 35 do curinga: %v", count)
	}
}

// Recarregar sem mudar o override preserva a contagem.
func TestExpirationStatePreservedAcrossReload(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("duas", "/api/*", "override")
	o.MaxApplications = ptr(2)
	r := route("payments", up.URL, "/api/*", withOverrides(o))
	g := capturing(t, recording(), r)
	g.get(t, "/api/x")
	g.swapRoutes(t, r, route("orders", up.URL, "/orders/*"))
	g.get(t, "/api/x")
	echoOf(t)(g.get(t, "/api/x"))
	if hits.Load() != 1 {
		t.Fatalf("a recarga não deveria zerar a contagem; chegaram %d ao upstream", hits.Load())
	}
}

// Requirement: Identificação da intervenção

func TestUpstreamErrorNotMarkedInHeader(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "falhou", http.StatusInternalServerError)
	}))
	t.Cleanup(up.Close)
	// Um override que não casa não muda nada.
	g := capturing(t, recording(), route("payments", up.URL, "/api/*",
		withOverrides(synth("outro", "/api/outro", "x"))))
	res, _ := g.get(t, "/api/x")
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("esperado 500 do upstream, recebido %d", res.StatusCode)
	}
	if got := res.Header.Values(HeaderGateway); len(got) != 1 || got[0] != "route=payments" {
		t.Fatalf("X-Gateway deveria identificar só a rota: %q", got)
	}
	if e := g.only(t); e.Intervened() || e.Override != "" || e.Outcome != exchange.OutcomeUpstream {
		t.Fatalf("a troca não deveria ter marcação de intervenção: %+v", e)
	}
}

// Cenário "Filtro por intervenção" da spec traffic-capture, com trocas
// sintetizadas e atrasadas por overrides reais.
func TestHistoryFilterByInterventionWithRealOverrides(t *testing.T) {
	up := statusUpstream(t)
	lenta := config.Override{Name: "lenta", Match: config.OverrideMatch{Path: "/api/payments/lenta"}, Latency: latency(time.Millisecond)}
	flaky := config.Override{Name: "flaky", Match: config.OverrideMatch{Path: "/api/payments/sintetizada"},
		Respond: &config.Respond{Status: http.StatusServiceUnavailable}}
	g := capturing(t, recording(), route("payments", up.URL, "/api/payments/*", withOverrides(lenta, flaky)))
	g.get(t, "/api/payments/normal")
	g.get(t, "/api/payments/lenta")
	g.get(t, "/api/payments/sintetizada")
	g.get(t, "/api/payments/erro?status=500")
	yes, no := true, false
	with := g.history(t, exchange.Filter{Intervened: &yes})
	if len(with) != 2 {
		t.Fatalf("o filtro por intervenção deveria trazer a sintetizada e a atrasada: %v", with)
	}
	synthesized, delayed := with[0], with[1]
	if synthesized.Path != "/api/payments/sintetizada" || synthesized.Outcome != exchange.OutcomeSynthesized ||
		synthesized.Override != "payments/flaky" || synthesized.Status != http.StatusServiceUnavailable {
		t.Fatalf("a troca sintetizada deveria constar do filtro: %+v", synthesized)
	}
	if delayed.Path != "/api/payments/lenta" || delayed.Override != "payments/lenta" || delayed.Outcome != exchange.OutcomeUpstream {
		t.Fatalf("a troca atrasada deveria constar do filtro: %+v", delayed)
	}
	if without := g.history(t, exchange.Filter{Intervened: &no}); len(without) != 2 {
		t.Fatalf("esperadas 2 trocas sem intervenção, há %d", len(without))
	}
}

// Requirement: Override ligado e desligado

func TestDisabledOverrideDoesNotIntercept(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("fora", "/api/*", "indisponível")
	o.Respond.Status = http.StatusServiceUnavailable
	o.On = ptr(false)
	g := capturing(t, recording(), route("payments", up.URL, "/api/*", withOverrides(o)))
	res, body := g.get(t, "/api/x")
	echoOf(t)(res, body)
	if hits.Load() != 1 || res.Header.Get(HeaderGateway) != "route=payments" {
		t.Fatalf("a requisição deveria seguir ao upstream sem marcação: %d %q", hits.Load(), res.Header.Get(HeaderGateway))
	}
}

func TestDisabledDoesNotHideLessSpecific(t *testing.T) {
	up, _ := countingUpstream(t, "payments")
	exact := synth("exato", "/api/x", "exato")
	exact.On = ptr(false)
	gw := gateway(t, route("payments", up.URL, "/api/*", withOverrides(exact, synth("curinga", "/api/*", "curinga"))))
	if _, body := get(t, gw, "/api/x"); string(body) != "curinga" {
		t.Fatalf("o curinga ligado deveria ser aplicado: %q", body)
	}
}

func TestReenablingRestoresBehavior(t *testing.T) {
	up, hits := countingUpstream(t, "payments")
	o := synth("fora", "/api/*", "indisponível")
	o.Respond.Status = http.StatusServiceUnavailable
	o.Respond.Headers = map[string]config.HeaderValues{"Retry-After": {"5"}}
	on := route("payments", up.URL, "/api/*", withOverrides(o))
	g := capturing(t, recording(), on)
	res, first := g.get(t, "/api/x")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("ligado, o override deveria responder: %d", res.StatusCode)
	}
	off := o
	off.On = ptr(false)
	g.swapRoutes(t, route("payments", up.URL, "/api/*", withOverrides(off)))
	echoOf(t)(g.get(t, "/api/x"))
	g.swapRoutes(t, on)
	res, again := g.get(t, "/api/x")
	if res.StatusCode != http.StatusServiceUnavailable || string(again) != string(first) || res.Header.Get("Retry-After") != "5" {
		t.Fatalf("religado, o override deveria responder igual: %d %q %v", res.StatusCode, again, res.Header)
	}
	if hits.Load() != 1 {
		t.Fatalf("só a requisição com o override desligado deveria chegar ao upstream, chegaram %d", hits.Load())
	}
}

// discardLog silencia o log do servidor de teste, que registraria o
// cancelamento do stream como erro.
func discardLog() *log.Logger { return log.New(io.Discard, "", 0) }

// Um override só de latência atrasa também as respostas de erro do gateway
// (502 e 504), no mesmo ponto entre a resposta pronta e a escrita, e o
// X-Gateway identifica o atraso.
func TestLatencyOnlyDelaysGatewayErrors(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(slow.Close)
	timeout := config.Duration(100 * time.Millisecond)
	lento := func(name string) config.Override {
		return config.Override{Name: name, Match: config.OverrideMatch{Path: "/*"}, Latency: latency(400 * time.Millisecond)}
	}
	for _, c := range []struct {
		route    config.Route
		status   int
		minDelay time.Duration
	}{
		{route("fora", down.URL, "/fora/*", withOverrides(lento("lento"))), http.StatusBadGateway, 400 * time.Millisecond},
		{route("lenta", slow.URL, "/lenta/*", withOverrides(lento("lento")), func(r *config.Route) { r.Timeout = &timeout }),
			http.StatusGatewayTimeout, 500 * time.Millisecond},
	} {
		t.Run(c.route.Name, func(t *testing.T) {
			g := capturing(t, recording(), c.route)
			res, _, el := timedGet(t, g.URL+"/"+c.route.Name+"/x")
			if res.StatusCode != c.status {
				t.Fatalf("esperado %d, recebido %d", c.status, res.StatusCode)
			}
			if el < c.minDelay {
				t.Fatalf("a resposta de erro deveria chegar depois do atraso, chegou em %v", el)
			}
			want := "route=" + c.route.Name + "; override=" + c.route.Name + "/lento; intervention=delayed"
			if got := res.Header.Get(HeaderGateway); got != want {
				t.Fatalf("X-Gateway inesperado: %q", got)
			}
			e := g.only(t)
			if e.Status != c.status || e.Outcome != exchange.OutcomeGateway || !slices.Equal(e.Interventions, []string{"delayed"}) {
				t.Fatalf("a troca deveria registrar o erro atrasado: %+v", e)
			}
			approx(t, "tempo injetado", e.Timing.InjectedMs, 400, 600)
		})
	}
}

// Se o cliente desiste durante o atraso de uma resposta sintetizada, nada foi
// entregue: a troca fica sem status, com a desistência anotada.
func TestClientCancelDuringSynthesizedDelay(t *testing.T) {
	o := synth("lento", "/api/*", "nunca entregue")
	o.Latency = latency(2 * time.Second)
	g := capturing(t, recording(), route("payments", "", "/api/*", withOverrides(o)))
	client := &http.Client{Timeout: 200 * time.Millisecond}
	if res, err := client.Get(g.URL + "/api/x"); err == nil {
		res.Body.Close()
		t.Fatalf("o cliente deveria desistir antes da resposta, recebeu %d", res.StatusCode)
	}
	var e exchange.Exchange
	deadline := time.Now().Add(5 * time.Second)
	for {
		if items := g.history(t, exchange.Filter{}); len(items) == 1 {
			e = items[0]
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a troca não foi registrada")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e.Status != 0 || !strings.HasPrefix(e.Error, "client_canceled") {
		t.Fatalf("a troca não deveria registrar resposta entregue: status %d, erro %q", e.Status, e.Error)
	}
	if !slices.Equal(e.Interventions, []string{"synthesized", "delayed"}) {
		t.Fatalf("as intervenções deveriam constar: %v", e.Interventions)
	}
}

// Um ResponseWriter de HTTP/1 sem sequestro de conexão não impede a queda: o
// handler é abortado sem resposta, e a captura registra o modo abort.
func TestDropWithoutHijackerAborts(t *testing.T) {
	o := synth("queda", "/api/*", "nunca enviado")
	o.Drop = true
	g := capturing(t, recording(), route("payments", "", "/api/*", withOverrides(o)))
	w := httptest.NewRecorder()
	func() {
		defer func() {
			if p := recover(); p != http.ErrAbortHandler {
				t.Fatalf("o handler deveria ser abortado com http.ErrAbortHandler: %v", p)
			}
		}()
		g.h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	}()
	if w.Body.Len() != 0 || w.Code != http.StatusOK && w.Code != 0 {
		t.Fatalf("nada deveria ser escrito: %d %q", w.Code, w.Body)
	}
	e := g.only(t)
	if e.Outcome != exchange.OutcomeDropped || e.DropMode != exchange.DropAbort || e.Status != 0 {
		t.Fatalf("a troca deveria registrar a queda por abort: %+v", e)
	}
}

// Cabeçalhos com vários valores declarados na resposta, como Set-Cookie, são
// repetidos na resposta sintetizada.
func TestSynthesizedRepeatedHeaders(t *testing.T) {
	o := synth("cookies", "/api/*", "ok")
	o.Respond.Headers = map[string]config.HeaderValues{"Set-Cookie": {"a=1", "b=2"}}
	g := capturing(t, recording(), route("payments", "", "/api/*", withOverrides(o)))
	res, _ := g.get(t, "/api/x")
	if got := res.Header.Values("Set-Cookie"); !slices.Equal(got, []string{"a=1", "b=2"}) {
		t.Fatalf("os dois Set-Cookie deveriam chegar ao cliente: %q", got)
	}
}
