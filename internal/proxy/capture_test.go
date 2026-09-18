package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/capture"
	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
)

// capGW é um gateway de teste com acesso ao histórico em que ele grava.
type capGW struct {
	*httptest.Server
	h    *Handler
	rec  *capture.Recorder
	hist store.Store
}

func capturing(t *testing.T, s config.Settings, routes ...config.Route) *capGW {
	t.Helper()
	return capturingInto(t, store.NewMemory(1000), s, routes...)
}

func capturingInto(t *testing.T, hist store.Store, s config.Settings, routes ...config.Route) *capGW {
	t.Helper()
	rec := capture.NewRecorder(hist, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(rec.Close)
	h := NewHandler(liveWith(t, s, routes...), rec)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &capGW{Server: srv, h: h, rec: rec, hist: hist}
}

// history devolve as trocas gravadas, da mais nova para a mais antiga,
// depois de esperar a gravação das já respondidas.
func (g *capGW) history(t *testing.T, f exchange.Filter) []exchange.Exchange {
	t.Helper()
	if err := g.rec.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	var all []exchange.Exchange
	p := store.Page{Limit: store.MaxLimit}
	for {
		res, err := g.hist.List(t.Context(), f, p)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, res.Items...)
		if res.Next == "" {
			return all
		}
		p.Cursor = res.Next
	}
}

// only devolve a única troca gravada, completa.
func (g *capGW) only(t *testing.T) exchange.Exchange {
	t.Helper()
	items := g.history(t, exchange.Filter{})
	if len(items) != 1 {
		t.Fatalf("esperada uma troca no histórico, há %d", len(items))
	}
	e, err := g.hist.Get(t.Context(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (g *capGW) get(t *testing.T, path string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, g.URL+path, nil)
	return do(t, req)
}

func approx(t *testing.T, name string, got, min, max float64) {
	t.Helper()
	if got < min || got > max {
		t.Errorf("%s = %.1fms, esperado entre %.0fms e %.0fms", name, got, min, max)
	}
}

// Requirement: Registro das trocas HTTP

func TestExchangeRecordedCompletely(t *testing.T) {
	up := echoUpstream(t, "payments")
	g := capturing(t, recording(), route("payments", up.URL, "/api/*"))

	req, _ := http.NewRequest(http.MethodPost, g.URL+"/api/charge?amount=100&retry=1", strings.NewReader(`{"card":"4242"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "abc-123")
	res, body := do(t, req)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}

	e := g.only(t)
	if len(e.ID) != 26 || e.Seq == 0 || e.Start.IsZero() {
		t.Errorf("identificação incompleta: id %q, seq %d, início %v", e.ID, e.Seq, e.Start)
	}
	if e.Route != "payments" || e.Upstream != up.URL {
		t.Errorf("rota %q e upstream %q, esperados payments e %s", e.Route, e.Upstream, up.URL)
	}
	if e.Method != http.MethodPost || e.Path != "/api/charge" || e.Query != "amount=100&retry=1" {
		t.Errorf("requisição registrada como %s %s ? %s", e.Method, e.Path, e.Query)
	}
	if e.Host == "" || e.ClientAddr == "" {
		t.Errorf("host %q e endereço do cliente %q deveriam constar", e.Host, e.ClientAddr)
	}
	if e.Status != http.StatusOK || e.Outcome != exchange.OutcomeUpstream || e.Intervened() {
		t.Errorf("status %d, resultado %s, intervenções %v", e.Status, e.Outcome, e.Interventions)
	}
	if got := e.Request.Headers.Get("X-Request-Id"); got != "abc-123" {
		t.Errorf("cabeçalho da requisição registrado como %q", got)
	}
	if string(e.Request.Body) != `{"card":"4242"}` || e.Request.Size != 15 || e.Request.Truncated {
		t.Errorf("corpo da requisição %q, tamanho %d, truncado %v", e.Request.Body, e.Request.Size, e.Request.Truncated)
	}
	if e.Response.Headers.Get("Content-Type") != "application/json" || e.Response.Headers.Get(HeaderGateway) != "route=payments" {
		t.Errorf("cabeçalhos da resposta registrados: %v", e.Response.Headers)
	}
	if !bytes.Equal(e.Response.Body, body) || e.Response.Size != int64(len(body)) || e.Response.Truncated {
		t.Errorf("corpo da resposta registrado difere do entregue ao cliente (%d de %d bytes)", len(e.Response.Body), len(body))
	}
	tm := e.Timing
	if tm.TotalMs < 0 || tm.InjectedMs != 0 || tm.UpstreamMs+tm.GatewayMs < tm.TotalMs-0.001 || tm.UpstreamMs+tm.GatewayMs > tm.TotalMs+0.001 {
		t.Errorf("tempos decompostos inconsistentes: %+v", tm)
	}
}

func TestResponseBodyAboveLimitIsTruncated(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789"), 100)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(big) }))
	t.Cleanup(up.Close)
	s := recording()
	s.CaptureMaxBodyBytes = 16
	g := capturing(t, s, route("a", up.URL, "/*"))

	_, body := g.get(t, "/x")
	if !bytes.Equal(body, big) {
		t.Fatalf("o cliente deveria receber o corpo inteiro, recebeu %d bytes", len(body))
	}
	e := g.only(t)
	if !e.Response.Truncated || e.Response.Size != int64(len(big)) || !bytes.Equal(e.Response.Body, big[:16]) {
		t.Fatalf("esperado corpo truncado em 16 bytes com tamanho real %d: truncado %v, tamanho %d, corpo %q",
			len(big), e.Response.Truncated, e.Response.Size, e.Response.Body)
	}
}

func TestRequestBodyAboveLimitIsForwardedWhole(t *testing.T) {
	up := echoUpstream(t, "a")
	s := recording()
	s.CaptureMaxBodyBytes = 8
	g := capturing(t, s, route("a", up.URL, "/*"))

	payload := bytes.Repeat([]byte("abcdefghij"), 500)
	req, _ := http.NewRequest(http.MethodPut, g.URL+"/upload", bytes.NewReader(payload))
	if e := echoOf(t)(do(t, req)); !bytes.Equal(e.Body, payload) {
		t.Fatalf("o upstream deveria receber o corpo inteiro, recebeu %d de %d bytes", len(e.Body), len(payload))
	}
	e := g.only(t)
	if !e.Request.Truncated || e.Request.Size != int64(len(payload)) || string(e.Request.Body) != "abcdefgh" {
		t.Fatalf("corpo da requisição: truncado %v, tamanho %d, corpo %q", e.Request.Truncated, e.Request.Size, e.Request.Body)
	}
}

func TestChunkedRequestBodyCaptured(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	pr, pw := io.Pipe()
	go func() {
		for i := range 3 {
			fmt.Fprintf(pw, "parte %d;", i)
		}
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, g.URL+"/stream", pr)
	if e := echoOf(t)(do(t, req)); string(e.Body) != "parte 0;parte 1;parte 2;" {
		t.Fatalf("corpo sem tamanho declarado alterado: %q", e.Body)
	}
	if e := g.only(t); string(e.Request.Body) != "parte 0;parte 1;parte 2;" || e.Request.Size != 24 || e.Request.Truncated {
		t.Fatalf("corpo sem tamanho declarado registrado como %q (%d bytes, truncado %v)", e.Request.Body, e.Request.Size, e.Request.Truncated)
	}
}

func TestNoRouteExchangeIsRecorded(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/api/*"))
	req, _ := http.NewRequest(http.MethodPost, g.URL+"/outro?x=1", strings.NewReader("corpo"))
	if res, _ := do(t, req); res.StatusCode != http.StatusNotFound {
		t.Fatalf("esperado 404, recebido %d", res.StatusCode)
	}
	e := g.only(t)
	if e.Route != "" || e.Upstream != "" {
		t.Errorf("troca sem rota registrada com rota %q e upstream %q", e.Route, e.Upstream)
	}
	if e.Status != http.StatusNotFound || e.Outcome != exchange.OutcomeGateway || !strings.Contains(e.Error, "no_route") {
		t.Errorf("status %d, resultado %s, erro %q", e.Status, e.Outcome, e.Error)
	}
	if e.Path != "/outro" || e.Query != "x=1" || string(e.Request.Body) != "corpo" {
		t.Errorf("requisição registrada como %s ? %s com corpo %q", e.Path, e.Query, e.Request.Body)
	}
	if !strings.Contains(string(e.Response.Body), "no_route") {
		t.Errorf("o corpo diagnóstico deveria constar da troca: %q", e.Response.Body)
	}
	if e.Timing.UpstreamMs != 0 || e.Timing.InjectedMs != 0 {
		t.Errorf("sem upstream, os tempos de upstream e injetado deveriam ser zero: %+v", e.Timing)
	}
}

func TestGatewayErrorsRecordedAsGateway(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	timeout := config.Duration(100 * time.Millisecond)
	dead := closedAddr(t)
	g := capturing(t, recording(),
		route("vazia", "", "/vazia/*"),
		route("morta", dead, "/morta/*"),
		route("lenta", slow.URL, "/lenta/*", func(r *config.Route) { r.Timeout = &timeout }),
	)
	for _, c := range []struct {
		path, route, upstream, code string
		status                      int
	}{
		{"/vazia/x", "vazia", "", "no_upstream", http.StatusNotImplemented},
		{"/morta/x", "morta", dead, "upstream_unavailable", http.StatusBadGateway},
		{"/lenta/x", "lenta", slow.URL, "upstream_timeout", http.StatusGatewayTimeout},
	} {
		if res, _ := g.get(t, c.path); res.StatusCode != c.status {
			t.Fatalf("%s: esperado %d, recebido %d", c.path, c.status, res.StatusCode)
		}
		items := g.history(t, exchange.Filter{Route: c.route})
		if len(items) != 1 {
			t.Fatalf("%s: esperada uma troca da rota %s, há %d", c.path, c.route, len(items))
		}
		e := items[0]
		if e.Status != c.status || e.Outcome != exchange.OutcomeGateway || !strings.Contains(e.Error, c.code) {
			t.Errorf("%s: status %d, resultado %s, erro %q", c.path, e.Status, e.Outcome, e.Error)
		}
		if e.Upstream != c.upstream || e.Intervened() {
			t.Errorf("%s: upstream %q, intervenções %v", c.path, e.Upstream, e.Interventions)
		}
	}
	// O tempo gasto esperando o upstream lento é tempo de upstream.
	e := g.history(t, exchange.Filter{Route: "lenta"})[0]
	approx(t, "tempo de upstream do 504", e.Timing.UpstreamMs, 90, 2000)
}

// Requirement: Distinção entre resposta do upstream e intervenção do gateway

func TestUpstreamErrorIsNotIntervention(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "falhou", http.StatusInternalServerError)
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	g.get(t, "/x")
	e := g.only(t)
	if e.Status != 500 || e.Outcome != exchange.OutcomeUpstream || e.Intervened() || e.Override != "" || e.Error != "" {
		t.Fatalf("500 do upstream registrado como status %d, resultado %s, intervenções %v, override %q, erro %q",
			e.Status, e.Outcome, e.Interventions, e.Override, e.Error)
	}
}

func TestStreamingStaysIncrementalWhileCaptured(t *testing.T) {
	next := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-next:
			case <-time.After(5 * time.Second):
				return
			}
		}
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("sse", up.URL, "/*"))
	res, err := http.Get(g.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	rd := bufio.NewReader(res.Body)
	for i := range 3 {
		line, err := rd.ReadString('\n')
		if err != nil || line != fmt.Sprintf("data: %d\n", i) {
			t.Fatalf("evento %d não chegou incrementalmente: %q %v", i, line, err)
		}
		rd.ReadString('\n')
		next <- struct{}{}
	}
	io.ReadAll(rd)
	res.Body.Close()
	e := g.only(t)
	if string(e.Response.Body) != "data: 0\n\ndata: 1\n\ndata: 2\n\n" {
		t.Fatalf("o stream capturado difere do entregue: %q", e.Response.Body)
	}
}

// Requirement: Decomposição da latência

func TestInjectedTimeSeparatedFromUpstream(t *testing.T) {
	// Os valores do cenário: atraso de 2s e upstream que responde em 150ms.
	// O atraso entra pelo mesmo ponto de injeção que os overrides usarão
	// (passo 8), aqui acionado por um gancho de teste; a 6.7 repete o
	// cenário com um override real.
	const upstreamDelay, injected = 150 * time.Millisecond, 2 * time.Second
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(upstreamDelay)
		io.WriteString(w, "ok")
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("payments", up.URL, "/*"))
	g.h.delayFor = func(*http.Request) (string, time.Duration) { return "payments/lenta", injected }

	start := time.Now()
	if _, body := g.get(t, "/x"); string(body) != "ok" {
		t.Fatalf("corpo %q", body)
	}
	if el := time.Since(start); el < upstreamDelay+injected {
		t.Fatalf("o atraso deveria somar ao tempo do upstream; a requisição levou %v", el)
	}
	e := g.only(t)
	tm := e.Timing
	approx(t, "tempo injetado", tm.InjectedMs, 2000, 2200)
	approx(t, "tempo de upstream", tm.UpstreamMs, 150, 300)
	if tm.GatewayMs < 0 {
		t.Errorf("overhead negativo: %+v", tm)
	}
	if sum := tm.UpstreamMs + tm.InjectedMs + tm.GatewayMs; sum < tm.TotalMs-0.001 || sum > tm.TotalMs+0.001 {
		t.Errorf("o total deveria ser a soma de upstream, injetado e overhead: %+v", tm)
	}
	if e.Override != "payments/lenta" || !e.Intervened() || e.Outcome != exchange.OutcomeUpstream {
		t.Errorf("o atraso deveria constar como intervenção do override sem mudar o resultado: %q %v %s",
			e.Override, e.Interventions, e.Outcome)
	}
}

func TestNoOverrideMeansZeroInjected(t *testing.T) {
	// O upstream demora o bastante para ser medido mesmo com relógio grosso.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	g.get(t, "/x")
	e := g.only(t)
	if e.Timing.InjectedMs != 0 || e.Intervened() {
		t.Fatalf("sem override o tempo injetado deveria ser zero: %+v %v", e.Timing, e.Interventions)
	}
	if e.Timing.UpstreamMs <= 0 {
		t.Fatalf("a troca encaminhada deveria ter tempo de upstream: %+v", e.Timing)
	}
}

func TestResponseWithoutUpstreamHasZeroUpstreamTime(t *testing.T) {
	g := capturing(t, recording(), route("vazia", "", "/*"))
	g.get(t, "/x")
	if e := g.only(t); e.Timing.UpstreamMs != 0 {
		t.Fatalf("sem contato com o upstream o tempo de upstream deveria ser zero: %+v", e.Timing)
	}
}

// Requirement: Exposição do histórico configurável

func TestRecordingDisabledStillForwards(t *testing.T) {
	up := echoUpstream(t, "a")
	s := recording()
	s.HistoryRecord = false
	g := capturing(t, s, route("a", up.URL, "/api/*"))
	payload := []byte("corpo que precisa chegar")
	for range 5 {
		req, _ := http.NewRequest(http.MethodPost, g.URL+"/api/x", bytes.NewReader(payload))
		if e := echoOf(t)(do(t, req)); !bytes.Equal(e.Body, payload) {
			t.Fatalf("com o registro desligado o encaminhamento deveria seguir igual: %q", e.Body)
		}
	}
	if res, _ := g.get(t, "/fora"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("esperado 404, recebido %d", res.StatusCode)
	}
	if items := g.history(t, exchange.Filter{}); len(items) != 0 {
		t.Fatalf("com o registro desligado nenhuma troca deveria ser registrada, há %d", len(items))
	}
}

func TestClearOnDemand(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	for range 3 {
		g.get(t, "/antes")
	}
	if err := g.rec.Clear(t.Context()); err != nil {
		t.Fatal(err)
	}
	if items := g.history(t, exchange.Filter{}); len(items) != 0 {
		t.Fatalf("depois da limpeza o histórico deveria estar vazio, há %d", len(items))
	}
	g.get(t, "/depois")
	if e := g.only(t); e.Path != "/depois" {
		t.Fatalf("a troca seguinte deveria voltar a ser registrada, registrada %s", e.Path)
	}
}

// failingStore recusa toda gravação; blockingStore trava nela.
type failingStore struct{ store.Store }

func (failingStore) Record(context.Context, *exchange.Exchange) error {
	return errors.New("disco cheio")
}

type blockingStore struct {
	store.Store
	release chan struct{}
}

func (b blockingStore) Record(ctx context.Context, e *exchange.Exchange) error {
	<-b.release
	return b.Store.Record(ctx, e)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRecordFailureOnlyLogs(t *testing.T) {
	up := echoUpstream(t, "a")
	var logs syncBuffer
	rec := capture.NewRecorder(failingStore{store.NewMemory(10)}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(rec.Close)
	srv := httptest.NewServer(NewHandler(liveOf(t, route("a", up.URL, "/*")), rec))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
	if e := echoOf(t)(do(t, req)); e.Upstream != "a" {
		t.Fatalf("a falha de gravação não deveria afetar o encaminhamento")
	}
	rec.Sync(t.Context())
	if !strings.Contains(logs.String(), "disco cheio") {
		t.Fatalf("a falha de gravação deveria ir para o log: %q", logs.String())
	}
}

func TestRecordingNeverBlocksForwarding(t *testing.T) {
	up := echoUpstream(t, "a")
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	hist := store.NewMemory(2 * capture.QueueSize)
	var logs syncBuffer
	rec := capture.NewRecorder(blockingStore{hist, release}, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(rec.Close)
	t.Cleanup(unblock)
	srv := httptest.NewServer(NewHandler(liveOf(t, route("a", up.URL, "/*")), rec))
	t.Cleanup(srv.Close)

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Mais requisições que a fila comporta: as excedentes são descartadas.
		for range capture.QueueSize + 50 {
			res, err := http.Get(srv.URL + "/x")
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("o encaminhamento ficou esperando o histórico")
	}
	// Com a gravação travada, cabem na fila QueueSize trocas, mais a que a
	// goroutine de gravação segura; as demais são descartadas com aviso.
	if !strings.Contains(logs.String(), "fila de gravação do histórico cheia") {
		t.Fatalf("o descarte por fila cheia deveria ir para o log")
	}
	unblock()
	if err := rec.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := hist.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	n := len(res.Items)
	for res.Next != "" {
		if res, err = hist.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit, Cursor: res.Next}); err != nil {
			t.Fatal(err)
		}
		n += len(res.Items)
	}
	if n == 0 || n > capture.QueueSize+1 {
		t.Fatalf("deveriam ter sido gravadas no máximo %d trocas, foram %d", capture.QueueSize+1, n)
	}
}

func TestRecordsIntoCurrentBackend(t *testing.T) {
	up := echoUpstream(t, "a")
	first := store.NewMemory(10)
	hist := store.NewSwitchable("memory", first)
	g := capturingInto(t, hist, recording(), route("a", up.URL, "/*"))
	g.get(t, "/antes")
	g.rec.Sync(t.Context())

	second := store.NewMemory(10)
	if err := hist.Swap(t.Context(), "memory", second); err != nil {
		t.Fatal(err)
	}
	g.get(t, "/depois")
	g.rec.Sync(t.Context())
	res, _ := second.List(t.Context(), exchange.Filter{}, store.Page{})
	if len(res.Items) != 1 || res.Items[0].Path != "/depois" {
		t.Fatalf("a troca seguinte à troca de backend deveria ir para o novo: %v", res.Items)
	}
}

func TestRecordedExchangesArePublished(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	sub := g.rec.Broker().Subscribe(4)
	defer sub.Close()
	g.get(t, "/x")
	select {
	case e := <-sub.C:
		if e.Path != "/x" || e.Route != "a" {
			t.Fatalf("troca publicada difere da registrada: %s %s", e.Route, e.Path)
		}
		if stored := g.only(t); stored.ID != e.ID {
			t.Fatalf("publicada %s, registrada %s", e.ID, stored.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a troca registrada não foi publicada")
	}
}

func TestConcurrentExchangesHaveDistinctIdentity(t *testing.T) {
	up := echoUpstream(t, "a")
	g := capturing(t, recording(), route("a", up.URL, "/*"))
	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			res, err := http.Get(fmt.Sprintf("%s/r/%d", g.URL, i))
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
	ids, seqs := map[string]bool{}, map[uint64]bool{}
	for _, e := range items {
		ids[e.ID], seqs[e.Seq] = true, true
	}
	if len(ids) != n || len(seqs) != n {
		t.Fatalf("identificadores ou sequências repetidos: %d ids e %d sequências distintos", len(ids), len(seqs))
	}
}

// upgradeUpstream aceita um upgrade de protocolo e ecoa as linhas que recebe
// pela conexão trocada.
func upgradeUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "eco" {
			http.Error(w, "upgrade esperado", http.StatusBadRequest)
			return
		}
		conn, brw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: eco\r\nConnection: Upgrade\r\nX-Eco: sim\r\n\r\n")
		brw.Flush()
		for {
			line, err := brw.ReadString('\n')
			if err != nil {
				return
			}
			brw.WriteString("eco: " + line)
			brw.Flush()
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// Um upgrade de protocolo é registrado com o 101 e os cabeçalhos da resposta
// do upstream, embora o ReverseProxy escreva essa resposta direto na conexão
// sequestrada.
func TestUpgradeExchangeRecordedWith101(t *testing.T) {
	up := upgradeUpstream(t)
	g := capturing(t, recording(), route("ws", up.URL, "/*"))

	conn, err := net.Dial("tcp", g.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /canal HTTP/1.1\r\nHost: gw.local\r\nUpgrade: eco\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("o cliente deveria receber 101, recebeu %d", res.StatusCode)
	}
	io.WriteString(conn, "olá\n")
	if line, err := br.ReadString('\n'); err != nil || line != "eco: olá\n" {
		t.Fatalf("a conexão trocada deveria ecoar: %q %v", line, err)
	}
	conn.Close()

	// O registro fecha quando o túnel termina, depois do fim da conexão.
	if err := g.rec.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	e := g.only(t)
	if e.Status != http.StatusSwitchingProtocols || e.Outcome != exchange.OutcomeUpstream {
		t.Fatalf("upgrade registrado com status %d e resultado %s", e.Status, e.Outcome)
	}
	h := e.Response.Headers
	if h.Get("Upgrade") != "eco" || h.Get("X-Eco") != "sim" || h.Get(HeaderGateway) != "route=ws" {
		t.Fatalf("cabeçalhos da resposta do upgrade registrados: %v", h)
	}
}

// Numa resposta do próprio gateway, um corpo sem Content-Length maior que o
// limite de captura fica com o tamanho real.
func TestNoRouteChunkedBodyKeepsRealSize(t *testing.T) {
	s := recording()
	s.CaptureMaxBodyBytes = 8
	g := capturing(t, s, route("a", "http://127.0.0.1:1", "/api/*"))
	payload := bytes.Repeat([]byte("abcdefghij"), 300)
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < len(payload); i += 100 {
			pw.Write(payload[i : i+100])
		}
		pw.Close()
	}()
	req, _ := http.NewRequest(http.MethodPost, g.URL+"/fora", pr)
	if res, _ := do(t, req); res.StatusCode != http.StatusNotFound {
		t.Fatalf("esperado 404, recebido %d", res.StatusCode)
	}
	e := g.only(t)
	if e.Request.Size != int64(len(payload)) || !e.Request.Truncated || string(e.Request.Body) != "abcdefgh" {
		t.Fatalf("corpo da requisição: tamanho %d (esperado %d), truncado %v, corpo %q",
			e.Request.Size, len(payload), e.Request.Truncated, e.Request.Body)
	}
}
