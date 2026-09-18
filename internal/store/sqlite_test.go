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

	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
	"github.com/gamerjp64/gateway/internal/store/storetest"
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

// Os filtros em SQL precisam casar exatamente como exchange.Filter.Match,
// inclusive nos casos em que o SQL puro divergiria: dobra de caixa fora do
// ASCII, instante zero e bordas da janela de tempo.
func TestSQLiteFilterMatchesReferenceSemantics(t *testing.T) {
	ctx := context.Background()
	s := openSQLite(t, filepath.Join(t.TempDir(), "history.db"))
	at := time.Date(2026, 9, 17, 12, 0, 0, 500, time.UTC)
	all := []*exchange.Exchange{
		{ID: "lock", Method: "LOCK", Path: "/a/%C3%A9/b", Start: at},
		{ID: "zero", Method: "GET", Path: "/zero"},
		{ID: "antes", Method: "get", Path: "/antes", Start: at.Add(-time.Nanosecond)},
		{ID: "depois", Method: "Get", Path: "/depois", Start: at.Add(time.Nanosecond)},
		{ID: "distante", Method: "GET", Path: "/distante", Start: time.Date(2400, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, e := range all {
		if err := s.Record(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	filters := map[string]exchange.Filter{
		"kelvin dobra para k":   {Method: "locK"},
		"método sem caixa":      {Method: "gEt"},
		"path por substring":    {Path: "%C3%A9/"},
		"desde o instante":      {Since: at},
		"até o instante":        {Until: at},
		"janela de um instante": {Since: at, Until: at.Add(time.Nanosecond)},
		"desde antes do zero":   {Since: time.Date(1, 1, 1, 0, 0, 0, 1, time.UTC)},
		"até depois de 2262":    {Until: time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	// A ordem de referência é a cronológica pelo início, da mais nova para a
	// mais antiga, e não a de registro.
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
			t.Errorf("%s: recebido %v, esperado %v", name, got, want)
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
		t.Fatalf("esperado ErrBadCursor, recebido %v", err)
	}
}

// Requirement: Armazenamento plugável — Backend indisponível impede a
// inicialização. Um banco que já existe mas não pode ser escrito abre só
// para leitura no SQLite, sem erro; a abertura precisa recusá-lo, e não
// deixar a falha para a primeira troca.
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
		t.Skip("este usuário escreve em arquivos somente leitura (root?); o cenário não se reproduz")
	}
	s, err = store.OpenSQLite(path)
	if err == nil {
		s.Close()
		t.Fatalf("um banco existente sem permissão de escrita deveria ser recusado na abertura")
	}
}

// Um banco criado antes da coluna seq é migrado na abertura, com a sequência
// tirada do JSON de cada troca, e passa a seguir a ordem de chegada.
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
	// Mesmo instante; a de sequência maior foi gravada primeiro.
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
		t.Fatalf("após a migração, a ordem deveria seguir a sequência: %v", got)
	}
	if err := s.Record(ctx, &exchange.Exchange{ID: "c", Seq: 3, Start: at}); err != nil {
		t.Fatalf("Record após a migração: %v", err)
	}
	if e, err := s.Neighbor(ctx, "b", store.Newer, exchange.Filter{}); err != nil || e.ID != "c" {
		t.Fatalf("seguinte de b deveria ser c: %q %v", e.ID, err)
	}
}
