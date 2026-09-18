package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/config/writer"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/override"
)

const (
	// DefaultHeartbeat é o intervalo padrão do heartbeat do fluxo de eventos.
	// O cliente considera a conexão perdida depois de três intervalos sem
	// nenhum evento.
	DefaultHeartbeat = 15 * time.Second
	// aggregateEvery é o intervalo de agregação das trocas novas e do estado
	// vivo dos overrides: no máximo uma atualização de cada por intervalo.
	aggregateEvery = time.Second
	// maxPerEvent limita as trocas de um evento exchanges; as demais do
	// intervalo são contadas em dropped.
	maxPerEvent = 200
	// streamBuffer é quantas trocas uma conexão acumula entre duas leituras
	// do broker. Com o buffer cheio a troca é contada como perdida: a
	// publicação nunca espera por uma conexão.
	streamBuffer = 1024
	// hubBuffer é quantos eventos de configuração e histórico uma conexão
	// acumula enquanto escreve.
	hubBuffer = 64
	// writeTimeout limita cada escrita no fluxo. Um cliente que não lê nesse
	// prazo é desconectado e reconecta quando puder.
	writeTimeout = 10 * time.Second
)

func (h *Handler) eventRoutes() {
	h.handle("/api/events", map[string]http.HandlerFunc{
		"GET": h.stream,
	})
	h.handle("/api/status", map[string]http.HandlerFunc{
		"GET": h.getStatus,
	})
}

// streamKey guarda no contexto das requisições o sinal de que o servidor
// saiu de serviço.
type streamKey struct{}

// StreamContext acrescenta a ctx o sinal stop, fechado quando o servidor que
// atende a requisição sai de serviço (encerramento ou troca de porta). O
// fluxo de eventos termina ao recebê-lo, em vez de segurar o Shutdown
// gracioso do servidor indefinidamente; o cliente reconecta.
func StreamContext(ctx context.Context, stop <-chan struct{}) context.Context {
	return context.WithValue(ctx, streamKey{}, stop)
}

func streamStop(ctx context.Context) <-chan struct{} {
	stop, _ := ctx.Value(streamKey{}).(<-chan struct{})
	return stop // nil: nunca é sinalizado
}

// event é um evento pronto para o fluxo, com os dados já serializados.
type event struct {
	name string
	data []byte
}

// hub distribui os eventos de configuração e de histórico às conexões
// abertas. A publicação nunca espera: uma conexão com o buffer cheio perde o
// evento, como qualquer evento perdido numa desconexão, e o cliente recarrega
// por REST o que exibe.
type hub struct {
	mu   sync.Mutex
	subs map[chan event]struct{}
}

func newHub() *hub { return &hub{subs: map[chan event]struct{}{}} }

func (h *hub) subscribe() chan event {
	c := make(chan event, hubBuffer)
	h.mu.Lock()
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *hub) unsubscribe(c chan event) {
	h.mu.Lock()
	delete(h.subs, c)
	h.mu.Unlock()
}

func (h *hub) publish(name string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	ev := event{name, data}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- ev:
		default:
		}
	}
}

// configEvent é o evento de uma alteração de configuração aplicada.
type configEvent struct {
	// Cause é api, reload ou learning.
	Cause    string   `json:"cause"`
	Routes   []string `json:"routes"`
	Settings []string `json:"settings"`
}

// historyEvent é o evento de limpeza do histórico ou de troca de backend.
type historyEvent struct {
	// Cause é cleared ou backend.
	Cause   string `json:"cause"`
	Backend string `json:"backend"`
}

// configChanged publica toda alteração aplicada pelo Writer — da API, da
// recarga ou do aprendizado.
func (h *Handler) configChanged(c writer.Change) {
	routes, settings := c.Routes, c.Settings
	if routes == nil {
		routes = []string{}
	}
	if settings == nil {
		settings = []string{}
	}
	h.events.publish("config", configEvent{Cause: c.Cause, Routes: routes, Settings: settings})
}

// exchangesEvent agrega as trocas registradas num intervalo.
type exchangesEvent struct {
	Items   []exchange.Exchange `json:"items"`
	Dropped uint64              `json:"dropped"`
}

type overridesEvent struct {
	Now   time.Time            `json:"now"`
	Items []override.LiveState `json:"items"`
}

type heartbeatEvent struct {
	Now time.Time `json:"now"`
}

// stream atende o fluxo text/event-stream do painel: hello ao conectar; as
// trocas novas agregadas a no máximo uma atualização por segundo; as
// alterações de configuração e do histórico; o estado vivo dos overrides
// quando muda de forma não contínua, também agregado por segundo; e um
// heartbeat periódico, que mantém a conexão viva sem tráfego.
//
// Nada aqui bloqueia o proxy: as trocas chegam pelo broker, que nunca espera
// por um assinante, e cada escrita tem prazo, de modo que um cliente lento
// perde eventos (contados em dropped) e, parado, é desconectado.
func (h *Handler) stream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// O prazo de escrita vale para a conexão: sem zerá-lo na saída, uma
	// requisição seguinte na mesma conexão herdaria o prazo vencido.
	defer rc.SetWriteDeadline(time.Time{})
	hdr := w.Header()
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	sub := h.rec.Broker().Subscribe(streamBuffer)
	defer sub.Close()
	evs := h.events.subscribe()
	defer h.events.unsubscribe(evs)

	var buf bytes.Buffer
	send := func(name string, data []byte) bool {
		buf.Reset()
		fmt.Fprintf(&buf, "event: %s\ndata: %s\n\n", name, data)
		rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := w.Write(buf.Bytes()); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	sendJSON := func(name string, v any) bool {
		data, err := json.Marshal(v)
		return err == nil && send(name, data)
	}

	if !sendJSON("hello", h.status()) {
		return
	}
	lastStates := statesKey(h.overrides.States())

	tick := time.NewTicker(aggregateEvery)
	defer tick.Stop()
	beat := time.NewTicker(h.heartbeat)
	defer beat.Stop()
	stop := streamStop(r.Context())
	var pending []exchange.Exchange
	var dropped uint64
	for {
		select {
		case <-r.Context().Done():
			return
		case <-stop:
			return
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			if len(pending) == maxPerEvent {
				// Ficam as mais novas; as mais antigas do intervalo são
				// contadas como perdidas.
				pending = append(pending[:0], pending[1:]...)
				dropped++
			}
			pending = append(pending, e.Summary())
		case ev := <-evs:
			if !send(ev.name, ev.data) {
				return
			}
		case <-tick.C:
			dropped += sub.TakeDropped()
			if len(pending) > 0 || dropped > 0 {
				// Com a exposição do histórico desligada, as trocas não
				// saem pelo fluxo.
				if h.live.Load().Settings.HistoryExpose {
					if !sendJSON("exchanges", exchangesEvent{Items: pending, Dropped: dropped}) {
						return
					}
				}
				pending, dropped = nil, 0
			}
			states, now := h.overrides.States()
			if key := statesKey(states, now); key != lastStates {
				lastStates = key
				if !sendJSON("overrides", overridesEvent{Now: now, Items: states}) {
					return
				}
			}
		case <-beat.C:
			if !sendJSON("heartbeat", heartbeatEvent{Now: h.now().UTC()}) {
				return
			}
		}
	}
}

// statesKey resume o estado vivo dos overrides sem o restante do tempo de
// vida, que muda continuamente e o painel decrementa por conta própria: o
// evento overrides só sai quando algo muda de forma não contínua — um
// override surge, some, expira, é reativado, liga, desliga ou é aplicado.
func statesKey(states []override.LiveState, _ time.Time) string {
	var b bytes.Buffer
	for _, s := range states {
		fmt.Fprintf(&b, "%s/%s:%t:%t:%v:%d:%d", s.Route, s.Override, s.Enabled, s.Active,
			s.RegisteredAt.UnixNano(), s.Applications, ptrVal(s.MaxApplications))
		if s.Expired != nil {
			b.WriteString(":" + *s.Expired)
		}
		if s.TTLRemainingMs == nil {
			b.WriteString(":nottl")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func ptrVal(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// statusBody é o estado resumido do processo, também enviado no hello.
type statusBody struct {
	Version       string        `json:"version"`
	SchemaVersion int           `json:"schemaVersion"`
	StartedAt     time.Time     `json:"startedAt"`
	ConfigPath    string        `json:"configPath"`
	RoutesDir     string        `json:"routesDir"`
	Ports         statusPorts   `json:"ports"`
	History       statusHistory `json:"history"`
	Learning      statusLearn   `json:"learning"`
	Routes        int           `json:"routes"`
}

type statusPorts struct {
	Traffic int `json:"traffic"`
	Admin   int `json:"admin"`
}

type statusHistory struct {
	Backend string `json:"backend"`
	Record  bool   `json:"record"`
	Expose  bool   `json:"expose"`
}

type statusLearn struct {
	Enabled bool `json:"enabled"`
}

func (h *Handler) status() statusBody {
	snap := h.live.Load()
	s := snap.Settings
	b := statusBody{
		Version:       Version,
		SchemaVersion: config.SchemaVersion,
		StartedAt:     h.startedAt.UTC(),
		ConfigPath:    h.loader.ConfigPath,
		RoutesDir:     s.RoutesDir,
		Ports:         statusPorts{Traffic: s.TrafficPort, Admin: s.AdminPort},
		History:       statusHistory{Backend: h.history.Backend(), Record: s.HistoryRecord, Expose: s.HistoryExpose},
		Learning:      statusLearn{Enabled: s.LearningEnabled},
		Routes:        len(snap.Routes),
	}
	if h.ports != nil {
		b.Ports.Traffic, b.Ports.Admin = h.ports()
	}
	return b
}

// getStatus devolve o estado resumido do processo.
func (h *Handler) getStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.status())
}
