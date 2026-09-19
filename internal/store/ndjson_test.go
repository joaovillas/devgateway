package store_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gamerjp64/devgateway/internal/exchange"
	"github.com/gamerjp64/devgateway/internal/store"
	"github.com/gamerjp64/devgateway/internal/store/storetest"
)

func openNDJSON(t storetest.TB, path string) store.Store {
	t.Helper()
	s, err := store.OpenNDJSON(path)
	if err != nil {
		t.Fatalf("OpenNDJSON: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestNDJSONContract(t *testing.T) {
	storetest.Run(t, storetest.Factory{
		New: func(t storetest.TB) store.Store {
			return openNDJSON(t, filepath.Join(t.TempDir(), "history.ndjson"))
		},
		Reopen: func(t storetest.TB, s store.Store) store.Store {
			path := s.(*store.NDJSON).Path()
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			return openNDJSON(t, path)
		},
	})
}

func recordIDs(t *testing.T, s store.Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := s.Record(context.Background(), &exchange.Exchange{ID: id, Status: 200}); err != nil {
			t.Fatalf("Record(%s): %v", id, err)
		}
	}
}

func listIDs(t *testing.T, s store.Store) []string {
	t.Helper()
	res, err := s.List(context.Background(), exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var out []string
	for _, e := range res.Items {
		out = append(out, e.ID)
	}
	return out
}

func appendRaw(t *testing.T, path, raw string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(raw); err != nil {
		t.Fatal(err)
	}
}

// An interrupted write leaves the last line without a terminator: it is
// dropped when the file is opened, and the next record starts on a clean
// line.
func TestNDJSONDiscardsIncompleteLastLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.ndjson")
	s, err := store.OpenNDJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	recordIDs(t, s, "a", "b")
	s.Close()
	appendRaw(t, path, `{"id":"interrupted","status":2`)

	s2 := openNDJSON(t, path)
	if got := listIDs(t, s2); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("b and a should be left, got %v", got)
	}
	if _, err := s2.Get(context.Background(), "interrupted"); err != store.ErrNotFound {
		t.Fatalf("the incomplete line should not be read: %v", err)
	}
	recordIDs(t, s2, "c")
	s2.Close()

	s3 := openNDJSON(t, path)
	if got := listIDs(t, s3); len(got) != 3 || got[0] != "c" {
		t.Fatalf("an exchange recorded after the drop should survive a restart: %v", got)
	}
}

// An unreadable line in the middle of the file is skipped without losing its
// neighbors, and navigation steps right over it.
func TestNDJSONSkipsUnreadableMiddleLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.ndjson")
	s, err := store.OpenNDJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	recordIDs(t, s, "a")
	s.Close()
	appendRaw(t, path, "this is not json\n")
	s, err = store.OpenNDJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	recordIDs(t, s, "b")
	s.Close()

	s2 := openNDJSON(t, path)
	if got := listIDs(t, s2); len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Fatalf("b and a should be left, got %v", got)
	}
	e, err := s2.Neighbor(context.Background(), "b", store.Older, exchange.Filter{})
	if err != nil || e.ID != "a" {
		t.Fatalf("the one before b should be a: %q %v", e.ID, err)
	}
	if _, err := s2.Get(context.Background(), "b"); err != nil {
		t.Fatalf("the exchange after the unreadable line should be readable: %v", err)
	}
}
