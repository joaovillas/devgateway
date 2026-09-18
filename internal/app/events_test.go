package app

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/exchange"
)

// Fluxo SSE da API de administração (Requirement: Atualização em tempo real).

type result struct {
	status int
	body   string
	err    error
}

func fetch(url string) result {
	res, err := http.Get(url)
	if err != nil {
		return result{err: err}
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	return result{status: res.StatusCode, body: string(b), err: err}
}

// sseEvent é um evento recebido, com o instante da chegada.
type sseEvent struct {
	name string
	data string
	at   time.Time
}

// sseStream lê um fluxo de eventos numa goroutine.
type sseStream struct {
	res    *http.Response
	events chan sseEvent
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

func openEvents(t *testing.T, url string) *sseStream {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("fluxo de eventos: %d %v", res.StatusCode, res.Header)
	}
	s := &sseStream{res: res, events: make(chan sseEvent, 1024), done: make(chan struct{})}
	t.Cleanup(func() { res.Body.Close() })
	go func() {
		defer close(s.done)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			case line == "" && ev.name != "":
				ev.at = time.Now()
				s.events <- ev
				ev = sseEvent{}
			}
		}
		s.mu.Lock()
		s.err = sc.Err()
		s.mu.Unlock()
	}()
	return s
}

// next espera o próximo evento de nome dado, descartando os demais.
func (s *sseStream) next(t *testing.T, name string) sseEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev := <-s.events:
			if ev.name == name {
				return ev
			}
		case <-s.done:
			t.Fatalf("o fluxo terminou esperando %q", name)
		case <-timeout:
			t.Fatalf("nenhum evento %q chegou", name)
		}
	}
}

// closed espera o fim do fluxo pelo servidor.
func (s *sseStream) closed(t *testing.T) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("o fluxo deveria ter terminado")
	}
}

type exchangesData struct {
	Items   []exchange.Exchange `json:"items"`
	Dropped uint64              `json:"dropped"`
}

func (ev sseEvent) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(ev.data), v); err != nil {
		t.Fatalf("evento %s com dados inválidos: %v: %s", ev.name, err, ev.data)
	}
}

// collect junta as trocas dos eventos exchanges até somar n, devolvendo os
// eventos recebidos.
func (s *sseStream) collect(t *testing.T, n int) ([]exchange.Exchange, []sseEvent) {
	t.Helper()
	var items []exchange.Exchange
	var evs []sseEvent
	for len(items) < n {
		ev := s.next(t, "exchanges")
		var d exchangesData
		ev.decode(t, &d)
		if d.Dropped != 0 {
			t.Fatalf("nenhuma troca deveria ser perdida: %+v", d)
		}
		items = append(items, d.Items...)
		evs = append(evs, ev)
	}
	return items, evs
}

// Scenario: Nova troca aparece sozinha — o cliente conectado recebe as trocas
// novas, em ordem, sem corpos.

func TestEventsDeliverExchangesInOrder(t *testing.T) {
	e := startAdmin(t, freePorts, statusRoutes(t))
	s := openEvents(t, e.api+"/events")
	var hello struct {
		Version string
		Ports   struct{ Traffic, Admin int }
		History struct{ Backend string }
		Routes  int
	}
	s.next(t, "hello").decode(t, &hello)
	if hello.Version == "" || hello.Routes != 2 || hello.History.Backend != "memory" ||
		!strings.HasSuffix(e.TrafficAddr(), fmt.Sprintf(":%d", hello.Ports.Traffic)) {
		t.Fatalf("hello: %+v", hello)
	}

	for i := range 5 {
		getBody(t, fmt.Sprintf("%s/payments/%d", e.traffic, i))
	}
	items, _ := s.collect(t, 5)
	if len(items) != 5 {
		t.Fatalf("esperadas 5 trocas, vieram %d", len(items))
	}
	for i, it := range items {
		if it.Path != fmt.Sprintf("/payments/%d", i) || it.Status != 200 || it.Response.Body != nil || it.ID == "" {
			t.Fatalf("troca %d fora de ordem ou com corpo: %+v", i, it)
		}
	}
}

// As trocas são agregadas: no máximo uma atualização por segundo.

func TestEventsAggregatedPerSecond(t *testing.T) {
	e := startAdmin(t, freePorts, statusRoutes(t))
	s := openEvents(t, e.api+"/events")
	s.next(t, "hello")
	const n = 30
	for i := range n {
		getBody(t, fmt.Sprintf("%s/payments/%d", e.traffic, i))
		time.Sleep(80 * time.Millisecond)
	}
	items, evs := s.collect(t, n)
	for i, it := range items {
		if it.Path != fmt.Sprintf("/payments/%d", i) {
			t.Fatalf("troca %d fora de ordem: %s", i, it.Path)
		}
	}
	if len(evs) < 2 || len(evs) > 5 {
		t.Fatalf("2,4 s de tráfego deveriam render de 2 a 5 atualizações agregadas, vieram %d", len(evs))
	}
	for i := 1; i < len(evs); i++ {
		if gap := evs[i].at.Sub(evs[i-1].at); gap < 800*time.Millisecond {
			t.Fatalf("atualizações com %v de intervalo; o mínimo é um segundo", gap)
		}
	}
}

// A conexão sobrevive a períodos sem tráfego: o heartbeat a mantém viva e a
// troca seguinte chega por ela.

func TestEventsSurviveIdlePeriods(t *testing.T) {
	e := startAdminWith(t, freePorts, statusRoutes(t), Options{Heartbeat: 200 * time.Millisecond})
	s := openEvents(t, e.api+"/events")
	s.next(t, "hello")
	start := time.Now()
	beats := 0
	for time.Since(start) < 2*time.Second {
		var hb struct{ Now time.Time }
		s.next(t, "heartbeat").decode(t, &hb)
		if hb.Now.IsZero() {
			t.Fatal("heartbeat sem instante")
		}
		beats++
	}
	if beats < 5 {
		t.Fatalf("esperados heartbeats a cada 200 ms, vieram %d em 2 s", beats)
	}
	getBody(t, e.traffic+"/payments/depois")
	if items, _ := s.collect(t, 1); items[0].Path != "/payments/depois" {
		t.Fatalf("a troca depois do silêncio deveria chegar: %+v", items)
	}
}

// Um cliente que não lê o fluxo não bloqueia o proxy.

func TestEventsSlowClientDoesNotBlockProxy(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{"payments.yaml": routeDoc("payments", up.URL, "/payments/*")})

	// Um cliente que pede o fluxo e nunca lê nada.
	conn, err := net.Dial("tcp", e.AdminAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.(*net.TCPConn).SetReadBuffer(512)
	fmt.Fprintf(conn, "GET /api/events HTTP/1.1\r\nHost: %s\r\n\r\n", e.AdminAddr())
	waitSubscribers(t, e, 1)

	// Cabeçalhos grandes engordam cada resumo de troca no fluxo.
	big := strings.Repeat("x", 4000)
	start := time.Now()
	var wg sync.WaitGroup
	var failed atomic.Int64
	for w := range 8 {
		wg.Go(func() {
			for i := range 100 {
				req, _ := http.NewRequest("GET", fmt.Sprintf("%s/payments/%d/%d", e.traffic, w, i), nil)
				req.Header.Set("X-Grande", big)
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					failed.Add(1)
					continue
				}
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
				if res.StatusCode != 200 {
					failed.Add(1)
				}
			}
		})
	}
	wg.Wait()
	if failed.Load() != 0 || hits.Load() != 800 {
		t.Fatalf("todas as requisições deveriam ser atendidas: %d falhas, %d no upstream", failed.Load(), hits.Load())
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("o proxy não deveria esperar pelo cliente lento: %v", d)
	}
}

func waitSubscribers(t *testing.T, e *adminEnv, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for e.Recorder.Broker().Subscribers() < n {
		if time.Now().After(deadline) {
			t.Fatalf("esperadas %d assinaturas do fluxo", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Alterações de configuração, limpeza e troca de backend do histórico e o
// estado vivo dos overrides também chegam pelo fluxo.

func TestEventsConfigHistoryAndOverrides(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	s := openEvents(t, e.api+"/events")
	s.next(t, "hello")

	type configData struct {
		Cause    string   `json:"cause"`
		Routes   []string `json:"routes"`
		Settings []string `json:"settings"`
	}
	r := e.call(t, "POST", "/routes/payments/overrides", "",
		`{"name":"once","match":{"path":"/payments/x"},"respond":{"status":418},"maxApplications":1}`)
	if r.status != 201 {
		t.Fatalf("criação do override: %d %s", r.status, r.body)
	}
	var c configData
	s.next(t, "config").decode(t, &c)
	if c.Cause != "api" || len(c.Routes) != 1 || c.Routes[0] != "payments" || c.Settings == nil {
		t.Fatalf("evento config da escrita: %+v", c)
	}

	// O override é aplicado e expira pela contagem.
	if st, _ := getBody(t, e.traffic+"/payments/x"); st != 418 {
		t.Fatalf("o override deveria sintetizar: %d", st)
	}
	type overridesData struct {
		Now   time.Time `json:"now"`
		Items []struct {
			Route        string  `json:"route"`
			Override     string  `json:"override"`
			Applications int64   `json:"applications"`
			Expired      *string `json:"expired"`
		} `json:"items"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var o overridesData
		s.next(t, "overrides").decode(t, &o)
		if len(o.Items) == 1 && o.Items[0].Applications == 1 && o.Items[0].Expired != nil && *o.Items[0].Expired == "applications" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("o estado vivo deveria mostrar a expiração: %+v", o)
		}
	}

	if r := e.patchSettings(t, `{"seed":5}`); r.status != 200 {
		t.Fatalf("alteração do seed: %d %s", r.status, r.body)
	}
	c = configData{}
	s.next(t, "config").decode(t, &c)
	if c.Cause != "api" || len(c.Settings) != 1 || c.Settings[0] != "seed" {
		t.Fatalf("evento config da configuração do processo: %+v", c)
	}

	if r := e.call(t, "DELETE", "/exchanges", "", ""); r.status != 204 {
		t.Fatalf("limpeza: %d", r.status)
	}
	var h struct{ Cause, Backend string }
	s.next(t, "history").decode(t, &h)
	if h.Cause != "cleared" || h.Backend != "memory" {
		t.Fatalf("evento history da limpeza: %+v", h)
	}
	if r := e.patchSettings(t, `{"history":{"backend":"ndjson","path":"h.ndjson"}}`); r.status != 200 {
		t.Fatalf("troca de backend: %d %s", r.status, r.body)
	}
	h.Cause, h.Backend = "", ""
	s.next(t, "history").decode(t, &h)
	if h.Cause != "backend" || h.Backend != "ndjson" {
		t.Fatalf("evento history da troca de backend: %+v", h)
	}

	if r := e.call(t, "POST", "/reload", "", ""); r.status != 200 {
		t.Fatalf("recarga: %d", r.status)
	}
	c = configData{}
	s.next(t, "config").decode(t, &c)
	if c.Cause != "reload" {
		t.Fatalf("evento config da recarga: %+v", c)
	}
}

// Com a exposição do histórico desligada, o fluxo não entrega trocas.

func TestEventsWithoutExposureOmitExchanges(t *testing.T) {
	e := startAdminWith(t, `{"ports":{"traffic":0,"admin":0},"history":{"expose":false}}`, statusRoutes(t),
		Options{Heartbeat: 300 * time.Millisecond})
	s := openEvents(t, e.api+"/events")
	s.next(t, "hello")
	getBody(t, e.traffic+"/payments/x")
	deadline := time.After(2500 * time.Millisecond)
	for {
		select {
		case ev := <-s.events:
			if ev.name == "exchanges" {
				t.Fatalf("nenhuma troca deveria sair com a exposição desligada: %s", ev.data)
			}
		case <-deadline:
			return
		}
	}
}

// Um fluxo aberto não segura o encerramento do processo.

func TestShutdownWithOpenEventStream(t *testing.T) {
	e := startAdmin(t, freePorts, nil)
	s := openEvents(t, e.api+"/events")
	s.next(t, "hello")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := e.Shutdown(ctx); err != nil {
		t.Fatalf("encerramento: %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("o encerramento esperou o fluxo aberto: %v", d)
	}
	s.closed(t)
}
