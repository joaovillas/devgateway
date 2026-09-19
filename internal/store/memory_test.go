package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
	"github.com/joaovillas/devgateway/internal/store/storetest"
)

func TestMemoryContract(t *testing.T) {
	storetest.Run(t, storetest.Factory{
		New: func(storetest.TB) store.Store { return store.NewMemory(1000) },
	})
}

// Requirement: Pluggable storage - capacity exceeded in memory
func TestMemoryCapacityExceeded(t *testing.T) {
	ctx := context.Background()
	m := store.NewMemory(100)
	for i := range 101 {
		m.Record(ctx, &exchange.Exchange{ID: fmt.Sprint(i), Status: 200})
	}
	if _, err := m.Get(ctx, "0"); err != store.ErrNotFound {
		t.Fatalf("the oldest exchange should have been evicted: %v", err)
	}
	if _, err := m.Get(ctx, "100"); err != nil {
		t.Fatalf("the new exchange should have been recorded: %v", err)
	}
	res, _ := m.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if len(res.Items) != 100 || res.Items[0].ID != "100" || res.Items[99].ID != "1" {
		t.Fatalf("the history should hold 100 exchanges, from 100 down to 1: %d", len(res.Items))
	}
	if _, err := m.Neighbor(ctx, "1", store.Older, exchange.Filter{}); err != store.ErrNoMore {
		t.Fatalf("1 is now the oldest one: %v", err)
	}
}

func TestMemoryCursorSurvivesEviction(t *testing.T) {
	ctx := context.Background()
	m := store.NewMemory(10)
	for i := range 10 {
		m.Record(ctx, &exchange.Exchange{ID: fmt.Sprint(i)})
	}
	page, _ := m.List(ctx, exchange.Filter{}, store.Page{Limit: 3}) // 9, 8, 7
	for i := 10; i < 15; i++ {
		m.Record(ctx, &exchange.Exchange{ID: fmt.Sprint(i)}) // evicts 0 through 4
	}
	res, err := m.List(ctx, exchange.Filter{}, store.Page{Limit: 10, Cursor: page.Next})
	if err != nil || len(res.Items) != 2 || res.Items[0].ID != "6" || res.Items[1].ID != "5" {
		t.Fatalf("the continuation should bring 6 and 5, the ones left below 7: %v", res.Items)
	}
}

// empty is the do-nothing implementation: it stores nothing and never fails.
type empty struct{}

func (empty) Record(context.Context, *exchange.Exchange) error { return nil }
func (empty) List(context.Context, exchange.Filter, store.Page) (store.ListResult, error) {
	return store.ListResult{}, nil
}
func (empty) Get(context.Context, string) (exchange.Exchange, error) { return exchange.Exchange{}, nil }
func (empty) Neighbor(context.Context, string, store.Direction, exchange.Filter) (exchange.Exchange, error) {
	return exchange.Exchange{}, nil
}
func (empty) Clear(context.Context) error { return nil }
func (empty) Close() error                { return nil }

// The suite has to fail the empty implementation on every case; a case that
// passed against it would not be checking anything.
func TestContractRejectsEmptyImplementation(t *testing.T) {
	f := storetest.Factory{
		New:    func(storetest.TB) store.Store { return empty{} },
		Reopen: func(storetest.TB, store.Store) store.Store { return empty{} },
	}
	for _, c := range storetest.Cases {
		failed, _ := storetest.RunCase(c, f, t.TempDir())
		if !failed {
			t.Errorf("case %s passed against the empty implementation", c.Name)
		}
	}
}
