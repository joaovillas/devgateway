package override

import (
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gamerjp64/devgateway/internal/config"
)

func prob(p float64) *float64 { return &p }

func dur(d time.Duration) *config.Duration {
	c := config.Duration(d)
	return &c
}

// draws reports, for sequence numbers 1..n, whether the application was drawn.
func draws(o *config.CompiledOverride, seed *uint64, n int) []bool {
	out := make([]bool, n)
	for i := range n {
		out[i] = Decide(o, Source(seed, uint64(i+1))).Apply
	}
	return out
}

// Requirement: Determinism by seed

func TestSameSeedSameDecisions(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"), Probability: prob(0.5),
	}).Overrides[0]
	seed := uint64(42)
	a, b := draws(o, &seed, 100), draws(o, &seed, 100)
	if !slices.Equal(a, b) {
		t.Fatal("the same seed should reproduce the same decisions")
	}
	other := uint64(43)
	if slices.Equal(a, draws(o, &other, 100)) {
		t.Fatal("different seeds should diverge")
	}
}

// A request's decisions depend only on (seed, sequence number): drawn from
// concurrent goroutines and in any order, they give the same result.
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
		t.Fatal("the concurrent decisions should match the sequential ones")
	}
}

func TestNoSeedIsNotReproducible(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"), Probability: prob(0.5),
	}).Overrides[0]
	if slices.Equal(draws(o, nil, 100), draws(o, nil, 100)) {
		t.Fatal("without a seed the decisions should not repeat")
	}
}

// The draw always consumes three values, in a fixed order: application, drop
// and delay. The delay takes the third value whatever the configuration is.
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
			t.Fatalf("seq %d: the delay should come from the third draw: %v, want %v", seq, d.Delay, want)
		}
		if rng.Uint64() != ref.Uint64() {
			t.Fatalf("seq %d: the decision should consume exactly three values", seq)
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
		t.Fatalf("with no application drawn there should be no drop and no delay: %+v", d)
	}
}

func TestDecisionCarriesDropAndDelay(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Drop: true, Latency: &config.Latency{Fixed: dur(2 * time.Second)},
	}).Overrides[0]
	d := Decide(o, Source(nil, 1))
	if !d.Apply || !d.Drop || d.Delay != 2*time.Second || d.Applied() != o {
		t.Fatalf("unexpected decision: %+v", d)
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
			t.Fatalf("delay %v out of range", d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatal("the drawn delays should not all be the same")
	}
}

func TestNilOverrideDrawsNothing(t *testing.T) {
	seed := uint64(1)
	rng := Source(&seed, 1)
	if d := Decide(nil, rng); d != (Decision{}) {
		t.Fatalf("with no override the decision should be empty: %+v", d)
	}
	if rng.Uint64() != Source(&seed, 1).Uint64() {
		t.Fatal("with no override nothing should be drawn")
	}
}

// Requirement: Application probability

func TestProbabilityAbsentAlwaysApplies(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"),
	}).Overrides[0]
	for i, applied := range draws(o, nil, 1000) {
		if !applied {
			t.Fatalf("without a probability request %d should be applied", i+1)
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
				t.Fatalf("probability %v: request %d applied = %v", c.p, i+1, applied)
			}
		}
	}
}

// Requirement: Response declared by the override

func TestResponseHeadersAndStatus(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Respond: &config.Respond{Headers: map[string]config.HeaderValues{"x-source": {"override"}}, Body: map[string]any{"ok": true}},
	}).Overrides[0]
	h := http.Header{}
	SetHeaders(h, o)
	if Status(o) != http.StatusOK {
		t.Fatalf("the default status should be 200: %d", Status(o))
	}
	if h.Get("X-Source") != "override" || h.Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected headers: %v", h)
	}
	if string(Body(o)) != `{"ok":true}` {
		t.Fatalf("unexpected body: %s", Body(o))
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
		t.Fatalf("the declared content type should win: %q", got)
	}
	if Status(o) != 201 {
		t.Fatalf("the declared status should hold: %d", Status(o))
	}
}
