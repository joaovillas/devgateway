package store

import (
	"context"
	"strconv"
	"sync"

	"github.com/gamerjp64/gateway/internal/exchange"
)

// Memory guarda as trocas num anel de capacidade fixa: ao atingi-la, a troca
// mais antiga é descartada para dar lugar à nova.
type Memory struct {
	mu    sync.RWMutex
	ring  []memEntry
	head  int // posição da troca mais antiga
	count int
	next  uint64 // chave de ordem da próxima troca
	byID  map[string]uint64
}

type memEntry struct {
	order uint64
	ex    exchange.Exchange
}

func NewMemory(capacity int) *Memory {
	if capacity < 1 {
		capacity = 1
	}
	return &Memory{ring: make([]memEntry, capacity), byID: map[string]uint64{}}
}

func (m *Memory) Record(_ context.Context, e *exchange.Exchange) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[e.ID]; ok {
		return ErrDuplicateID
	}
	if m.count == len(m.ring) {
		delete(m.byID, m.ring[m.head].ex.ID)
		m.head = (m.head + 1) % len(m.ring)
		m.count--
	}
	pos := (m.head + m.count) % len(m.ring)
	m.ring[pos] = memEntry{order: m.next, ex: *e}
	m.byID[e.ID] = m.next
	m.next++
	m.count++
	return nil
}

// at devolve a i-ésima troca, da mais antiga (0) para a mais nova.
func (m *Memory) at(i int) *memEntry { return &m.ring[(m.head+i)%len(m.ring)] }

// indexOf localiza a posição de uma chave de ordem. As chaves no anel são
// contíguas, então a posição é aritmética.
func (m *Memory) indexOf(order uint64) (int, bool) {
	if m.count == 0 {
		return 0, false
	}
	first := m.at(0).order
	if order < first || order >= first+uint64(m.count) {
		return 0, false
	}
	return int(order - first), true
}

func (m *Memory) List(_ context.Context, f exchange.Filter, p Page) (ListResult, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	limit := NormalizeLimit(p.Limit)
	start := m.count - 1
	if p.Cursor != "" {
		c, err := strconv.ParseUint(p.Cursor, 10, 64)
		if err != nil {
			return ListResult{}, ErrBadCursor
		}
		// O cursor é a chave da última troca entregue; segue-se da anterior.
		// Se ela já saiu do anel, as restantes também saíram.
		i, ok := m.indexOf(c)
		if !ok {
			if m.count > 0 && c > m.at(m.count-1).order {
				i = m.count
			} else {
				return ListResult{}, nil
			}
		}
		start = i - 1
	}
	var res ListResult
	var last uint64
	for i := start; i >= 0; i-- {
		en := m.at(i)
		if !f.Match(&en.ex) {
			continue
		}
		if len(res.Items) == limit {
			// Há mais uma troca que casa: a página continua depois da última entregue.
			res.Next = strconv.FormatUint(last, 10)
			break
		}
		res.Items = append(res.Items, en.ex)
		last = en.order
	}
	return res, nil
}

func (m *Memory) Get(_ context.Context, id string) (exchange.Exchange, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	order, ok := m.byID[id]
	if !ok {
		return exchange.Exchange{}, ErrNotFound
	}
	i, _ := m.indexOf(order)
	return m.at(i).ex, nil
}

func (m *Memory) Neighbor(_ context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	order, ok := m.byID[id]
	if !ok {
		return exchange.Exchange{}, ErrNotFound
	}
	i, _ := m.indexOf(order)
	step := -1
	if d == Newer {
		step = 1
	}
	for j := i + step; j >= 0 && j < m.count; j += step {
		if en := m.at(j); f.Match(&en.ex) {
			return en.ex, nil
		}
	}
	return exchange.Exchange{}, ErrNoMore
}

func (m *Memory) Clear(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.ring)
	m.head, m.count = 0, 0
	m.byID = map[string]uint64{}
	return nil
}

func (m *Memory) Close() error { return nil }
