package override

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
)

// clock é um relógio de teste que só anda quando mandado.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func snapOf(t *testing.T, overrides ...config.Override) *config.Snapshot {
	t.Helper()
	return config.NewSnapshot(config.Settings{}, []*config.CompiledRoute{compiled(t, overrides...)}, nil)
}

func tracked(t *testing.T, overrides ...config.Override) (*Tracker, *config.Live, *clock) {
	t.Helper()
	c := newClock()
	live := config.NewLive(snapOf(t, overrides...))
	return newTracker(live, c.now), live, c
}

func limited(name string, ttl time.Duration, maxApps int) config.Override {
	o := ov(name, config.OverrideMatch{Path: "/api/*"})
	if ttl > 0 {
		o.TTL = dur(ttl)
	}
	if maxApps > 0 {
		o.MaxApplications = &maxApps
	}
	return o
}

func stateOf(t *testing.T, tr *Tracker, name string) LiveState {
	t.Helper()
	s, ok := tr.State("payments", name)
	if !ok {
		t.Fatalf("override %s sem estado vivo", name)
	}
	return s
}

// Requirement: Expiração por tempo e por contagem

func TestOverrideExpiresByTime(t *testing.T) {
	tr, live, c := tracked(t, limited("o", 30*time.Second, 0))
	o := live.Load().Routes[0].Overrides[0]
	c.advance(29 * time.Second)
	if !tr.Active(o) || !tr.Claim(o) {
		t.Fatal("antes de 30s o override deveria estar ativo")
	}
	c.advance(1*time.Second + time.Millisecond)
	if tr.Active(o) || tr.Claim(o) {
		t.Fatal("passados 30s o override deveria ter expirado sem ação do usuário")
	}
	s := stateOf(t, tr, "o")
	if s.Active || s.Expired == nil || *s.Expired != ExpiredTTL || *s.TTLRemainingMs != 0 {
		t.Fatalf("estado de expiração pelo tempo inesperado: %+v", s)
	}
}

func TestOverrideExpiresByCount(t *testing.T) {
	tr, live, _ := tracked(t, limited("o", 0, 2))
	o := live.Load().Routes[0].Overrides[0]
	got := []bool{tr.Claim(o), tr.Claim(o), tr.Claim(o)}
	if !got[0] || !got[1] || got[2] {
		t.Fatalf("as duas primeiras aplicações deveriam valer e a terceira não: %v", got)
	}
	if tr.Active(o) {
		t.Fatal("esgotado o limite, o override deveria sair da seleção")
	}
	s := stateOf(t, tr, "o")
	if s.Expired == nil || *s.Expired != ExpiredApplications || s.Applications != 2 {
		t.Fatalf("estado de expiração pela contagem inesperado: %+v", s)
	}
}

func TestRemainingTimeAndCountQueryable(t *testing.T) {
	tr, live, c := tracked(t, limited("o", 60*time.Second, 5))
	o := live.Load().Routes[0].Overrides[0]
	registered := c.now()
	c.advance(10 * time.Second)
	tr.Claim(o)
	c.advance(10 * time.Second)
	tr.Claim(o)
	s := stateOf(t, tr, "o")
	if s.TTLRemainingMs == nil || *s.TTLRemainingMs != 40000 {
		t.Fatalf("esperados 40s restantes, estado %+v", s)
	}
	if s.Applications != 2 || s.MaxApplications == nil || *s.MaxApplications != 5 {
		t.Fatalf("esperadas duas aplicações de um limite de cinco: %+v", s)
	}
	if !s.Active || s.Expired != nil || !s.Enabled || !s.RegisteredAt.Equal(registered) {
		t.Fatalf("o override deveria estar ativo desde o registro: %+v", s)
	}
	if s.LastAppliedAt == nil || !s.LastAppliedAt.Equal(c.now()) {
		t.Fatalf("a última aplicação deveria ser a de agora: %+v", s.LastAppliedAt)
	}
	all, now := tr.States()
	if len(all) != 1 || all[0].Override != "o" || all[0].Route != "payments" || !now.Equal(c.now()) {
		t.Fatalf("a consulta geral deveria trazer o override: %+v em %v", all, now)
	}
}

func TestOverrideWithoutLimitsRemains(t *testing.T) {
	tr, live, c := tracked(t, limited("o", 0, 0))
	o := live.Load().Routes[0].Overrides[0]
	for range 1000 {
		if !tr.Claim(o) {
			t.Fatal("sem limites o override deveria valer sempre")
		}
	}
	c.advance(30 * 24 * time.Hour)
	if !tr.Active(o) {
		t.Fatal("sem tempo de vida o override deveria continuar ativo")
	}
	s := stateOf(t, tr, "o")
	if s.TTLRemainingMs != nil || s.MaxApplications != nil || s.Expired != nil || s.Applications != 1000 {
		t.Fatalf("sem limites o estado não deveria ter restante nem limite: %+v", s)
	}
}

// Sob requisições concorrentes, um override limitado a n aplicações é
// aplicado exatamente n vezes.
func TestCountLimitHoldsUnderConcurrency(t *testing.T) {
	tr, live, _ := tracked(t, limited("o", 0, 10))
	o := live.Load().Routes[0].Overrides[0]
	var won atomic.Int64
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(func() {
			if tr.Claim(o) {
				won.Add(1)
			}
		})
	}
	wg.Wait()
	if won.Load() != 10 {
		t.Fatalf("esperadas exatamente 10 aplicações, houve %d", won.Load())
	}
}

// O estado vivo fica fora do snapshot: uma recarga que não muda o override
// preserva o relógio e a contagem.
func TestStatePreservedAcrossReloadWhenUnchanged(t *testing.T) {
	o := limited("o", time.Minute, 5)
	tr, live, c := tracked(t, o, limited("outro", 0, 0))
	tr.Claim(live.Load().Routes[0].Override("o"))
	c.advance(20 * time.Second)
	// Outro override muda e este fica igual.
	live.Swap(snapOf(t, o, limited("outro", 0, 3)))
	s := stateOf(t, tr, "o")
	if s.Applications != 1 || *s.TTLRemainingMs != 40000 {
		t.Fatalf("a recarga não deveria recomeçar o override inalterado: %+v", s)
	}
	// Mudar a resposta declarada também não recomeça o relógio.
	changed := o
	changed.Respond = respond("outra resposta")
	live.Swap(snapOf(t, changed))
	if s := stateOf(t, tr, "o"); s.Applications != 1 || *s.TTLRemainingMs != 40000 {
		t.Fatalf("mudar só a resposta não deveria recomeçar o estado: %+v", s)
	}
	if _, ok := tr.State("payments", "outro"); ok {
		t.Fatal("o override removido não deveria ter estado")
	}
}

func TestStateRestartsWhenLimitsChange(t *testing.T) {
	tr, live, c := tracked(t, limited("o", time.Minute, 5))
	tr.Claim(live.Load().Routes[0].Overrides[0])
	c.advance(20 * time.Second)
	live.Swap(snapOf(t, limited("o", 2*time.Minute, 5)))
	s := stateOf(t, tr, "o")
	if s.Applications != 0 || *s.TTLRemainingMs != 120000 || !s.RegisteredAt.Equal(c.now()) {
		t.Fatalf("mudar o tempo de vida deveria recomeçar relógio e contagem: %+v", s)
	}
	tr.Claim(live.Load().Routes[0].Overrides[0])
	live.Swap(snapOf(t, limited("o", 2*time.Minute, 6)))
	if s := stateOf(t, tr, "o"); s.Applications != 0 {
		t.Fatalf("mudar o limite de aplicações deveria zerar a contagem: %+v", s)
	}
}

// Religar um override recomeça o relógio: um override que expirou enquanto
// estava ligado volta a valer.
func TestReenablingRestartsState(t *testing.T) {
	o := limited("o", 0, 1)
	tr, live, _ := tracked(t, o)
	tr.Claim(live.Load().Routes[0].Overrides[0])
	off := o
	off.On = new(bool)
	live.Swap(snapOf(t, off))
	if s := stateOf(t, tr, "o"); s.Enabled || s.Active || s.Applications != 1 {
		t.Fatalf("desligado, o override deveria estar inativo e manter a contagem: %+v", s)
	}
	live.Swap(snapOf(t, o))
	s := stateOf(t, tr, "o")
	if !s.Enabled || !s.Active || s.Applications != 0 {
		t.Fatalf("religado, o override deveria voltar a valer: %+v", s)
	}
}

func TestResetReactivatesExpired(t *testing.T) {
	tr, live, c := tracked(t, limited("o", time.Second, 0))
	c.advance(2 * time.Second)
	if tr.Active(live.Load().Routes[0].Overrides[0]) {
		t.Fatal("o override deveria ter expirado")
	}
	if !tr.Reset("payments", "o") {
		t.Fatal("o reset deveria encontrar o override")
	}
	if !tr.Active(live.Load().Routes[0].Overrides[0]) {
		t.Fatal("depois do reset o override deveria voltar a valer")
	}
	if tr.Reset("payments", "nao-existe") {
		t.Fatal("o reset de override inexistente deveria falhar")
	}
}
