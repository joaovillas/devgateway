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

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
)

// settingsFor monta a configuração efetiva como o processo a monta, a partir
// de um gateway.json (vazio: arquivo ausente) e do ambiente dado.
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

// Requirement: Armazenamento plugável do histórico — Memória é o padrão
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
		t.Fatalf("sem seleção, o backend deveria ser memória, recebido %T", st)
	}
	if store.BackendName(s) != config.BackendMemory {
		t.Fatalf("nome do backend padrão: %q", store.BackendName(s))
	}
	// Capacidade limitada, com descarte das mais antigas ao atingi-la.
	for i := range s.HistoryCapacity + 1 {
		m.Record(ctx, &exchange.Exchange{ID: fmt.Sprint(i)})
	}
	if _, err := m.Get(ctx, "0"); err != store.ErrNotFound {
		t.Fatalf("a troca mais antiga deveria ter sido descartada: %v", err)
	}
	if _, err := m.Get(ctx, fmt.Sprint(s.HistoryCapacity)); err != nil {
		t.Fatalf("a troca nova deveria estar registrada: %v", err)
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
			t.Errorf("%s: backend aberto %s, esperado %s", c.backend, got, c.want)
		}
		st.Close()
	}
}

// fileAsDir devolve um caminho cujo diretório é um arquivo comum, onde
// nenhum backend consegue criar o seu.
func fileAsDir(t *testing.T, name string) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "arquivo-comum")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(blocker, name)
}

// Requirement: Armazenamento plugável do histórico — Backend indisponível
// impede a inicialização (a recusa de iniciar do processo está em app).
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
			t.Fatalf("%s: deveria falhar num caminho dentro de um arquivo", backend)
		}
		msg := err.Error()
		if !strings.Contains(msg, backend) || !strings.Contains(msg, path) {
			t.Errorf("%s: erro deveria nomear o backend e o caminho: %s", backend, msg)
		}
		var cause *fs.PathError
		if !errors.As(err, &cause) {
			t.Errorf("%s: erro deveria encadear a causa do sistema de arquivos: %s", backend, msg)
		}
	}
}

func TestOpenUnknownBackend(t *testing.T) {
	_, err := store.Open(config.Settings{HistoryBackend: "redis"})
	if err == nil || !strings.Contains(err.Error(), "redis") {
		t.Fatalf("backend desconhecido deveria ser recusado nomeando-o: %v", err)
	}
}
