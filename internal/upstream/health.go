// Package upstream tracks each upstream's recent availability from the
// forwarding attempts made on the traffic port. The gateway does not probe
// the upstreams: the state comes only from the request path, and is counted
// independently of history recording.
package upstream

import (
	"sync"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
)

const (
	// Window is how many recent attempts each upstream keeps.
	Window = 20
	// DownAfter is how many consecutive failures, the most recent ones, mark
	// an upstream as down.
	DownAfter = 3
)

// Status is an upstream's availability.
type Status string

const (
	// StatusUp: there has been at least one attempt and the DownAfter most
	// recent ones did not all fail (with fewer than DownAfter attempts, it is
	// always this one).
	StatusUp Status = "up"
	// StatusDown: the DownAfter most recent attempts got no response.
	StatusDown Status = "down"
	// StatusUnknown: no attempt since startup, or since the upstream was
	// first declared.
	StatusUnknown Status = "unknown"
)

// Recent summarizes the latest attempts.
type Recent struct {
	Attempts int `json:"attempts"`
	Failures int `json:"failures"`
}

// Item is an upstream's availability, as GET /api/upstreams returns it.
type Item struct {
	Upstream      string     `json:"upstream"`
	Routes        []string   `json:"routes"`
	Status        Status     `json:"status"`
	Recent        Recent     `json:"recent"`
	LastSuccessAt *time.Time `json:"lastSuccessAt"`
	LastFailureAt *time.Time `json:"lastFailureAt"`
	LastError     string     `json:"lastError"`
}

// record keeps an upstream's recent attempts in a ring.
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

// status applies the rule: no attempt, unknown; the DownAfter most recent
// ones with no response, down; otherwise, up.
func (r *record) status() Status {
	if r == nil || r.n == 0 {
		return StatusUnknown
	}
	if r.n < DownAfter {
		// Too few attempts to call it down; the failures show up in Recent.
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

// Health is the recent availability of the upstreams. It is safe for
// concurrent use and cheap on the request path: recording an attempt takes a
// mutex and writes into a fixed-size ring.
type Health struct {
	mu  sync.Mutex
	by  map[string]*record
	now func() time.Time
}

// New creates empty tracking: every upstream starts out unknown.
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

// Success records that the upstream responded, whatever the status: a 5xx
// from the upstream itself is a response.
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

// Failure records an attempt with no response from the upstream: connection
// refused, connection timeout, or the route timeout running out before the
// response. A client giving up is not an upstream failure and does not go
// through here.
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

// Report returns the availability of every upstream declared by the routes,
// in precedence order, along with the routes that point to it. Routes
// without an upstream do not show up. Whatever was recorded for an upstream
// that is no longer declared is dropped: if it comes back, it comes back
// unknown.
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
