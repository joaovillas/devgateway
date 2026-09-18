package override

import (
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
)

func prob(p float64) *float64 { return &p }

func dur(d time.Duration) *config.Duration {
	c := config.Duration(d)
	return &c
}

// draws devolve, para as sequências 1..n, se a aplicação foi sorteada.
func draws(o *config.CompiledOverride, seed *uint64, n int) []bool {
	out := make([]bool, n)
	for i := range n {
		out[i] = Decide(o, Source(seed, uint64(i+1))).Apply
	}
	return out
}

// Requirement: Determinismo por seed

func TestSameSeedSameDecisions(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"), Probability: prob(0.5),
	}).Overrides[0]
	seed := uint64(42)
	a, b := draws(o, &seed, 100), draws(o, &seed, 100)
	if !slices.Equal(a, b) {
		t.Fatal("o mesmo seed deveria reproduzir as mesmas decisões")
	}
	other := uint64(43)
	if slices.Equal(a, draws(o, &other, 100)) {
		t.Fatal("seeds distintos deveriam divergir")
	}
}

// As decisões de uma requisição dependem só de (seed, sequência): sorteadas
// de goroutines concorrentes e em qualquer ordem, dão o mesmo resultado.
func TestDecisionsIndependentOfScheduling(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"),
		Probability: prob(0.5), Latency: &config.Latency{Min: dur(100 * time.Millisecond), Max: dur(500 * time.Millisecond)},
	}).Overrides[0]
	seed := uint64(7)
	const n = 200
	want := make([]Decision, n)
	for i := range n {
		want[i] = Decide(o, Source(&seed, uint64(i+1)))
	}
	got := make([]Decision, n)
	var wg sync.WaitGroup
	for i := n - 1; i >= 0; i-- {
		wg.Go(func() { got[i] = Decide(o, Source(&seed, uint64(i+1))) })
	}
	wg.Wait()
	if !slices.Equal(want, got) {
		t.Fatal("as decisões concorrentes deveriam ser iguais às sequenciais")
	}
}

func TestNoSeedIsNotReproducible(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"), Probability: prob(0.5),
	}).Overrides[0]
	if slices.Equal(draws(o, nil, 100), draws(o, nil, 100)) {
		t.Fatal("sem seed as decisões não deveriam se repetir")
	}
}

// O sorteio consome sempre três valores, em ordem fixa: aplicação, queda e
// atraso. O atraso ocupa o terceiro valor qualquer que seja a configuração.
func TestFixedDrawOrder(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"),
		Latency: &config.Latency{Min: dur(0), Max: dur(time.Second)},
	}).Overrides[0]
	seed := uint64(1)
	for seq := uint64(1); seq <= 20; seq++ {
		ref := Source(&seed, seq)
		ref.Float64()
		ref.Float64()
		want := time.Duration(ref.Float64() * float64(time.Second))
		rng := Source(&seed, seq)
		d := Decide(o, rng)
		if d.Delay != want {
			t.Fatalf("seq %d: o atraso deveria vir do terceiro sorteio: %v, esperado %v", seq, d.Delay, want)
		}
		if rng.Uint64() != ref.Uint64() {
			t.Fatalf("seq %d: a decisão deveria consumir exatamente três valores", seq)
		}
	}
}

func TestDecisionWithoutApplicationIsEmpty(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Probability: prob(0),
		Drop: true, Latency: &config.Latency{Fixed: dur(time.Second)},
	}).Overrides[0]
	seed := uint64(3)
	d := Decide(o, Source(&seed, 1))
	if d.Apply || d.Drop || d.Delay != 0 || d.Applied() != nil || d.Override != o {
		t.Fatalf("sem aplicação sorteada não deveria haver queda nem atraso: %+v", d)
	}
}

func TestDecisionCarriesDropAndDelay(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Drop: true, Latency: &config.Latency{Fixed: dur(2 * time.Second)},
	}).Overrides[0]
	d := Decide(o, Source(nil, 1))
	if !d.Apply || !d.Drop || d.Delay != 2*time.Second || d.Applied() != o {
		t.Fatalf("decisão inesperada: %+v", d)
	}
}

func TestDelayWithinRange(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Latency: &config.Latency{Min: dur(100 * time.Millisecond), Max: dur(500 * time.Millisecond)},
	}).Overrides[0]
	seen := map[time.Duration]bool{}
	for seq := range uint64(50) {
		d := Decide(o, Source(nil, seq)).Delay
		if d < 100*time.Millisecond || d > 500*time.Millisecond {
			t.Fatalf("atraso %v fora do intervalo", d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatal("os atrasos sorteados não deveriam ser todos iguais")
	}
}

func TestNilOverrideDrawsNothing(t *testing.T) {
	seed := uint64(1)
	rng := Source(&seed, 1)
	if d := Decide(nil, rng); d != (Decision{}) {
		t.Fatalf("sem override a decisão deveria ser vazia: %+v", d)
	}
	if rng.Uint64() != Source(&seed, 1).Uint64() {
		t.Fatal("sem override nada deveria ser sorteado")
	}
}

// Requirement: Probabilidade de aplicação

func TestProbabilityAbsentAlwaysApplies(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"),
	}).Overrides[0]
	for i, applied := range draws(o, nil, 1000) {
		if !applied {
			t.Fatalf("sem probabilidade a requisição %d deveria ser aplicada", i+1)
		}
	}
}

func TestProbabilityOneAlwaysAppliesAndZeroNever(t *testing.T) {
	seed := uint64(9)
	for _, c := range []struct {
		p    float64
		want bool
	}{{1, true}, {0, false}} {
		o := compiled(t, config.Override{
			Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"), Probability: prob(c.p),
		}).Overrides[0]
		for i, applied := range draws(o, &seed, 1000) {
			if applied != c.want {
				t.Fatalf("probabilidade %v: requisição %d aplicada = %v", c.p, i+1, applied)
			}
		}
	}
}

// Requirement: Resposta declarada pelo override

func TestResponseHeadersAndStatus(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Respond: &config.Respond{Headers: map[string]config.HeaderValues{"x-source": {"override"}}, Body: map[string]any{"ok": true}},
	}).Overrides[0]
	h := http.Header{}
	SetHeaders(h, o)
	if Status(o) != http.StatusOK {
		t.Fatalf("status padrão deveria ser 200: %d", Status(o))
	}
	if h.Get("X-Source") != "override" || h.Get("Content-Type") != "application/json" {
		t.Fatalf("cabeçalhos inesperados: %v", h)
	}
	if string(Body(o)) != `{"ok":true}` {
		t.Fatalf("corpo inesperado: %s", Body(o))
	}
}

func TestDeclaredContentTypeWins(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Respond: &config.Respond{
			Status:  201,
			Headers: map[string]config.HeaderValues{"content-type": {"application/problem+json"}},
			Body:    map[string]any{"ok": true},
		},
	}).Overrides[0]
	h := http.Header{}
	SetHeaders(h, o)
	if got := h.Values("Content-Type"); len(got) != 1 || got[0] != "application/problem+json" {
		t.Fatalf("o tipo de conteúdo declarado deveria prevalecer: %q", got)
	}
	if Status(o) != 201 {
		t.Fatalf("status declarado deveria valer: %d", Status(o))
	}
}
