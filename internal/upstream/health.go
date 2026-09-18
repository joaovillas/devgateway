// Package upstream acompanha a disponibilidade recente de cada upstream a
// partir das tentativas de encaminhamento da porta de tráfego. O gateway não
// sonda os upstreams: o estado vem só do caminho das requisições, e é
// contado independentemente do registro do histórico.
package upstream

import (
	"sync"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
)

const (
	// Window é quantas tentativas recentes cada upstream guarda.
	Window = 20
	// DownAfter é quantas falhas seguidas, as mais recentes, dão o upstream
	// como indisponível.
	DownAfter = 3
)

// Status é a disponibilidade de um upstream.
type Status string

const (
	// StatusUp: houve tentativa e as DownAfter mais recentes não falharam
	// todas (com menos de DownAfter tentativas, sempre este).
	StatusUp Status = "up"
	// StatusDown: as DownAfter tentativas mais recentes ficaram sem resposta.
	StatusDown Status = "down"
	// StatusUnknown: nenhuma tentativa desde o início, ou desde que o
	// upstream passou a ser declarado.
	StatusUnknown Status = "unknown"
)

// Recent resume as últimas tentativas.
type Recent struct {
	Attempts int `json:"attempts"`
	Failures int `json:"failures"`
}

// Item é a disponibilidade de um upstream, como GET /api/upstreams a devolve.
type Item struct {
	Upstream      string     `json:"upstream"`
	Routes        []string   `json:"routes"`
	Status        Status     `json:"status"`
	Recent        Recent     `json:"recent"`
	LastSuccessAt *time.Time `json:"lastSuccessAt"`
	LastFailureAt *time.Time `json:"lastFailureAt"`
	LastError     string     `json:"lastError"`
}

// record guarda as tentativas recentes de um upstream num anel.
type record struct {
	failed      [Window]bool
	n, next     int
	lastSuccess time.Time
	lastFailure time.Time
	lastError   string
}

func (r *record) add(failed bool) {
	r.failed[r.next] = failed
	r.next = (r.next + 1) % Window
	if r.n < Window {
		r.n++
	}
}

// status aplica a regra: sem tentativa, desconhecido; as DownAfter mais
// recentes sem resposta, indisponível; senão, respondendo.
func (r *record) status() Status {
	if r == nil || r.n == 0 {
		return StatusUnknown
	}
	if r.n < DownAfter {
		// Poucas tentativas para concluir indisponibilidade; as falhas
		// aparecem em Recent.
		return StatusUp
	}
	for i := 1; i <= DownAfter; i++ {
		if !r.failed[(r.next-i+Window)%Window] {
			return StatusUp
		}
	}
	return StatusDown
}

func (r *record) failures() int {
	f := 0
	for i := 0; i < r.n; i++ {
		if r.failed[(r.next-1-i+Window)%Window] {
			f++
		}
	}
	return f
}

// Health é a disponibilidade recente dos upstreams. É seguro para uso
// concorrente e barato no caminho da requisição: registrar uma tentativa
// toma um mutex e escreve num anel de tamanho fixo.
type Health struct {
	mu  sync.Mutex
	by  map[string]*record
	now func() time.Time
}

// New cria o acompanhamento vazio: todo upstream começa desconhecido.
func New() *Health {
	return &Health{by: map[string]*record{}, now: time.Now}
}

func (h *Health) rec(upstream string) *record {
	r := h.by[upstream]
	if r == nil {
		r = &record{}
		h.by[upstream] = r
	}
	return r
}

// Success registra que o upstream respondeu, qualquer que seja o status: um
// 5xx do próprio upstream é uma resposta.
func (h *Health) Success(upstream string) {
	if h == nil || upstream == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rec(upstream)
	r.add(false)
	r.lastSuccess = h.now().UTC()
}

// Failure registra uma tentativa sem resposta do upstream: conexão recusada,
// tempo limite de conexão ou o tempo limite da rota esgotado antes da
// resposta. A desistência do cliente não é falha do upstream e não passa por
// aqui.
func (h *Health) Failure(upstream, reason string) {
	if h == nil || upstream == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rec(upstream)
	r.add(true)
	r.lastFailure = h.now().UTC()
	r.lastError = reason
}

// Report devolve a disponibilidade de cada upstream declarado pelas rotas,
// na ordem em que aparece na precedência, com as rotas que apontam para ele.
// Rotas sem upstream não aparecem. O que foi registrado para um upstream que
// deixou de ser declarado é descartado: se ele voltar, volta desconhecido.
func (h *Health) Report(routes []*config.CompiledRoute) []Item {
	items := []Item{}
	index := map[string]int{}
	for _, rt := range routes {
		u := rt.Doc.Upstream
		if u == "" {
			continue
		}
		if i, ok := index[u]; ok {
			items[i].Routes = append(items[i].Routes, rt.Name())
			continue
		}
		index[u] = len(items)
		items = append(items, Item{Upstream: u, Routes: []string{rt.Name()}})
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for u := range h.by {
		if _, ok := index[u]; !ok {
			delete(h.by, u)
		}
	}
	for i := range items {
		r := h.by[items[i].Upstream]
		items[i].Status = r.status()
		items[i].LastError = ""
		if r == nil {
			continue
		}
		items[i].Recent = Recent{Attempts: r.n, Failures: r.failures()}
		items[i].LastSuccessAt = timePtr(r.lastSuccess)
		items[i].LastFailureAt = timePtr(r.lastFailure)
		items[i].LastError = r.lastError
	}
	return items
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
