package store_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
	"github.com/gamerjp64/gateway/internal/store/storetest"
)

func TestSwitchableContract(t *testing.T) {
	storetest.Run(t, storetest.Factory{
		New: func(storetest.TB) store.Store {
			return store.NewSwitchable(config.BackendMemory, store.NewMemory(1000))
		},
	})
}

// closeSpy registra o fechamento e acusa qualquer uso depois dele.
type closeSpy struct {
	store.Store
	closed    atomic.Bool
	afterUse  atomic.Int64
	recorded  atomic.Int64
	closeHits atomic.Int64
}

func (c *closeSpy) Record(ctx context.Context, e *exchange.Exchange) error {
	if c.closed.Load() {
		c.afterUse.Add(1)
	}
	c.recorded.Add(1)
	return c.Store.Record(ctx, e)
}

func (c *closeSpy) Close() error {
	c.closeHits.Add(1)
	c.closed.Store(true)
	return c.Store.Close()
}

// writeWhile grava trocas de vários goroutines até stop fechar, e devolve
// quantas foram gravadas com sucesso.
func writeWhile(t *testing.T, w store.Store, stop <-chan struct{}) func() int64 {
	var wg sync.WaitGroup
	var ok atomic.Int64
	var seq atomic.Int64
	for range 8 {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				e := &exchange.Exchange{ID: fmt.Sprintf("W%06d", seq.Add(1)), Method: "GET", Path: "/x", Status: 200}
				if err := w.Record(context.Background(), e); err != nil {
					t.Errorf("gravação durante a troca falhou: %v", err)
					return
				}
				ok.Add(1)
			}
		})
	}
	return func() int64 { wg.Wait(); return ok.Load() }
}

func countAll(t *testing.T, s store.Store) int64 {
	t.Helper()
	var n int64
	cursor := ""
	for {
		res, err := s.List(context.Background(), exchange.Filter{}, store.Page{Limit: store.MaxLimit, Cursor: cursor})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		n += int64(len(res.Items))
		if cursor = res.Next; cursor == "" {
			return n
		}
	}
}

// Requirement: Recarga sem reinício — Backend do histórico trocado a quente
func TestSwitchMemoryToSQLiteHot(t *testing.T) {
	ctx := context.Background()
	mem := &closeSpy{Store: store.NewMemory(1 << 20)}
	w := store.NewSwitchable(config.BackendMemory, mem)
	t.Cleanup(func() { w.Close() })
	recordIDs(t, w, "antiga")

	stop := make(chan struct{})
	wait := writeWhile(t, w, stop)
	// Deixa as gravações em memória começarem antes da troca.
	for mem.recorded.Load() < 50 {
		runtime.Gosched()
	}
	path := filepath.Join(t.TempDir(), "history.db")
	sqlitePath := settingsFor(t, "", map[string]string{
		"GATEWAY_HISTORY_BACKEND": config.BackendSQLite,
		"GATEWAY_HISTORY_PATH":    path,
	})
	if err := w.Switch(ctx, sqlitePath); err != nil {
		t.Fatalf("troca para SQLite: %v", err)
	}
	// E continuar depois dela, já no SQLite.
	afterSwap := countAll(t, w)
	for countAll(t, w) < afterSwap+50 {
	}
	close(stop)
	written := wait()

	if w.Backend() != config.BackendSQLite {
		t.Fatalf("backend em uso deveria ser sqlite, é %q", w.Backend())
	}
	if mem.closeHits.Load() != 1 {
		t.Fatalf("o backend anterior deveria ser fechado uma vez, foi %d", mem.closeHits.Load())
	}
	if n := mem.afterUse.Load(); n != 0 {
		t.Fatalf("%d gravações chegaram ao backend anterior depois de fechado", n)
	}
	// Nenhuma gravação perdida: cada uma está em exatamente um dos dois.
	inMem, inSQLite := countAll(t, mem.Store)-1, countAll(t, w)
	if inMem+inSQLite != written || inSQLite == 0 {
		t.Fatalf("gravadas %d, em memória %d, no SQLite %d", written, inMem, inSQLite)
	}
	// Sem migração: a troca anterior segue só na memória.
	if _, err := w.Get(ctx, "antiga"); err != store.ErrNotFound {
		t.Fatalf("o histórico anterior não deveria ser migrado: %v", err)
	}
	if _, err := mem.Store.Get(ctx, "antiga"); err != nil {
		t.Fatalf("o histórico anterior deveria seguir intacto: %v", err)
	}
	// As trocas seguintes estão no arquivo do SQLite.
	recordIDs(t, w, "nova")
	w.Close()
	reopened := openSQLite(t, path)
	if _, err := reopened.Get(ctx, "nova"); err != nil {
		t.Fatalf("troca registrada após a troca deveria estar no SQLite: %v", err)
	}
	if got := countAll(t, reopened); got != inSQLite+1 {
		t.Fatalf("SQLite deveria ter %d trocas, tem %d", inSQLite+1, got)
	}
}

// Requirement: Recarga sem reinício — Backend novo indisponível preserva o atual
func TestSwitchToUnavailableKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	mem := &closeSpy{Store: store.NewMemory(1 << 20)}
	w := store.NewSwitchable(config.BackendMemory, mem)
	t.Cleanup(func() { w.Close() })
	recordIDs(t, w, "antes")

	stop := make(chan struct{})
	wait := writeWhile(t, w, stop)
	path := fileAsDir(t, "history.db")
	err := w.Switch(ctx, config.Settings{HistoryBackend: config.BackendSQLite, HistoryPath: path})
	close(stop)
	written := wait()
	if err == nil {
		t.Fatalf("a troca para um backend que não abre deveria ser recusada")
	}
	if !strings.Contains(err.Error(), config.BackendSQLite) || !strings.Contains(err.Error(), path) {
		t.Fatalf("a recusa deveria nomear o backend e a causa: %v", err)
	}
	if cause := new(*fs.PathError); !errors.As(err, cause) {
		t.Fatalf("a recusa deveria encadear a causa do sistema de arquivos: %v", err)
	}
	if w.Backend() != config.BackendMemory || mem.closeHits.Load() != 0 {
		t.Fatalf("o backend atual deveria seguir em uso: %q, fechado %d vez(es)", w.Backend(), mem.closeHits.Load())
	}
	recordIDs(t, w, "depois")
	for _, id := range []string{"antes", "depois"} {
		if _, err := mem.Store.Get(ctx, id); err != nil {
			t.Fatalf("%s deveria estar no backend atual: %v", id, err)
		}
	}
	if got := countAll(t, w); got != written+2 {
		t.Fatalf("gravadas %d durante a tentativa, mais 2; histórico tem %d", written, got)
	}
}
