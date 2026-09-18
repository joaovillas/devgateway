package writer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gamerjp64/gateway/internal/config"
)

const (
	paymentsDoc = "# comentário que a reescrita perde\nschemaVersion: 1\nname: payments\nupstream: http://localhost:9001\nmatch:\n  path: /api/payments/*\n"
	ordersDoc   = "# pedidos\nschemaVersion: 1\nname: orders\nupstream: http://localhost:9002\nmatch:\n  path: /api/orders/*\n"
)

// setup grava os documentos num diretório de rotas e carrega a configuração.
func setup(t *testing.T) (dir string, live *config.Live) {
	t.Helper()
	dir = t.TempDir()
	for name, doc := range map[string]string{"payments.yaml": paymentsDoc, "orders.yaml": ordersDoc} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docs, _, err := config.ReadRoutesDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := config.BuildRoutes(docs)
	if err != nil {
		t.Fatal(err)
	}
	return dir, config.NewLive(config.NewSnapshot(config.Settings{RoutesDir: dir}, routes, nil))
}

func addOverride(name string) func(*config.Route) (bool, error) {
	return func(r *config.Route) (bool, error) {
		r.Overrides = append(r.Overrides, config.Override{
			Name: name, Match: config.OverrideMatch{Path: "/api/payments/x"}, Respond: &config.Respond{Body: "ok"},
		})
		return true, nil
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestUpdateRouteWritesDocumentAndSwapsSnapshot(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	var changes []Change
	w.OnChange(func(c Change) { changes = append(changes, c) })
	before := live.Load()
	ok, err := w.UpdateRoute(RouteUpdate{Route: "payments", Cause: CauseLearning, Apply: addOverride("novo")})
	if err != nil || !ok {
		t.Fatalf("a escrita deveria acontecer: %v %v", ok, err)
	}
	doc, err := config.ParseRoute("payments.yaml", []byte(read(t, filepath.Join(dir, "payments.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Overrides) != 1 || doc.Overrides[0].Name != "novo" {
		t.Fatalf("o documento gravado deveria conter o override: %+v", doc.Overrides)
	}
	snap := live.Load()
	if snap == before || snap.Route("payments").Override("novo") == nil {
		t.Fatal("o snapshot deveria ter sido trocado com o override novo")
	}
	if snap.Route("orders") == nil {
		t.Fatal("as demais rotas deveriam continuar no snapshot")
	}
	if len(changes) != 1 || changes[0].Cause != CauseLearning || changes[0].Routes[0] != "payments" {
		t.Fatalf("a alteração deveria ser avisada: %+v", changes)
	}
	// O documento não tocado fica byte a byte igual, e nenhum temporário sobra.
	if got := read(t, filepath.Join(dir, "orders.yaml")); got != ordersDoc {
		t.Fatalf("o documento não tocado mudou:\n%s", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("o diretório deveria ter só os dois documentos: %v", entries)
	}
}

func TestInvalidUpdateWritesNothing(t *testing.T) {
	dir, live := setup(t)
	before := live.Load()
	_, err := New(live).UpdateRoute(RouteUpdate{Route: "payments", Apply: func(r *config.Route) (bool, error) {
		p := 1.5
		r.Overrides = append(r.Overrides, config.Override{
			Name: "ruim", Match: config.OverrideMatch{Path: "/x"}, Respond: &config.Respond{}, Probability: &p,
		})
		return true, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "probability") {
		t.Fatalf("a escrita inválida deveria ser recusada apontando o campo: %v", err)
	}
	if got := read(t, filepath.Join(dir, "payments.yaml")); got != paymentsDoc {
		t.Fatalf("nada deveria ter sido gravado:\n%s", got)
	}
	if live.Load() != before {
		t.Fatal("a configuração em vigor deveria continuar")
	}
}

func TestUpdateWithoutChangeWritesNothing(t *testing.T) {
	dir, live := setup(t)
	before := live.Load()
	ok, err := New(live).UpdateRoute(RouteUpdate{Route: "payments", Apply: func(*config.Route) (bool, error) { return false, nil }})
	if ok || err != nil {
		t.Fatalf("sem mudança nada deveria ser gravado: %v %v", ok, err)
	}
	if read(t, filepath.Join(dir, "payments.yaml")) != paymentsDoc || live.Load() != before {
		t.Fatal("sem mudança o documento e o snapshot deveriam continuar")
	}
}

func TestUpdateMissingRoute(t *testing.T) {
	_, live := setup(t)
	if _, err := New(live).UpdateRoute(RouteUpdate{Route: "nada", Apply: addOverride("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("esperado ErrNotFound, recebido %v", err)
	}
}

func TestIfMatchRefusesStaleVersion(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	if _, err := w.UpdateRoute(RouteUpdate{Route: "payments", IfMatch: `"outra"`, Apply: addOverride("x")}); !errors.Is(err, ErrStale) {
		t.Fatalf("esperado ErrStale, recebido %v", err)
	}
	v := Version([]byte(read(t, filepath.Join(dir, "payments.yaml"))))
	if ok, err := w.UpdateRoute(RouteUpdate{Route: "payments", IfMatch: v, Apply: addOverride("x")}); !ok || err != nil {
		t.Fatalf("com a versão atual a escrita deveria acontecer: %v %v", ok, err)
	}
}

// A alteração parte do documento em disco, sob o mutex: escritas
// concorrentes não se perdem umas às outras.
func TestConcurrentUpdatesAreSerialized(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			if _, err := w.UpdateRoute(RouteUpdate{Route: "payments", Apply: addOverride("o" + string(rune('a'+i)))}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	doc, err := config.ParseRoute("payments.yaml", []byte(read(t, filepath.Join(dir, "payments.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Overrides) != 10 || len(live.Load().Route("payments").Overrides) != 10 {
		t.Fatalf("as dez escritas deveriam constar do documento e do snapshot: %d", len(doc.Overrides))
	}
}

func TestExclusiveSharesTheWriteMutex(t *testing.T) {
	_, live := setup(t)
	w := New(live)
	inside := make(chan struct{})
	release := make(chan struct{})
	go w.Exclusive(func() error {
		close(inside)
		<-release
		return nil
	})
	<-inside
	done := make(chan struct{})
	go func() {
		w.UpdateRoute(RouteUpdate{Route: "payments", Apply: addOverride("x")})
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("a escrita deveria esperar a operação exclusiva")
	default:
	}
	close(release)
	<-done
}

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.yaml")
	if err := WriteFileAtomic(path, []byte("um")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("dois")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "dois" {
		t.Fatalf("conteúdo inesperado: %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("nenhum temporário deveria sobrar: %v", entries)
	}
}
