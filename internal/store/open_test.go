package store_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
)

// settingsFor builds the effective configuration the same way the process
// does, from a gateway.json (empty: no file) and the given environment.
func settingsFor(t *testing.T, gatewayJSON string, env map[string]string) config.Settings {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.json")
	if gatewayJSON != "" {
		if err := os.WriteFile(path, []byte(gatewayJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, _, err := config.LoadSettings(path, func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	})
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	return s
}

// Requirement: Pluggable history storage - memory is the default
func TestOpenMemoryIsDefault(t *testing.T) {
	ctx := context.Background()
	s := settingsFor(t, "", nil)
	st, err := store.Open(s)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m, ok := st.(*store.Memory)
	if !ok {
		t.Fatalf("with nothing selected, the backend should be memory, got %T", st)
	}
	if store.BackendName(s) != config.BackendMemory {
		t.Fatalf("default backend name: %q", store.BackendName(s))
	}
	// Bounded capacity, dropping the oldest ones once it is reached.
	for i := range s.HistoryCapacity + 1 {
		m.Record(ctx, &exchange.Exchange{ID: fmt.Sprint(i)})
	}
	if _, err := m.Get(ctx, "0"); err != store.ErrNotFound {
		t.Fatalf("the oldest exchange should have been dropped: %v", err)
	}
	if _, err := m.Get(ctx, fmt.Sprint(s.HistoryCapacity)); err != nil {
		t.Fatalf("the new exchange should have been recorded: %v", err)
	}
}

func TestOpenSelectsBackendByEnvironment(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		backend string
		want    string
	}{
		{config.BackendNDJSON, "*store.NDJSON"},
		{config.BackendSQLite, "*store.SQLite"},
		{config.BackendMemory, "*store.Memory"},
	}
	for _, c := range cases {
		s := settingsFor(t, "", map[string]string{
			"GATEWAY_HISTORY_BACKEND": c.backend,
			"GATEWAY_HISTORY_PATH":    filepath.Join(dir, "history-"+c.backend),
		})
		st, err := store.Open(s)
		if err != nil {
			t.Fatalf("%s: %v", c.backend, err)
		}
		if got := fmt.Sprintf("%T", st); got != c.want {
			t.Errorf("%s: opened backend %s, want %s", c.backend, got, c.want)
		}
		st.Close()
	}
}

// fileAsDir returns a path whose parent directory is a plain file, where no
// backend can create its own.
func fileAsDir(t *testing.T, name string) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "plain-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, name)
}

// Requirement: Pluggable history storage - an unavailable backend keeps the
// process from starting (the process refusing to start lives in app).
func TestOpenUnavailableBackendNamesBackendAndCause(t *testing.T) {
	for _, backend := range []string{config.BackendSQLite, config.BackendNDJSON} {
		path := fileAsDir(t, "history")
		s := settingsFor(t, "", map[string]string{
			"GATEWAY_HISTORY_BACKEND": backend,
			"GATEWAY_HISTORY_PATH":    path,
		})
		st, err := store.Open(s)
		if err == nil {
			st.Close()
			t.Fatalf("%s: should fail on a path inside a file", backend)
		}
		msg := err.Error()
		if !strings.Contains(msg, backend) || !strings.Contains(msg, path) {
			t.Errorf("%s: the error should name the backend and the path: %s", backend, msg)
		}
		var cause *fs.PathError
		if !errors.As(err, &cause) {
			t.Errorf("%s: the error should wrap the filesystem cause: %s", backend, msg)
		}
	}
}

func TestOpenUnknownBackend(t *testing.T) {
	_, err := store.Open(config.Settings{HistoryBackend: "redis"})
	if err == nil || !strings.Contains(err.Error(), "redis") {
		t.Fatalf("an unknown backend should be rejected by name: %v", err)
	}
}
