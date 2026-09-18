package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
	"github.com/gamerjp64/gateway/internal/store/storetest"
)

func TestMemoryContract(t *testing.T) {
	storetest.Run(t, storetest.Factory{
		New: func(storetest.TB) store.Store { return store.NewMemory(1000) },
	})
}

// Requirement: Armazenamento plugável — Capacidade excedida em memória
func TestMemoryCapacityExceeded(t *testing.T) {
	ctx := context.Background()
	m := store.NewMemory(100)
	for i := range 101 {
		m.Record(ctx, &exchange.Exchange{ID: fmt.Sprint(i), Status: 200})
	}
	if _, err := m.Get(ctx, "0"); err != store.ErrNotFound {
		t.Fatalf("a troca mais antiga deveria ter saído: %v", err)
	}
	if _, err := m.Get(ctx, "100"); err != nil {
		t.Fatalf("a troca nova deveria estar registrada: %v", err)
	}
	res, _ := m.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if len(res.Items) != 100 || res.Items[0].ID != "100" || res.Items[99].ID != "1" {
		t.Fatalf("histórico deveria ter 100 trocas, da 100 à 1: %d", len(res.Items))
	}
	if _, err := m.Neighbor(ctx, "1", store.Older, exchange.Filter{}); err != store.ErrNoMore {
		t.Fatalf("a 1 agora é a mais antiga: %v", err)
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
		m.Record(ctx, &exchange.Exchange{ID: fmt.Sprint(i)}) // expulsa 0 a 4
	}
	res, err := m.List(ctx, exchange.Filter{}, store.Page{Limit: 10, Cursor: page.Next})
	if err != nil || len(res.Items) != 2 || res.Items[0].ID != "6" || res.Items[1].ID != "5" {
		t.Fatalf("continuação deveria trazer 6 e 5, as que restam abaixo de 7: %v", res.Items)
	}
}

// empty é a implementação vazia: não guarda nada e nunca falha.
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

// A bateria precisa reprovar a implementação vazia em todos os casos; um
// caso que passasse contra ela não estaria verificando nada.
func TestContractRejectsEmptyImplementation(t *testing.T) {
	f := storetest.Factory{
		New:    func(storetest.TB) store.Store { return empty{} },
		Reopen: func(storetest.TB, store.Store) store.Store { return empty{} },
	}
	for _, c := range storetest.Cases {
		failed, _ := storetest.RunCase(c, f, t.TempDir())
		if !failed {
			t.Errorf("caso %s passou contra a implementação vazia", c.Name)
		}
	}
}
