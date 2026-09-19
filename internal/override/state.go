package override

import (
	"slices"
	"sync"
	"time"

	"github.com/gamerjp64/devgateway/internal/config"
)

// Expiry reasons reported in the live state.
const (
	ExpiredTTL          = "ttl"
	ExpiredApplications = "applications"
)

// Tracker holds the overrides' live state — start of the time-to-live clock,
// application count and last application — outside the snapshot, which is
// immutable. An override's state is identified by route and name and survives
// snapshot swaps for as long as the override still exists; the clock and the
// count restart when the override appears, when ttl or maxApplications
// change, when it is re-enabled and under Reset.
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

// NewTracker tracks the overrides of the configuration in effect in live. The
// clock of every override present starts now.
func NewTracker(live *config.Live) *Tracker {
	return newTracker(live, time.Now)
}

func newTracker(live *config.Live, now func() time.Time) *Tracker {
	t := &Tracker{live: live, now: now, entries: map[stateKey]*entry{}}
	t.mu.Lock()
	t.refreshLocked()
	t.mu.Unlock()
	// Reconciliation happens on the swap itself, so that a new override's
	// clock starts at load time and not on the first request after it.
	live.Subscribe(func(*config.Snapshot) {
		t.mu.Lock()
		t.refreshLocked()
		t.mu.Unlock()
	})
	return t
}

// refreshLocked reconciles the state with the snapshot in effect, if it has
// changed since the last reconciliation. Only the snapshot in effect counts: a
// request still running with an earlier snapshot does not roll the state back.
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

// expired returns the expiry reason, or an empty string.
func (e *entry) expired(now time.Time) string {
	switch {
	case e.ttl != nil && !now.Before(e.registeredAt.Add(*e.ttl)):
		return ExpiredTTL
	case e.max != nil && e.applications >= int64(*e.max):
		return ExpiredApplications
	}
	return ""
}

// lookupLocked returns the override's state. An override that is not in the
// snapshot in effect (removed while a request was still using it) has no
// state: it holds without limits only if it declares none.
func (t *Tracker) lookupLocked(o *config.CompiledOverride) (*entry, bool) {
	t.refreshLocked()
	if e := t.entries[stateKey{o.Route, o.Doc.Name}]; e != nil {
		return e, true
	}
	return nil, o.Doc.TTL == nil && o.Doc.MaxApplications == nil
}

// Active reports whether the override can take part in the selection right
// now: it has expired neither by time nor by count. Whether it is enabled is
// checked separately, from the document.
func (t *Tracker) Active(o *config.CompiledOverride) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.lookupLocked(o)
	if e == nil {
		return ok
	}
	return e.expired(t.now()) == ""
}

// Claim counts one application of the override, if it has not expired yet,
// and reports whether the application holds. Checking and counting happen
// together: under concurrent requests, an override limited to n applications
// is applied at most n times.
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

// Reset restarts the override's time-to-live clock and zeroes its application
// count, reactivating it if it had expired. It does not change the document.
// Reports whether the override exists in the configuration in effect.
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

// LiveState is an override's live state, as the API exposes it.
type LiveState struct {
	Route    string `json:"route"`
	Override string `json:"override"`
	// Enabled is the document's value.
	Enabled bool `json:"enabled"`
	// Active reports that the override takes part in the selection right now:
	// enabled and not expired.
	Active bool `json:"active"`
	// Expired is nil, "ttl" or "applications".
	Expired *string `json:"expired"`
	// RegisteredAt is the start of the time-to-live clock.
	RegisteredAt time.Time `json:"registeredAt"`
	// TTLRemainingMs is what is left of the time to live: nil with no time to
	// live, 0 once expired.
	TTLRemainingMs *int64 `json:"ttlRemainingMs"`
	// Applications counts the applications since RegisteredAt.
	Applications int64 `json:"applications"`
	// MaxApplications is the declared limit, nil with no limit.
	MaxApplications *int       `json:"maxApplications"`
	LastAppliedAt   *time.Time `json:"lastAppliedAt"`
}

func (e *entry) status(route, name string, now time.Time) LiveState {
	s := LiveState{
		Route:        route,
		Override:     name,
		Enabled:      e.enabled,
		RegisteredAt: e.registeredAt.UTC(),
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
		at := e.lastAppliedAt.UTC()
		s.LastAppliedAt = &at
	}
	return s
}

// State returns the live state of the override with the given name in the
// route.
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

// States returns the live state of every override in the configuration in
// effect, route by route in precedence order, and the instant of the query.
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
	return slices.Clip(out), now.UTC()
}
