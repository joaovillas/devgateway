package override

import (
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
)

// chance builds a frequency pointer: an effect's chance or the legacy
// probability of the override.
func chance(c float64) *float64 { return &c }

func dur(d time.Duration) *config.Duration {
	c := config.Duration(d)
	return &c
}

// draws reports, for sequence numbers 1..n, whether any effect was drawn.
func draws(o *config.CompiledOverride, seed *uint64, n int) []bool {
	out := make([]bool, n)
	for i := range n {
		out[i] = Decide(o, Source(seed, uint64(i+1))).Apply
	}
	return out
}

// tally counts, over sequence numbers 1..n, how many requests each effect was
// drawn for.
type tally struct{ drop, respond, delayed int }

func count(o *config.CompiledOverride, seed *uint64, n int) tally {
	var t tally
	for i := range n {
		d := Decide(o, Source(seed, uint64(i+1)))
		if d.Drop {
			t.drop++
		}
		if d.Respond {
			t.respond++
		}
		if d.Delayed {
			t.delayed++
		}
	}
	return t
}

// Requirement: Determinism by seed

func TestSameSeedSameDecisions(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"), Probability: chance(0.5),
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
		Probability: chance(0.5), Latency: &config.Latency{Min: dur(100 * time.Millisecond), Max: dur(500 * time.Millisecond)},
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
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"), Probability: chance(0.5),
	}).Overrides[0]
	if slices.Equal(draws(o, nil, 100), draws(o, nil, 100)) {
		t.Fatal("without a seed the decisions should not repeat")
	}
}

// The draw always consumes four values, in a fixed order: drop, respond,
// delay and the delay's position within the range. Each one keeps its slot
// whatever the configuration declares.
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
		ref.Float64()
		want := time.Duration(ref.Float64() * float64(time.Second))
		rng := Source(&seed, seq)
		d := Decide(o, rng)
		if d.Delay != want {
			t.Fatalf("seq %d: the delay should come from the fourth draw: %v, want %v", seq, d.Delay, want)
		}
		if rng.Uint64() != ref.Uint64() {
			t.Fatalf("seq %d: the decision should consume exactly four values", seq)
		}
	}
}

func TestDecisionWithoutApplicationIsEmpty(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Probability: chance(0),
		Drop: config.Drop{On: true}, Latency: &config.Latency{Fixed: dur(time.Second)},
	}).Overrides[0]
	seed := uint64(3)
	d := Decide(o, Source(&seed, 1))
	if d.Apply || d.Drop || d.Respond || d.Delayed || d.Delay != 0 || d.Applied() != nil || d.Override != o {
		t.Fatalf("with no effect drawn there should be no drop and no delay: %+v", d)
	}
}

func TestDecisionCarriesDropAndDelay(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Drop: config.Drop{On: true}, Latency: &config.Latency{Fixed: dur(2 * time.Second)},
	}).Overrides[0]
	d := Decide(o, Source(nil, 1))
	if !d.Apply || !d.Drop || !d.Delayed || d.Delay != 2*time.Second || d.Applied() != o {
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

// Requirement: Frequency of each effect

func TestEffectWithoutChanceAlwaysHolds(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"),
	}).Overrides[0]
	if got := count(o, nil, 1000); got.respond != 1000 {
		t.Fatalf("with no frequency declared every request should be answered: %d of 1000", got.respond)
	}
	for i, applied := range draws(o, nil, 1000) {
		if !applied {
			t.Fatalf("with no frequency declared request %d should be applied", i+1)
		}
	}
}

// Binomial(1000, 0.3) has a standard deviation of about 14.5; the tolerance
// is four deviations.
const (
	thirtyLow, thirtyHigh = 242, 358
	// Binomial(1000, 0.05): standard deviation about 6.9, four deviations.
	fiveLow, fiveHigh = 22, 78
)

// Each declared effect is drawn against its own frequency: a response at 30%
// and a delay with no frequency, which holds on every request.
func TestEachEffectHasItsOwnChance(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Respond: &config.Respond{Status: 503, Chance: chance(0.3)},
		Latency: &config.Latency{Fixed: dur(time.Second)},
	}).Overrides[0]
	seed := uint64(42)
	got := count(o, &seed, 1000)
	if got.respond < thirtyLow || got.respond > thirtyHigh {
		t.Fatalf("want around 300 responses out of 1000, got %d", got.respond)
	}
	if got.delayed != 1000 {
		t.Fatalf("the delay declares no frequency and should hold on all 1000: %d", got.delayed)
	}
	if got.drop != 0 {
		t.Fatalf("no drop is declared and none should be drawn: %d", got.drop)
	}
}

// The drop carries its own frequency while the declared response holds on
// every request.
func TestDropChanceWithResponseAlways(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Respond: respond("x"), Drop: config.Drop{On: true, Chance: chance(0.05)},
	}).Overrides[0]
	seed := uint64(7)
	got := count(o, &seed, 1000)
	if got.drop < fiveLow || got.drop > fiveHigh {
		t.Fatalf("want around 50 drops out of 1000, got %d", got.drop)
	}
	if got.respond != 1000 {
		t.Fatalf("the response declares no frequency and should hold on all 1000: %d", got.respond)
	}
}

// A frequency of zero silences that effect and leaves the others alone.
func TestChanceZeroNeverHolds(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"},
		Respond: &config.Respond{Body: "x", Chance: chance(0)},
		Latency: &config.Latency{Fixed: dur(time.Second)},
	}).Overrides[0]
	seed := uint64(9)
	got := count(o, &seed, 1000)
	if got.respond != 0 {
		t.Fatalf("a frequency of zero should never hold: %d", got.respond)
	}
	if got.delayed != 1000 {
		t.Fatalf("the other effects should go on holding: %d delays of 1000", got.delayed)
	}
}

// The override's legacy probability is the default of the effects that
// declare none, and does not touch the ones that do.
func TestLegacyProbabilityIsTheDefaultChance(t *testing.T) {
	o := compiled(t, config.Override{
		Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Probability: chance(0.3),
		Respond: respond("x"),
		Latency: &config.Latency{Fixed: dur(time.Second), Chance: chance(1)},
	}).Overrides[0]
	seed := uint64(11)
	got := count(o, &seed, 1000)
	if got.respond < thirtyLow || got.respond > thirtyHigh {
		t.Fatalf("the response has no frequency of its own and should follow the 0.3: %d of 1000", got.respond)
	}
	if got.delayed != 1000 {
		t.Fatalf("the delay declares 1.0 and should hold on all 1000: %d", got.delayed)
	}
}

func TestProbabilityOneAlwaysAppliesAndZeroNever(t *testing.T) {
	seed := uint64(9)
	for _, c := range []struct {
		p    float64
		want bool
	}{{1, true}, {0, false}} {
		o := compiled(t, config.Override{
			Name: "o", Match: config.OverrideMatch{Path: "/api/*"}, Respond: respond("x"), Probability: chance(c.p),
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
