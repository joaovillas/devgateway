package override

import (
	"slices"
	"sync"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
)

// Motivos de expiração informados no estado vivo.
const (
	ExpiredTTL          = "ttl"
	ExpiredApplications = "applications"
)

// Tracker guarda o estado vivo dos overrides — início do relógio do tempo de
// vida, contagem de aplicações e última aplicação — fora do snapshot, que é
// imutável. O estado de um override é identificado por rota e nome e
// sobrevive às trocas de snapshot enquanto o override continua existindo; o
// relógio e a contagem recomeçam quando o override surge, quando ttl ou
// maxApplications mudam, quando ele é religado e sob Reset.
type Tracker struct {
	live *config.Live
	now  func() time.Time

	mu      sync.Mutex
	synced  *config.Snapshot
	entries map[stateKey]*entry
}

type stateKey struct{ route, name string }

type entry struct {
	enabled       bool
	ttl           *time.Duration
	max           *int
	registeredAt  time.Time
	applications  int64
	lastAppliedAt time.Time
}

// NewTracker acompanha os overrides da configuração em vigor em live. O
// relógio de cada override presente começa agora.
func NewTracker(live *config.Live) *Tracker {
	return newTracker(live, time.Now)
}

func newTracker(live *config.Live, now func() time.Time) *Tracker {
	t := &Tracker{live: live, now: now, entries: map[stateKey]*entry{}}
	t.mu.Lock()
	t.refreshLocked()
	t.mu.Unlock()
	// A reconciliação acontece na própria troca, para que o relógio de um
	// override novo comece na carga e não na primeira requisição depois dela.
	live.Subscribe(func(*config.Snapshot) {
		t.mu.Lock()
		t.refreshLocked()
		t.mu.Unlock()
	})
	return t
}

// refreshLocked reconcilia o estado com o snapshot em vigor, se ele mudou
// desde a última reconciliação. Só o snapshot em vigor é considerado: uma
// requisição que ainda segue com um snapshot anterior não faz o estado
// voltar atrás.
func (t *Tracker) refreshLocked() {
	snap := t.live.Load()
	if snap == t.synced {
		return
	}
	t.synced = snap
	now := t.now()
	next := make(map[stateKey]*entry, len(t.entries))
	for _, r := range snap.Routes {
		for _, o := range r.Overrides {
			k := stateKey{o.Route, o.Doc.Name}
			ttl, maxApps := limits(o)
			e := t.entries[k]
			if e == nil || !equalPtr(e.ttl, ttl) || !equalPtr(e.max, maxApps) || (!e.enabled && o.Doc.Enabled()) {
				e = &entry{registeredAt: now}
			}
			e.enabled, e.ttl, e.max = o.Doc.Enabled(), ttl, maxApps
			next[k] = e
		}
	}
	t.entries = next
}

func limits(o *config.CompiledOverride) (*time.Duration, *int) {
	var ttl *time.Duration
	if o.Doc.TTL != nil {
		d := time.Duration(*o.Doc.TTL)
		ttl = &d
	}
	var n *int
	if o.Doc.MaxApplications != nil {
		v := *o.Doc.MaxApplications
		n = &v
	}
	return ttl, n
}

func equalPtr[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// expired devolve o motivo de expiração, ou vazio.
func (e *entry) expired(now time.Time) string {
	switch {
	case e.ttl != nil && !now.Before(e.registeredAt.Add(*e.ttl)):
		return ExpiredTTL
	case e.max != nil && e.applications >= int64(*e.max):
		return ExpiredApplications
	}
	return ""
}

// lookupLocked devolve o estado do override. Um override que não consta do
// snapshot em vigor (removido enquanto uma requisição ainda o usava) não tem
// estado: vale sem limites só se não declara nenhum.
func (t *Tracker) lookupLocked(o *config.CompiledOverride) (*entry, bool) {
	t.refreshLocked()
	if e := t.entries[stateKey{o.Route, o.Doc.Name}]; e != nil {
		return e, true
	}
	return nil, o.Doc.TTL == nil && o.Doc.MaxApplications == nil
}

// Active informa se o override pode participar da seleção agora: não
// expirou pelo tempo nem pela contagem. Que ele esteja ligado é verificado à
// parte, pelo documento.
func (t *Tracker) Active(o *config.CompiledOverride) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.lookupLocked(o)
	if e == nil {
		return ok
	}
	return e.expired(t.now()) == ""
}

// Claim conta uma aplicação do override, se ele ainda não expirou, e informa
// se a aplicação vale. Verificar e contar acontecem juntos: sob requisições
// concorrentes, um override com limite de n aplicações é aplicado no máximo
// n vezes.
func (t *Tracker) Claim(o *config.CompiledOverride) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.lookupLocked(o)
	if e == nil {
		return ok
	}
	now := t.now()
	if e.expired(now) != "" {
		return false
	}
	e.applications++
	e.lastAppliedAt = now
	return true
}

// Reset recomeça o relógio do tempo de vida e zera a contagem de aplicações
// do override, reativando-o se expirou. Não altera o documento. Informa se o
// override existe na configuração em vigor.
func (t *Tracker) Reset(route, name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	e := t.entries[stateKey{route, name}]
	if e == nil {
		return false
	}
	e.registeredAt, e.applications, e.lastAppliedAt = t.now(), 0, time.Time{}
	return true
}

// LiveState é o estado vivo de um override, como a API o expõe.
type LiveState struct {
	Route    string `json:"route"`
	Override string `json:"override"`
	// Enabled é o valor do documento.
	Enabled bool `json:"enabled"`
	// Active informa que o override participa da seleção agora: ligado e não
	// expirado.
	Active bool `json:"active"`
	// Expired é nil, "ttl" ou "applications".
	Expired *string `json:"expired"`
	// RegisteredAt é o início do relógio do tempo de vida.
	RegisteredAt time.Time `json:"registeredAt"`
	// TTLRemainingMs é o restante do tempo de vida: nil sem tempo de vida, 0
	// quando expirado.
	TTLRemainingMs *int64 `json:"ttlRemainingMs"`
	// Applications conta as aplicações desde RegisteredAt.
	Applications int64 `json:"applications"`
	// MaxApplications é o limite declarado, nil sem limite.
	MaxApplications *int       `json:"maxApplications"`
	LastAppliedAt   *time.Time `json:"lastAppliedAt"`
}

func (e *entry) status(route, name string, now time.Time) LiveState {
	s := LiveState{
		Route:        route,
		Override:     name,
		Enabled:      e.enabled,
		RegisteredAt: e.registeredAt,
		Applications: e.applications,
	}
	if why := e.expired(now); why != "" {
		s.Expired = &why
	}
	s.Active = e.enabled && s.Expired == nil
	if e.ttl != nil {
		ms := max(e.registeredAt.Add(*e.ttl).Sub(now), 0).Milliseconds()
		s.TTLRemainingMs = &ms
	}
	if e.max != nil {
		n := *e.max
		s.MaxApplications = &n
	}
	if !e.lastAppliedAt.IsZero() {
		at := e.lastAppliedAt
		s.LastAppliedAt = &at
	}
	return s
}

// State devolve o estado vivo do override de nome dado na rota.
func (t *Tracker) State(route, name string) (LiveState, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	e := t.entries[stateKey{route, name}]
	if e == nil {
		return LiveState{}, false
	}
	return e.status(route, name, t.now()), true
}

// States devolve o estado vivo de todos os overrides da configuração em
// vigor, rota a rota na ordem de precedência, e o instante da consulta.
func (t *Tracker) States() ([]LiveState, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked()
	now := t.now()
	out := []LiveState{}
	for _, r := range t.synced.Routes {
		for _, o := range r.Overrides {
			if e := t.entries[stateKey{o.Route, o.Doc.Name}]; e != nil {
				out = append(out, e.status(o.Route, o.Doc.Name, now))
			}
		}
	}
	return slices.Clip(out), now
}
