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

	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
	"github.com/joaovillas/devgateway/internal/store/storetest"
)

func TestSwitchableContract(t *testing.T) {
	storetest.Run(t, storetest.Factory{
		New: func(storetest.TB) store.Store {
			return store.NewSwitchable(config.BackendMemory, store.NewMemory(1000))
		},
	})
}

// closeSpy records the close and flags any use after it.
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

// writeWhile records exchanges from several goroutines until stop is closed,
// and returns how many were recorded successfully.
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
					t.Errorf("a write during the swap failed: %v", err)
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

// Requirement: Reload without restart - history backend swapped while running
func TestSwitchMemoryToSQLiteHot(t *testing.T) {
	ctx := context.Background()
	mem := &closeSpy{Store: store.NewMemory(1 << 20)}
	w := store.NewSwitchable(config.BackendMemory, mem)
	t.Cleanup(func() { w.Close() })
	recordIDs(t, w, "old")

	stop := make(chan struct{})
	wait := writeWhile(t, w, stop)
	// Let the in-memory writes get going before the swap.
	for mem.recorded.Load() < 50 {
		runtime.Gosched()
	}
	path := filepath.Join(t.TempDir(), "history.db")
	sqlitePath := settingsFor(t, "", map[string]string{
		"GATEWAY_HISTORY_BACKEND": config.BackendSQLite,
		"GATEWAY_HISTORY_PATH":    path,
	})
	if err := w.Switch(ctx, sqlitePath); err != nil {
		t.Fatalf("swap to SQLite: %v", err)
	}
	// And keep going after it, now on SQLite.
	afterSwap := countAll(t, w)
	for countAll(t, w) < afterSwap+50 {
	}
	close(stop)
	written := wait()

	if w.Backend() != config.BackendSQLite {
		t.Fatalf("the backend in use should be sqlite, it is %q", w.Backend())
	}
	if mem.closeHits.Load() != 1 {
		t.Fatalf("the previous backend should be closed once, it was %d", mem.closeHits.Load())
	}
	if n := mem.afterUse.Load(); n != 0 {
		t.Fatalf("%d writes reached the previous backend after it was closed", n)
	}
	// No write lost: each one is in exactly one of the two.
	inMem, inSQLite := countAll(t, mem.Store)-1, countAll(t, w)
	if inMem+inSQLite != written || inSQLite == 0 {
		t.Fatalf("written %d, in memory %d, in SQLite %d", written, inMem, inSQLite)
	}
	// No migration: the earlier exchange stays only in memory.
	if _, err := w.Get(ctx, "old"); err != store.ErrNotFound {
		t.Fatalf("the previous history should not be migrated: %v", err)
	}
	if _, err := mem.Store.Get(ctx, "old"); err != nil {
		t.Fatalf("the previous history should stay intact: %v", err)
	}
	// The following exchanges are in the SQLite file.
	recordIDs(t, w, "new")
	w.Close()
	reopened := openSQLite(t, path)
	if _, err := reopened.Get(ctx, "new"); err != nil {
		t.Fatalf("an exchange recorded after the swap should be in SQLite: %v", err)
	}
	if got := countAll(t, reopened); got != inSQLite+1 {
		t.Fatalf("SQLite should hold %d exchanges, it holds %d", inSQLite+1, got)
	}
}

// Requirement: Reload without restart - an unavailable new backend keeps the
// current one
func TestSwitchToUnavailableKeepsCurrent(t *testing.T) {
	ctx := context.Background()
	mem := &closeSpy{Store: store.NewMemory(1 << 20)}
	w := store.NewSwitchable(config.BackendMemory, mem)
	t.Cleanup(func() { w.Close() })
	recordIDs(t, w, "before")

	stop := make(chan struct{})
	wait := writeWhile(t, w, stop)
	path := fileAsDir(t, "history.db")
	err := w.Switch(ctx, config.Settings{HistoryBackend: config.BackendSQLite, HistoryPath: path})
	close(stop)
	written := wait()
	if err == nil {
		t.Fatalf("swapping to a backend that does not open should be rejected")
	}
	if !strings.Contains(err.Error(), config.BackendSQLite) || !strings.Contains(err.Error(), path) {
		t.Fatalf("the rejection should name the backend and the cause: %v", err)
	}
	if cause := new(*fs.PathError); !errors.As(err, cause) {
		t.Fatalf("the rejection should wrap the filesystem cause: %v", err)
	}
	if w.Backend() != config.BackendMemory || mem.closeHits.Load() != 0 {
		t.Fatalf("the current backend should stay in use: %q, closed %d time(s)", w.Backend(), mem.closeHits.Load())
	}
	recordIDs(t, w, "after")
	for _, id := range []string{"before", "after"} {
		if _, err := mem.Store.Get(ctx, id); err != nil {
			t.Fatalf("%s should be in the current backend: %v", id, err)
		}
	}
	if got := countAll(t, w); got != written+2 {
		t.Fatalf("%d written during the attempt, plus 2; the history holds %d", written, got)
	}
}
