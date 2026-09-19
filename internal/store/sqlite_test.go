package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/gamerjp64/devgateway/internal/exchange"
	"github.com/gamerjp64/devgateway/internal/store"
	"github.com/gamerjp64/devgateway/internal/store/storetest"
)

func openSQLite(t storetest.TB, path string) *store.SQLite {
	t.Helper()
	s, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSQLiteContract(t *testing.T) {
	storetest.Run(t, storetest.Factory{
		New: func(t storetest.TB) store.Store {
			return openSQLite(t, filepath.Join(t.TempDir(), "history.db"))
		},
		Reopen: func(t storetest.TB, s store.Store) store.Store {
			path := s.(*store.SQLite).Path()
			if err := s.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			return openSQLite(t, path)
		},
	})
}

// The SQL filters have to match exchange.Filter.Match exactly, including the
// cases where plain SQL would diverge: case folding beyond ASCII, the zero
// instant and the edges of the time window.
func TestSQLiteFilterMatchesReferenceSemantics(t *testing.T) {
	ctx := context.Background()
	s := openSQLite(t, filepath.Join(t.TempDir(), "history.db"))
	at := time.Date(2026, 9, 17, 12, 0, 0, 500, time.UTC)
	all := []*exchange.Exchange{
		{ID: "lock", Method: "LOCK", Path: "/a/%C3%A9/b", Start: at},
		{ID: "zero", Method: "GET", Path: "/zero"},
		{ID: "before", Method: "get", Path: "/before", Start: at.Add(-time.Nanosecond)},
		{ID: "after", Method: "Get", Path: "/after", Start: at.Add(time.Nanosecond)},
		{ID: "distant", Method: "GET", Path: "/distant", Start: time.Date(2400, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, e := range all {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	filters := map[string]exchange.Filter{
		"kelvin folds to k":    {Method: "locK"},
		"method is case-blind": {Method: "gEt"},
		"path by substring":    {Path: "%C3%A9/"},
		"since the instant":    {Since: at},
		"until the instant":    {Until: at},
		"one-instant window":   {Since: at, Until: at.Add(time.Nanosecond)},
		"since before zero":    {Since: time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC)},
		"until after 2262":     {Until: time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	// The reference order is chronological by start, newest first, not the
	// order in which the exchanges were recorded.
	chrono := slices.Clone(all)
	slices.SortStableFunc(chrono, func(a, b *exchange.Exchange) int { return b.Start.Compare(a.Start) })
	for name, f := range filters {
		var want []string
		for _, e := range chrono {
			if f.Match(e) {
				want = append(want, e.ID)
			}
		}
		res, err := s.List(ctx, f, store.Page{Limit: store.MaxLimit})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var got []string
		for _, e := range res.Items {
			got = append(got, e.ID)
		}
		if len(want) == 0 || !equalStrings(got, want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSQLiteBadCursor(t *testing.T) {
	s := openSQLite(t, filepath.Join(t.TempDir(), "history.db"))
	if _, err := s.List(context.Background(), exchange.Filter{}, store.Page{Cursor: "x"}); err != store.ErrBadCursor {
		t.Fatalf("want ErrBadCursor, got %v", err)
	}
}

// Requirement: Pluggable storage - an unavailable backend keeps the process
// from starting. A database that already exists but cannot be written opens
// read-only in SQLite, without an error; opening has to reject it instead of
// leaving the failure for the first exchange.
func TestSQLiteRefusesReadOnlyExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := store.OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	recordIDs(t, s, "a")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o444); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			os.Chmod(p, 0o644)
		}
	})
	if f, err := os.OpenFile(path, os.O_WRONLY, 0); err == nil {
		f.Close()
		t.Skip("this user can write to read-only files (root?); the scenario does not reproduce")
	}
	s, err = store.OpenSQLite(path)
	if err == nil {
		s.Close()
		t.Fatalf("an existing database without write permission should be rejected on open")
	}
}

// A database created before the seq column is migrated on open, with the
// sequence taken from each exchange's JSON, and from then on follows arrival
// order.
func TestSQLiteMigratesDatabaseWithoutSeq(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE exchanges (
		ord        INTEGER PRIMARY KEY AUTOINCREMENT,
		id         TEXT    NOT NULL UNIQUE,
		route      TEXT    NOT NULL,
		upstream   TEXT    NOT NULL,
		override   TEXT    NOT NULL,
		method     TEXT    NOT NULL,
		path       BLOB    NOT NULL,
		status     INTEGER NOT NULL,
		intervened INTEGER NOT NULL,
		start_sec  INTEGER NOT NULL,
		start_nsec INTEGER NOT NULL,
		data       BLOB    NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	// Same instant; the one with the higher sequence was written first.
	for _, e := range []exchange.Exchange{{ID: "b", Seq: 2, Start: at}, {ID: "a", Seq: 1, Start: at}} {
		data, _ := json.Marshal(e)
		if _, err := db.Exec(`INSERT INTO exchanges (id, route, upstream, override, method, path, status, intervened, start_sec, start_nsec, data)
			VALUES (?, '', '', '', '', ?, 0, 0, ?, ?, ?)`, e.ID, []byte{}, at.Unix(), at.Nanosecond(), data); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s := openSQLite(t, path)
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range res.Items {
		got = append(got, e.ID)
	}
	if !equalStrings(got, []string{"b", "a"}) {
		t.Fatalf("after the migration, the order should follow the sequence: %v", got)
	}
	if err := s.Record(ctx, &exchange.Exchange{ID: "c", Seq: 3, Start: at}); err != nil {
		t.Fatalf("Record after the migration: %v", err)
	}
	if e, err := s.Neighbor(ctx, "b", store.Newer, exchange.Filter{}); err != nil || e.ID != "c" {
		t.Fatalf("the one after b should be c: %q %v", e.ID, err)
	}
}
