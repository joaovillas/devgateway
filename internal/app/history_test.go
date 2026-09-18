package app

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gamerjp64/gateway/internal/config"
)

// Requirement: Armazenamento plugável do histórico

func TestHistoryMemoryIsDefault(t *testing.T) {
	a := startWith(t, freePorts, nil)
	if got := a.History.Backend(); got != config.BackendMemory {
		t.Fatalf("sem seleção, o histórico deveria ficar em memória, está em %q", got)
	}
}

func TestHistoryBackendSelectedByEnvironment(t *testing.T) {
	t.Setenv("GATEWAY_HISTORY_BACKEND", config.BackendSQLite)
	t.Setenv("GATEWAY_HISTORY_PATH", filepath.Join(t.TempDir(), "history.db"))
	a := startWith(t, freePorts, nil)
	if got := a.History.Backend(); got != config.BackendSQLite {
		t.Fatalf("o backend da variável de ambiente deveria valer, em uso %q", got)
	}
}

// freePort reserva e libera uma porta, para verificar depois que ela não
// ficou ocupada.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestHistoryUnavailableBackendRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "arquivo-comum")
	writeFile(t, blocker, "x")
	dbPath := filepath.Join(blocker, "history.db")
	traffic, admin := freePort(t), freePort(t)
	for admin == traffic {
		admin = freePort(t)
	}
	writeFile(t, filepath.Join(dir, "gateway.json"), `{"ports":{"traffic":`+strconv.Itoa(traffic)+
		`,"admin":`+strconv.Itoa(admin)+`},"history":{"backend":"sqlite","path":"arquivo-comum/history.db"}}`)

	a, err := Start(Options{Loader: loaderFor(filepath.Join(dir, "gateway.json")), Web: testWeb, Log: quietLog()})
	if err == nil {
		a.Shutdown(t.Context())
		t.Fatalf("deveria recusar iniciar com o backend indisponível")
	}
	msg := err.Error()
	if !strings.Contains(msg, "sqlite") || !strings.Contains(msg, dbPath) {
		t.Fatalf("a recusa deveria informar o backend e a causa: %s", msg)
	}
	if cause := new(*fs.PathError); !errors.As(err, cause) {
		t.Fatalf("a recusa deveria encadear a causa do sistema de arquivos: %s", msg)
	}
	if _, statErr := os.Stat(blocker); statErr != nil {
		t.Fatalf("o arquivo no caminho não deveria ser alterado: %v", statErr)
	}
	// Nenhuma porta fica aberta.
	for _, p := range []int{traffic, admin} {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err != nil {
			t.Fatalf("a porta %d ficou ocupada após a recusa: %v", p, err)
		}
		ln.Close()
	}
}
