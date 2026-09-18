package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
)

// Switchable é o histórico em uso pelo processo: um Store que delega ao
// backend atual e permite trocá-lo a quente. A troca segue o protocolo das
// portas: o novo backend é inicializado antes, o ponteiro é trocado e só
// então o antigo é fechado. O histórico não é migrado; as trocas antigas
// ficam intactas no backend anterior.
type Switchable struct {
	cur atomic.Pointer[backend]
	// swap serializa as trocas entre si; as operações não o tomam.
	swap sync.Mutex
}

// backend é um Store em uso, com o nome que o selecionou. inUse conta as
// operações em curso: o fechamento espera todas terminarem, para que uma
// gravação iniciada antes da troca nunca encontre o backend já fechado.
type backend struct {
	name  string
	s     Store
	inUse sync.RWMutex
}

// NewSwitchable começa com o backend s, identificado por name.
func NewSwitchable(name string, s Store) *Switchable {
	w := &Switchable{}
	w.cur.Store(&backend{name: name, s: s})
	return w
}

// acquire devolve o backend atual reservado para uma operação. Se uma troca
// acontece entre a leitura do ponteiro e a reserva, tenta de novo com o novo.
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

// Backend informa o nome do backend em uso.
func (w *Switchable) Backend() string { return w.cur.Load().name }

// Swap passa a usar next, identificado por name, e fecha o anterior depois
// que as operações em curso nele terminam. next já precisa estar
// inicializado. O erro, quando há, é do fechamento do anterior; a troca vale
// mesmo assim.
func (w *Switchable) Swap(_ context.Context, name string, next Store) error {
	w.swap.Lock()
	defer w.swap.Unlock()
	old := w.cur.Swap(&backend{name: name, s: next})
	old.inUse.Lock()
	defer old.inUse.Unlock()
	if err := old.s.Close(); err != nil {
		return fmt.Errorf("fechando o backend do histórico anterior %s: %w", old.name, err)
	}
	return nil
}

// Switch inicializa o backend descrito por s e troca para ele. Se a
// inicialização falhar, a troca é recusada com o backend e a causa, e o
// backend atual segue em uso.
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

// Close fecha o backend atual, esperando as operações em curso.
func (w *Switchable) Close() error {
	w.swap.Lock()
	defer w.swap.Unlock()
	b := w.cur.Load()
	b.inUse.Lock()
	defer b.inUse.Unlock()
	return b.s.Close()
}
