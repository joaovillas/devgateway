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
	dir = tempDir(t)
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
	path := filepath.Join(tempDir(t), "a.yaml")
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

func newRoute(name, path string) config.Route {
	return config.Route{SchemaVersion: 1, Name: name, Upstream: "http://localhost:9003", Match: config.RouteMatch{Path: path}}
}

func TestCreateRouteWritesOwnDocument(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	var changes []Change
	w.OnChange(func(c Change) { changes = append(changes, c) })
	res, err := w.CreateRoute(newRoute("shipping", "/shipping/*"), CauseAPI)
	if err != nil || !res.Created || res.File != filepath.Join(dir, "shipping.yaml") {
		t.Fatalf("criação: %+v %v", res, err)
	}
	if res.Version != Version([]byte(read(t, res.File))) || live.Load().Route("shipping") == nil {
		t.Fatal("o documento criado deveria estar em disco e no snapshot")
	}
	if read(t, filepath.Join(dir, "payments.yaml")) != paymentsDoc || read(t, filepath.Join(dir, "orders.yaml")) != ordersDoc {
		t.Fatal("os demais documentos não deveriam ser tocados")
	}
	if len(changes) != 1 || changes[0].Routes[0] != "shipping" {
		t.Fatalf("a criação deveria ser avisada: %+v", changes)
	}
}

func TestCreateRouteConflicts(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	var es config.Errors
	if _, err := w.CreateRoute(newRoute("payments", "/outro/*"), CauseAPI); !errors.As(err, &es) || !es.HasConflict() {
		t.Fatalf("nome repetido deveria ser conflito: %v", err)
	}
	if _, err := w.CreateRoute(newRoute("clone", "/api/orders/*"), CauseAPI); !errors.As(err, &es) || !es.HasConflict() ||
		!strings.Contains(err.Error(), "orders.yaml") {
		t.Fatalf("casamento idêntico deveria ser conflito nomeando o arquivo: %v", err)
	}
	// Um arquivo órfão no caminho não é sobrescrito.
	orphan := filepath.Join(dir, "orfa.yaml")
	os.WriteFile(orphan, []byte("# à mão\n"), 0o644)
	if _, err := w.CreateRoute(newRoute("orfa", "/orfa/*"), CauseAPI); !errors.As(err, &es) || !es.HasConflict() {
		t.Fatalf("arquivo existente deveria ser conflito: %v", err)
	}
	if read(t, orphan) != "# à mão\n" {
		t.Fatal("o arquivo existente não deveria ser sobrescrito")
	}
	// Um nome inválido é recusado antes de virar caminho de arquivo.
	if _, err := w.CreateRoute(newRoute("../fora", "/fora/*"), CauseAPI); err == nil || (errors.As(err, &es) && es.HasConflict()) {
		t.Fatalf("nome inválido deveria ser recusado como inválido: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "fora.yaml")); err == nil {
		t.Fatal("nada deveria ser gravado fora do diretório de rotas")
	}
}

func TestCreateRouteMakesMissingDirectory(t *testing.T) {
	dir := filepath.Join(tempDir(t), "routes")
	live := config.NewLive(config.NewSnapshot(config.Settings{RoutesDir: dir}, nil, nil))
	if _, err := New(live).CreateRoute(newRoute("a", "/a/*"), CauseAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.yaml")); err != nil {
		t.Fatalf("o diretório de rotas deveria ser criado: %v", err)
	}
}

func TestWriteDocumentKeepsTextAsSent(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	text := strings.Replace(paymentsDoc, "9001", "9005", 1) + "# comentário no fim\n"
	res, err := w.WriteDocument(DocumentWrite{Route: "payments", Data: []byte(text), Cause: CauseAPI})
	if err != nil || !res.Changed || res.Created {
		t.Fatalf("gravação: %+v %v", res, err)
	}
	if read(t, filepath.Join(dir, "payments.yaml")) != text || live.Load().Route("payments").Doc.Upstream != "http://localhost:9005" {
		t.Fatal("o texto deveria ser gravado como enviado e valer no snapshot")
	}
	// Erro localizado no texto enviado.
	_, err = w.WriteDocument(DocumentWrite{Route: "payments", Data: []byte(text + "timeout: -1s\n")})
	var es config.Errors
	if !errors.As(err, &es) || es[0].Field != "timeout" || es[0].Line != 8 {
		t.Fatalf("o erro deveria apontar o campo e a linha: %v", err)
	}
	// Nome divergente.
	if _, err := w.WriteDocument(DocumentWrite{Route: "payments", Data: []byte(ordersDoc)}); !errors.As(err, &es) || es[0].Field != "name" {
		t.Fatalf("nome divergente deveria ser recusado: %v", err)
	}
	// Rota nova.
	res, err = w.WriteDocument(DocumentWrite{Route: "novo", Data: []byte("schemaVersion: 1\nname: novo\nmatch:\n  path: /novo/*\noverrides:\n  - name: ok\n    match:\n      path: /novo/x\n    respond:\n      status: 204\n")})
	if err != nil || !res.Created || live.Load().Route("novo") == nil {
		t.Fatalf("criação pelo documento: %+v %v", res, err)
	}
	if _, err := w.WriteDocument(DocumentWrite{Route: "outro", IfMatch: `"x"`, Data: []byte("schemaVersion: 1\nname: outro\nmatch:\n  path: /o\n")}); !errors.Is(err, ErrStale) {
		t.Fatalf("If-Match para rota inexistente deveria ser ErrStale: %v", err)
	}
}

func TestDeleteRouteRemovesDocument(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	if _, err := w.DeleteRoute("payments", `"velha"`, CauseAPI); !errors.Is(err, ErrStale) {
		t.Fatalf("If-Match desatualizado: %v", err)
	}
	if _, err := w.DeleteRoute("payments", "", CauseAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "payments.yaml")); err == nil || live.Load().Route("payments") != nil {
		t.Fatal("a rota e o documento deveriam sumir")
	}
	if read(t, filepath.Join(dir, "orders.yaml")) != ordersDoc {
		t.Fatal("os demais documentos não deveriam ser tocados")
	}
	if _, err := w.DeleteRoute("payments", "", CauseAPI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remoção repetida: %v", err)
	}
}

func TestUpdateRenamesRouteInSameFile(t *testing.T) {
	dir, live := setup(t)
	res, err := New(live).Update(RouteUpdate{Route: "orders", Apply: func(r *config.Route) (bool, error) {
		r.Name = "pedidos"
		return true, nil
	}})
	if err != nil || res.Route != "pedidos" || res.File != filepath.Join(dir, "orders.yaml") {
		t.Fatalf("renomeação: %+v %v", res, err)
	}
	if live.Load().Route("orders") != nil || live.Load().Route("pedidos") == nil {
		t.Fatal("o snapshot deveria trazer a rota pelo nome novo")
	}
}

func TestReload(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	load := func() (*config.Snapshot, error) {
		docs, warnings, err := config.ReadRoutesDir(dir)
		if err != nil {
			return nil, err
		}
		routes, err := config.BuildRoutes(docs)
		if err != nil {
			return nil, err
		}
		return config.NewSnapshot(config.Settings{RoutesDir: dir}, routes, warnings), nil
	}
	os.WriteFile(filepath.Join(dir, "novo.yaml"), []byte("schemaVersion: 1\nname: novo\nmatch:\n  path: /novo\n"), 0o644)

	// Uma falha ao aplicar preserva a configuração em vigor.
	before := live.Load()
	if _, _, err := w.Reload(load, func(_, _ *config.Snapshot) error { return errors.New("porta ocupada") }); err == nil || live.Load() != before {
		t.Fatalf("a falha ao aplicar deveria preservar o snapshot: %v", err)
	}
	c, snap, err := w.Reload(load, nil)
	if err != nil || live.Load() != snap || snap.Route("novo") == nil {
		t.Fatalf("recarga: %v", err)
	}
	if c.Cause != CauseReload || len(c.Routes) != 1 || c.Routes[0] != "novo" {
		t.Fatalf("a mudança deveria nomear a rota nova: %+v", c)
	}
	// Documento inválido: recusa e preserva.
	os.WriteFile(filepath.Join(dir, "novo.yaml"), []byte("schemaVersion: 1\nname: novo\nmatch:\n  path: novo\n"), 0o644)
	if _, _, err := w.Reload(load, nil); err == nil || live.Load() != snap {
		t.Fatalf("a recarga inválida deveria preservar o snapshot: %v", err)
	}
}
