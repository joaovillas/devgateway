package store_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	for name, f := range filters {
		var want []string
		for i := len(all) - 1; i >= 0; i-- {
			if f.Match(all[i]) {
				want = append(want, all[i].ID)
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
