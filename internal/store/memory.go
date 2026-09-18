package store

import (
	"context"
	"slices"
	"sync"

	"github.com/gamerjp64/gateway/internal/exchange"
)

// Memory guarda as trocas em memória, com capacidade fixa: ao atingi-la, a
// troca mais antiga é descartada para dar lugar à nova. As trocas ficam em
// ordem crescente de chave (orderKey), da mais antiga para a mais nova.
type Memory struct {
	mu       sync.RWMutex
	capacity int
	items    []memEntry
	byID     map[string]orderKey
	next     uint64 // posição de registro da próxima troca
	epoch    uint64 // avança a cada limpeza
}

type memEntry struct {
	key orderKey
	ex  exchange.Exchange
}

func NewMemory(capacity int) *Memory {
	if capacity < 1 {
		capacity = 1
	}
	return &Memory{capacity: capacity, byID: map[string]orderKey{}}
}

// search devolve a posição da primeira troca com chave não anterior a k.
func (m *Memory) search(k orderKey) int {
	i, _ := slices.BinarySearchFunc(m.items, k, func(e memEntry, k orderKey) int { return e.key.compare(k) })
	return i
}

func (m *Memory) Record(_ context.Context, e *exchange.Exchange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[e.ID]; ok {
		return ErrDuplicateID
	}
	if len(m.items) == m.capacity {
		// Sai a mais antiga já registrada, para que a nova sempre entre.
		delete(m.byID, m.items[0].ex.ID)
		m.items[0] = memEntry{}
		m.items = m.items[1:]
	}
	k := keyOf(e, m.next)
	m.next++
	m.items = slices.Insert(m.items, m.search(k), memEntry{key: k, ex: *e})
	m.byID[e.ID] = k
	return nil
}

func (m *Memory) List(_ context.Context, f exchange.Filter, p Page) (ListResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	limit := NormalizeLimit(p.Limit)
	start := len(m.items) - 1
	if p.Cursor != "" {
		c, err := decodeCursor(p.Cursor)
		if err != nil {
			return ListResult{}, err
		}
		if c.Epoch != m.epoch {
			return ListResult{}, nil // cursor anterior à última limpeza
		}
		// O cursor é a chave da última troca entregue; segue-se da anterior
		// a ela, ainda que ela já tenha saído do histórico.
		start = m.search(c.Key) - 1
	}
	var res ListResult
	var last orderKey
	for i := start; i >= 0; i-- {
		en := &m.items[i]
		if !f.Match(&en.ex) {
			continue
		}
		if len(res.Items) == limit {
			// Há mais uma troca que casa: a página continua depois da última entregue.
			res.Next = encodeCursor(m.epoch, last)
			break
		}
		res.Items = append(res.Items, en.ex)
		last = en.key
	}
	return res, nil
}

func (m *Memory) Get(_ context.Context, id string) (exchange.Exchange, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.byID[id]
	if !ok {
		return exchange.Exchange{}, ErrNotFound
	}
	return m.items[m.search(k)].ex, nil
}

func (m *Memory) Neighbor(_ context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	k, ok := m.byID[id]
	if !ok {
		return exchange.Exchange{}, ErrNotFound
	}
	i := m.search(k)
	step := -1
	if d == Newer {
		step = 1
	}
	for j := i + step; j >= 0 && j < len(m.items); j += step {
		if en := &m.items[j]; f.Match(&en.ex) {
			return en.ex, nil
		}
	}
	return exchange.Exchange{}, ErrNoMore
}

func (m *Memory) Clear(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = nil
	m.byID = map[string]orderKey{}
	m.epoch++
	return nil
}

func (m *Memory) Close() error { return nil }
