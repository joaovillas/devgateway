package proxy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/capture"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
)

// Os cenários de consulta, leitura e navegação correm sobre trocas reais,
// capturadas pelo proxy, e não sobre trocas montadas à mão.

// statusUpstream responde com o status pedido em ?status=, 200 por padrão.
func statusUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code := http.StatusOK
		if v := r.URL.Query().Get("status"); v != "" {
			code, _ = strconv.Atoi(v)
		}
		w.WriteHeader(code)
		io.WriteString(w, "resposta de "+r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

// historyGateway monta duas rotas, payments e orders, e atrasa com um
// override de teste as requisições cujo path contém "/lenta".
func historyGateway(t *testing.T) *capGW {
	t.Helper()
	up := statusUpstream(t)
	g := capturing(t, recording(),
		route("payments", up.URL, "/api/payments/*"),
		route("orders", up.URL, "/api/orders/*"),
	)
	g.h.delayFor = func(r *http.Request) (string, time.Duration) {
		if strings.Contains(r.URL.Path, "/lenta") {
			return "payments/lenta", time.Millisecond
		}
		return "", 0
	}
	return g
}

// Requirement: Consulta e filtragem do histórico

func TestHistoryFilterByRoute(t *testing.T) {
	g := historyGateway(t)
	for i := range 3 {
		g.get(t, fmt.Sprintf("/api/payments/%d", i))
		g.get(t, fmt.Sprintf("/api/orders/%d", i))
	}
	items := g.history(t, exchange.Filter{Route: "payments"})
	if len(items) != 3 {
		t.Fatalf("esperadas 3 trocas de payments, há %d", len(items))
	}
	for _, e := range items {
		if e.Route != "payments" {
			t.Fatalf("troca da rota %s no filtro por payments", e.Route)
		}
	}
}

func TestHistoryFilterByStatusRange(t *testing.T) {
	g := historyGateway(t)
	for _, code := range []int{200, 404, 500, 503, 599, 201} {
		g.get(t, fmt.Sprintf("/api/orders/x?status=%d", code))
	}
	g.get(t, "/sem-rota") // 404 do próprio gateway
	items := g.history(t, exchange.Filter{StatusMin: 500, StatusMax: 599})
	var got []int
	for _, e := range items {
		got = append(got, e.Status)
	}
	if fmt.Sprint(got) != "[599 503 500]" {
		t.Fatalf("esperados os status 5xx do mais novo ao mais antigo, recebidos %v", got)
	}
}

// synthesize registra no mesmo histórico uma troca respondida por override,
// pelo caminho da captura. A síntese em si é da fase de overrides (6.4); a
// 6.10 repete este filtro com uma síntese real.
func (g *capGW) synthesize(t *testing.T, path, override string, status int) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := g.rec.Begin(httptest.NewRecorder(), req, capture.Options{Record: true, MaxBodyBytes: 1024})
	rec.SetRoute("payments", "")
	rec.Intervene(override, "synthesized")
	rec.Writer().WriteHeader(status)
	rec.Finish()
}

func TestHistoryFilterByIntervention(t *testing.T) {
	g := historyGateway(t)
	g.get(t, "/api/payments/normal")
	g.get(t, "/api/payments/lenta")
	g.synthesize(t, "/api/payments/sintetizada", "payments/flaky", http.StatusServiceUnavailable)
	g.get(t, "/api/orders/normal?status=500")
	yes, no := true, false
	with := g.history(t, exchange.Filter{Intervened: &yes})
	if len(with) != 2 {
		t.Fatalf("o filtro por intervenção deveria trazer a troca sintetizada e a atrasada: %v", with)
	}
	synth, delayed := with[0], with[1]
	if synth.Path != "/api/payments/sintetizada" || synth.Outcome != exchange.OutcomeSynthesized || synth.Override != "payments/flaky" {
		t.Fatalf("a troca sintetizada deveria constar do filtro: %+v", synth)
	}
	if delayed.Path != "/api/payments/lenta" || delayed.Override != "payments/lenta" {
		t.Fatalf("a troca atrasada deveria constar do filtro: %+v", delayed)
	}
	if without := g.history(t, exchange.Filter{Intervened: &no}); len(without) != 2 {
		t.Fatalf("esperadas 2 trocas sem intervenção, há %d", len(without))
	}
}

// A ordem do histórico é a de chegada: uma troca lenta que chegou antes e
// terminou depois não passa à frente das que chegaram depois dela.
func TestHistoryOrderFollowsArrivalNotCompletion(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lenta" {
			close(arrived)
			<-release
		}
		io.WriteString(w, r.URL.Path)
	}))
	t.Cleanup(up.Close)
	g := capturing(t, recording(), route("a", up.URL, "/*"))

	slow := make(chan struct{})
	go func() {
		defer close(slow)
		res, err := http.Get(g.URL + "/lenta")
		if err != nil {
			t.Error(err)
			return
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
	}()
	<-arrived
	g.get(t, "/rapida")
	g.rec.Sync(t.Context())
	close(release)
	<-slow

	items := g.history(t, exchange.Filter{})
	if len(items) != 2 || items[0].Path != "/rapida" || items[1].Path != "/lenta" {
		var got []string
		for _, e := range items {
			got = append(got, e.Path)
		}
		t.Fatalf("da mais nova para a mais antiga pela chegada, esperado [/rapida /lenta], recebido %v", got)
	}
	if items[0].Start.Before(items[1].Start) || items[1].Seq > items[0].Seq {
		t.Fatalf("a lenta chegou antes: início %v e %v, sequência %d e %d", items[1].Start, items[0].Start, items[1].Seq, items[0].Seq)
	}
	next, err := g.hist.Neighbor(t.Context(), items[1].ID, store.Newer, exchange.Filter{})
	if err != nil || next.Path != "/rapida" {
		t.Fatalf("a seguinte da lenta deveria ser a rápida: %s %v", next.Path, err)
	}
}

func TestHistoryCombinedFilters(t *testing.T) {
	g := historyGateway(t)
	g.get(t, "/api/payments/a?status=500")
	g.get(t, "/api/payments/b?status=200")
	g.get(t, "/api/orders/c?status=500")
	items := g.history(t, exchange.Filter{Route: "payments", StatusMin: 500, StatusMax: 599, Method: "get"})
	if len(items) != 1 || items[0].Path != "/api/payments/a" {
		t.Fatalf("filtros combinados deveriam valer em conjunto: %v", items)
	}
}

func TestHistoryOrderAndPagination(t *testing.T) {
	g := historyGateway(t)
	for i := range 150 {
		g.get(t, fmt.Sprintf("/api/orders/%d", i))
	}
	g.rec.Sync(t.Context())
	page, err := g.hist.List(t.Context(), exchange.Filter{}, store.Page{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 50 || page.Next == "" {
		t.Fatalf("esperada uma página de 50 com continuação, recebidas %d (next %q)", len(page.Items), page.Next)
	}
	for i, e := range page.Items {
		if want := fmt.Sprintf("/api/orders/%d", 149-i); e.Path != want {
			t.Fatalf("item %d = %s, esperado %s (da mais nova para a mais antiga)", i, e.Path, want)
		}
	}
}

// Requirement: Leitura individual e navegação por cursor

func TestHistoryGetByID(t *testing.T) {
	g := historyGateway(t)
	g.get(t, "/api/payments/lenta")
	items := g.history(t, exchange.Filter{})
	e, err := g.hist.Get(t.Context(), items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID != items[0].ID || e.Method != http.MethodGet || e.Status != 200 {
		t.Fatalf("troca recuperada incompleta: %+v", e)
	}
	if string(e.Response.Body) != "resposta de /api/payments/lenta" || e.Response.Headers.Get(HeaderGateway) == "" {
		t.Fatalf("a resposta deveria vir completa: %q %v", e.Response.Body, e.Response.Headers)
	}
	if tm := e.Timing; tm.InjectedMs <= 0 || tm.UpstreamMs+tm.InjectedMs+tm.GatewayMs < tm.TotalMs-0.001 {
		t.Fatalf("os tempos decompostos deveriam vir preenchidos: %+v", e.Timing)
	}
}

func TestHistoryGetMissingID(t *testing.T) {
	g := historyGateway(t)
	g.get(t, "/api/payments/x")
	g.rec.Sync(t.Context())
	if _, err := g.hist.Get(t.Context(), exchange.NewID(time.Now())); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("esperado ErrNotFound, recebido %v", err)
	}
}

func TestHistoryNavigateItemByItem(t *testing.T) {
	g := historyGateway(t)
	for i := range 3 {
		g.get(t, fmt.Sprintf("/api/orders/%d", i))
	}
	items := g.history(t, exchange.Filter{}) // 2, 1, 0
	next, err := g.hist.Neighbor(t.Context(), items[2].ID, store.Newer, exchange.Filter{})
	if err != nil || next.ID != items[1].ID {
		t.Fatalf("a seguinte de /0 deveria ser /1: %s %v", next.Path, err)
	}
	if _, err := g.hist.Neighbor(t.Context(), items[0].ID, store.Newer, exchange.Filter{}); !errors.Is(err, store.ErrNoMore) {
		t.Fatalf("depois da mais nova não há mais itens: %v", err)
	}
	prev, err := g.hist.Neighbor(t.Context(), items[1].ID, store.Older, exchange.Filter{})
	if err != nil || prev.ID != items[2].ID {
		t.Fatalf("a anterior de /1 deveria ser /0: %s %v", prev.Path, err)
	}
}

func TestHistoryNavigationRespectsFilter(t *testing.T) {
	g := historyGateway(t)
	for _, code := range []int{500, 200, 201, 503, 200} {
		g.get(t, fmt.Sprintf("/api/orders/%d?status=%d", code, code))
	}
	items := g.history(t, exchange.Filter{}) // 200, 503, 201, 200, 500
	f := exchange.Filter{StatusMin: 500, StatusMax: 599}
	first := items[4]
	next, err := g.hist.Neighbor(t.Context(), first.ID, store.Newer, f)
	if err != nil || next.Status != 503 {
		t.Fatalf("a seguinte com filtro 5xx deveria pular os 2xx e chegar ao 503: %d %v", next.Status, err)
	}
	if _, err := g.hist.Neighbor(t.Context(), next.ID, store.Newer, f); !errors.Is(err, store.ErrNoMore) {
		t.Fatalf("não há outra troca 5xx mais nova: %v", err)
	}
}
