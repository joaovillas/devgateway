package store

import (
	"context"
	"slices"
	"sync"

	"github.com/joaovillas/devgateway/internal/exchange"
)

// Memory keeps exchanges in memory with a fixed capacity: once it is
// reached, the oldest exchange is dropped to make room for the new one.
// Exchanges are kept in ascending key (orderKey) order, oldest to newest.
type Memory struct {
	mu       sync.RWMutex
	capacity int
	items    []memEntry
	byID     map[string]orderKey
	next     uint64 // record position of the next exchange
	epoch    uint64 // bumped on every clear
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

// search returns the position of the first exchange whose key is not before k.
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
		// The oldest one already recorded goes out, so the new one always
		// gets in.
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
			return ListResult{}, nil // cursor predates the last clear
		}
		// The cursor is the key of the last exchange delivered; the listing
		// resumes from the one before it, even if that one has already been
		// evicted from the history.
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
			// One more exchange matches: the page continues after the last
			// one delivered.
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
