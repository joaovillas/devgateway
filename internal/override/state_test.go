package override

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gamerjp64/devgateway/internal/config"
)

// clock is a test clock that only moves when told to.
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
		t.Fatalf("override %s has no live state", name)
	}
	return s
}

// Requirement: Expiry by time and by count

func TestOverrideExpiresByTime(t *testing.T) {
	tr, live, c := tracked(t, limited("o", 30*time.Second, 0))
	o := live.Load().Routes[0].Overrides[0]
	c.advance(29 * time.Second)
	if !tr.Active(o) || !tr.Claim(o) {
		t.Fatal("before 30s the override should be active")
	}
	c.advance(1*time.Second + time.Millisecond)
	if tr.Active(o) || tr.Claim(o) {
		t.Fatal("after 30s the override should have expired with no action from the user")
	}
	s := stateOf(t, tr, "o")
	if s.Active || s.Expired == nil || *s.Expired != ExpiredTTL || *s.TTLRemainingMs != 0 {
		t.Fatalf("unexpected expiry-by-time state: %+v", s)
	}
}

func TestOverrideExpiresByCount(t *testing.T) {
	tr, live, _ := tracked(t, limited("o", 0, 2))
	o := live.Load().Routes[0].Overrides[0]
	got := []bool{tr.Claim(o), tr.Claim(o), tr.Claim(o)}
	if !got[0] || !got[1] || got[2] {
		t.Fatalf("the first two applications should hold and the third should not: %v", got)
	}
	if tr.Active(o) {
		t.Fatal("once the limit is used up, the override should drop out of the selection")
	}
	s := stateOf(t, tr, "o")
	if s.Expired == nil || *s.Expired != ExpiredApplications || s.Applications != 2 {
		t.Fatalf("unexpected expiry-by-count state: %+v", s)
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
		t.Fatalf("want 40s remaining, state %+v", s)
	}
	if s.Applications != 2 || s.MaxApplications == nil || *s.MaxApplications != 5 {
		t.Fatalf("want two applications out of a limit of five: %+v", s)
	}
	if !s.Active || s.Expired != nil || !s.Enabled || !s.RegisteredAt.Equal(registered) {
		t.Fatalf("the override should be active since it was registered: %+v", s)
	}
	if s.LastAppliedAt == nil || !s.LastAppliedAt.Equal(c.now()) {
		t.Fatalf("the last application should be the one from just now: %+v", s.LastAppliedAt)
	}
	all, now := tr.States()
	if len(all) != 1 || all[0].Override != "o" || all[0].Route != "payments" || !now.Equal(c.now()) {
		t.Fatalf("the overall query should include the override: %+v at %v", all, now)
	}
}

func TestOverrideWithoutLimitsRemains(t *testing.T) {
	tr, live, c := tracked(t, limited("o", 0, 0))
	o := live.Load().Routes[0].Overrides[0]
	for range 1000 {
		if !tr.Claim(o) {
			t.Fatal("with no limits the override should always hold")
		}
	}
	c.advance(30 * 24 * time.Hour)
	if !tr.Active(o) {
		t.Fatal("with no time to live the override should stay active")
	}
	s := stateOf(t, tr, "o")
	if s.TTLRemainingMs != nil || s.MaxApplications != nil || s.Expired != nil || s.Applications != 1000 {
		t.Fatalf("with no limits the state should have neither a remainder nor a limit: %+v", s)
	}
}

// Under concurrent requests, an override limited to n applications is applied
// exactly n times.
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
		t.Fatalf("want exactly 10 applications, got %d", won.Load())
	}
}

// The live state lives outside the snapshot: a reload that does not change
// the override keeps its clock and its count.
func TestStatePreservedAcrossReloadWhenUnchanged(t *testing.T) {
	o := limited("o", time.Minute, 5)
	tr, live, c := tracked(t, o, limited("other", 0, 0))
	tr.Claim(live.Load().Routes[0].Override("o"))
	c.advance(20 * time.Second)
	// Another override changes and this one stays the same.
	live.Swap(snapOf(t, o, limited("other", 0, 3)))
	s := stateOf(t, tr, "o")
	if s.Applications != 1 || *s.TTLRemainingMs != 40000 {
		t.Fatalf("the reload should not restart the unchanged override: %+v", s)
	}
	// Changing the declared response does not restart the clock either.
	changed := o
	changed.Respond = respond("another response")
	live.Swap(snapOf(t, changed))
	if s := stateOf(t, tr, "o"); s.Applications != 1 || *s.TTLRemainingMs != 40000 {
		t.Fatalf("changing only the response should not restart the state: %+v", s)
	}
	if _, ok := tr.State("payments", "other"); ok {
		t.Fatal("the removed override should have no state")
	}
}

func TestStateRestartsWhenLimitsChange(t *testing.T) {
	tr, live, c := tracked(t, limited("o", time.Minute, 5))
	tr.Claim(live.Load().Routes[0].Overrides[0])
	c.advance(20 * time.Second)
	live.Swap(snapOf(t, limited("o", 2*time.Minute, 5)))
	s := stateOf(t, tr, "o")
	if s.Applications != 0 || *s.TTLRemainingMs != 120000 || !s.RegisteredAt.Equal(c.now()) {
		t.Fatalf("changing the time to live should restart clock and count: %+v", s)
	}
	tr.Claim(live.Load().Routes[0].Overrides[0])
	live.Swap(snapOf(t, limited("o", 2*time.Minute, 6)))
	if s := stateOf(t, tr, "o"); s.Applications != 0 {
		t.Fatalf("changing the application limit should zero the count: %+v", s)
	}
}

// Re-enabling an override restarts its clock: an override that expired while
// it was enabled holds again.
func TestReenablingRestartsState(t *testing.T) {
	o := limited("o", 0, 1)
	tr, live, _ := tracked(t, o)
	tr.Claim(live.Load().Routes[0].Overrides[0])
	off := o
	off.On = new(bool)
	live.Swap(snapOf(t, off))
	if s := stateOf(t, tr, "o"); s.Enabled || s.Active || s.Applications != 1 {
		t.Fatalf("once disabled, the override should be inactive and keep its count: %+v", s)
	}
	live.Swap(snapOf(t, o))
	s := stateOf(t, tr, "o")
	if !s.Enabled || !s.Active || s.Applications != 0 {
		t.Fatalf("once re-enabled, the override should hold again: %+v", s)
	}
}

func TestResetReactivatesExpired(t *testing.T) {
	tr, live, c := tracked(t, limited("o", time.Second, 0))
	c.advance(2 * time.Second)
	if tr.Active(live.Load().Routes[0].Overrides[0]) {
		t.Fatal("the override should have expired")
	}
	if !tr.Reset("payments", "o") {
		t.Fatal("the reset should find the override")
	}
	if !tr.Active(live.Load().Routes[0].Overrides[0]) {
		t.Fatal("after the reset the override should hold again")
	}
	if tr.Reset("payments", "does-not-exist") {
		t.Fatal("resetting an override that does not exist should fail")
	}
}
