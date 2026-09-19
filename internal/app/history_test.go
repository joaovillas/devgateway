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

	"github.com/joaovillas/devgateway/internal/config"
)

// Requirement: Pluggable history storage

func TestHistoryMemoryIsDefault(t *testing.T) {
	a := startWith(t, freePorts, nil)
	if got := a.History.Backend(); got != config.BackendMemory {
		t.Fatalf("with nothing selected the history should live in memory, it is in %q", got)
	}
}

func TestHistoryBackendSelectedByEnvironment(t *testing.T) {
	t.Setenv("GATEWAY_HISTORY_BACKEND", config.BackendSQLite)
	t.Setenv("GATEWAY_HISTORY_PATH", filepath.Join(t.TempDir(), "history.db"))
	a := startWith(t, freePorts, nil)
	if got := a.History.Backend(); got != config.BackendSQLite {
		t.Fatalf("the backend from the environment variable should win, in use %q", got)
	}
}

// freePort reserves and releases a port, so that we can check afterwards that
// it was not left taken.
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
	blocker := filepath.Join(dir, "plain-file")
	writeFile(t, blocker, "x")
	dbPath := filepath.Join(blocker, "history.db")
	traffic, admin := freePort(t), freePort(t)
	for admin == traffic {
		admin = freePort(t)
	}
	writeFile(t, filepath.Join(dir, "gateway.json"), `{"ports":{"traffic":`+strconv.Itoa(traffic)+
		`,"admin":`+strconv.Itoa(admin)+`},"history":{"backend":"sqlite","path":"plain-file/history.db"}}`)

	a, err := Start(Options{Loader: loaderFor(filepath.Join(dir, "gateway.json")), Web: testWeb, Log: quietLog()})
	if err == nil {
		a.Shutdown(t.Context())
		t.Fatalf("it should refuse to start with an unavailable backend")
	}
	msg := err.Error()
	if !strings.Contains(msg, "sqlite") || !strings.Contains(msg, dbPath) {
		t.Fatalf("the refusal should report the backend and the cause: %s", msg)
	}
	if cause := new(*fs.PathError); !errors.As(err, cause) {
		t.Fatalf("the refusal should chain the filesystem cause: %s", msg)
	}
	if _, statErr := os.Stat(blocker); statErr != nil {
		t.Fatalf("the file sitting at that path should not be changed: %v", statErr)
	}
	// No port is left open.
	for _, p := range []int{traffic, admin} {
		ln, err := net.Listen("tcp", ":"+strconv.Itoa(p))
		if err != nil {
			t.Fatalf("port %d was left taken after the refusal: %v", p, err)
		}
		ln.Close()
	}
}
