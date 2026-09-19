package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/exchange"
)

// Switchable is the history in use by the process: a Store that delegates to
// the current backend and allows swapping it while running. The swap follows
// the same protocol as the ports: the new backend is initialized first, the
// pointer is swapped, and only then is the old one closed. The history is
// not migrated; the old exchanges stay untouched in the previous backend.
type Switchable struct {
	cur atomic.Pointer[backend]
	// swap serializes swaps against each other; operations do not take it.
	swap sync.Mutex
}

// backend is a Store in use, along with the name that selected it. inUse
// counts the operations in flight: closing waits for all of them to finish,
// so a write that started before the swap never finds the backend already
// closed.
type backend struct {
	name  string
	s     Store
	inUse sync.RWMutex
}

// NewSwitchable starts out with backend s, identified by name.
func NewSwitchable(name string, s Store) *Switchable {
	w := &Switchable{}
	w.cur.Store(&backend{name: name, s: s})
	return w
}

// acquire returns the current backend reserved for one operation. If a swap
// happens between reading the pointer and reserving it, it retries with the
// new one.
func (w *Switchable) acquire() *backend {
	for {
		b := w.cur.Load()
		b.inUse.RLock()
		if w.cur.Load() == b {
			return b
		}
		b.inUse.RUnlock()
	}
}

// Backend reports the name of the backend in use.
func (w *Switchable) Backend() string { return w.cur.Load().name }

// Swap starts using next, identified by name, and closes the previous one
// after the operations in flight on it finish. next must already be
// initialized. The error, when there is one, comes from closing the previous
// backend; the swap took effect regardless.
func (w *Switchable) Swap(_ context.Context, name string, next Store) error {
	w.swap.Lock()
	defer w.swap.Unlock()
	old := w.cur.Swap(&backend{name: name, s: next})
	old.inUse.Lock()
	defer old.inUse.Unlock()
	if err := old.s.Close(); err != nil {
		return fmt.Errorf("closing the previous history backend %s: %w", old.name, err)
	}
	return nil
}

// Switch initializes the backend described by s and swaps to it. If
// initialization fails, the swap is rejected naming the backend and the
// cause, and the current backend stays in use.
func (w *Switchable) Switch(ctx context.Context, s config.Settings) error {
	next, err := Open(s)
	if err != nil {
		return err
	}
	return w.Swap(ctx, BackendName(s), next)
}

func (w *Switchable) Record(ctx context.Context, e *exchange.Exchange) error {
	b := w.acquire()
	defer b.inUse.RUnlock()
	return b.s.Record(ctx, e)
}

func (w *Switchable) List(ctx context.Context, f exchange.Filter, p Page) (ListResult, error) {
	b := w.acquire()
	defer b.inUse.RUnlock()
	return b.s.List(ctx, f, p)
}

func (w *Switchable) Get(ctx context.Context, id string) (exchange.Exchange, error) {
	b := w.acquire()
	defer b.inUse.RUnlock()
	return b.s.Get(ctx, id)
}

func (w *Switchable) Neighbor(ctx context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error) {
	b := w.acquire()
	defer b.inUse.RUnlock()
	return b.s.Neighbor(ctx, id, d, f)
}

func (w *Switchable) Clear(ctx context.Context) error {
	b := w.acquire()
	defer b.inUse.RUnlock()
	return b.s.Clear(ctx)
}

// Close closes the current backend, waiting for the operations in flight.
func (w *Switchable) Close() error {
	w.swap.Lock()
	defer w.swap.Unlock()
	b := w.cur.Load()
	b.inUse.Lock()
	defer b.inUse.Unlock()
	return b.s.Close()
}
