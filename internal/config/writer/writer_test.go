package writer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gamerjp64/devgateway/internal/config"
)

const (
	paymentsDoc = "# comment that a rewrite would lose\nschemaVersion: 1\nname: payments\nupstream: http://localhost:9001\nmatch:\n  path: /api/payments/*\n"
	ordersDoc   = "# orders\nschemaVersion: 1\nname: orders\nupstream: http://localhost:9002\nmatch:\n  path: /api/orders/*\n"
)

// setup writes the documents into a routes directory and loads the configuration.
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
	ok, err := w.UpdateRoute(RouteUpdate{Route: "payments", Cause: CauseLearning, Apply: addOverride("fresh")})
	if err != nil || !ok {
		t.Fatalf("the write should have happened: %v %v", ok, err)
	}
	doc, err := config.ParseRoute("payments.yaml", []byte(read(t, filepath.Join(dir, "payments.yaml"))))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Overrides) != 1 || doc.Overrides[0].Name != "fresh" {
		t.Fatalf("the written document should hold the override: %+v", doc.Overrides)
	}
	snap := live.Load()
	if snap == before || snap.Route("payments").Override("fresh") == nil {
		t.Fatal("the snapshot should have been swapped with the new override")
	}
	if snap.Route("orders") == nil {
		t.Fatal("the other routes should still be in the snapshot")
	}
	if len(changes) != 1 || changes[0].Cause != CauseLearning || changes[0].Routes[0] != "payments" {
		t.Fatalf("the change should have been reported: %+v", changes)
	}
	// The untouched document stays byte for byte the same, and no temporary file is left over.
	if got := read(t, filepath.Join(dir, "orders.yaml")); got != ordersDoc {
		t.Fatalf("the untouched document changed:\n%s", got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Fatalf("the directory should hold only the two documents: %v", entries)
	}
}

func TestInvalidUpdateWritesNothing(t *testing.T) {
	dir, live := setup(t)
	before := live.Load()
	_, err := New(live).UpdateRoute(RouteUpdate{Route: "payments", Apply: func(r *config.Route) (bool, error) {
		p := 1.5
		r.Overrides = append(r.Overrides, config.Override{
			Name: "bad", Match: config.OverrideMatch{Path: "/x"}, Respond: &config.Respond{}, Probability: &p,
		})
		return true, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "probability") {
		t.Fatalf("the invalid write should have been rejected naming the field: %v", err)
	}
	if got := read(t, filepath.Join(dir, "payments.yaml")); got != paymentsDoc {
		t.Fatalf("nothing should have been written:\n%s", got)
	}
	if live.Load() != before {
		t.Fatal("the configuration in force should have stayed put")
	}
}

func TestUpdateWithoutChangeWritesNothing(t *testing.T) {
	dir, live := setup(t)
	before := live.Load()
	ok, err := New(live).UpdateRoute(RouteUpdate{Route: "payments", Apply: func(*config.Route) (bool, error) { return false, nil }})
	if ok || err != nil {
		t.Fatalf("with no change nothing should have been written: %v %v", ok, err)
	}
	if read(t, filepath.Join(dir, "payments.yaml")) != paymentsDoc || live.Load() != before {
		t.Fatal("with no change the document and the snapshot should have stayed put")
	}
}

func TestUpdateMissingRoute(t *testing.T) {
	_, live := setup(t)
	if _, err := New(live).UpdateRoute(RouteUpdate{Route: "nothing", Apply: addOverride("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestIfMatchRefusesStaleVersion(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	if _, err := w.UpdateRoute(RouteUpdate{Route: "payments", IfMatch: `"other"`, Apply: addOverride("x")}); !errors.Is(err, ErrStale) {
		t.Fatalf("expected ErrStale, got %v", err)
	}
	v := Version([]byte(read(t, filepath.Join(dir, "payments.yaml"))))
	if ok, err := w.UpdateRoute(RouteUpdate{Route: "payments", IfMatch: v, Apply: addOverride("x")}); !ok || err != nil {
		t.Fatalf("with the current version the write should have happened: %v %v", ok, err)
	}
}

// The change starts from the document on disk, under the mutex: concurrent
// writes do not lose one another.
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
		t.Fatalf("all ten writes should be in the document and in the snapshot: %d", len(doc.Overrides))
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
		t.Fatal("the write should have waited for the exclusive operation")
	default:
	}
	close(release)
	<-done
}

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	path := filepath.Join(tempDir(t), "a.yaml")
	if err := WriteFileAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if got := read(t, path); got != "two" {
		t.Fatalf("unexpected content: %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("no temporary file should have been left over: %v", entries)
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
		t.Fatalf("creation: %+v %v", res, err)
	}
	if res.Version != Version([]byte(read(t, res.File))) || live.Load().Route("shipping") == nil {
		t.Fatal("the created document should be on disk and in the snapshot")
	}
	if read(t, filepath.Join(dir, "payments.yaml")) != paymentsDoc || read(t, filepath.Join(dir, "orders.yaml")) != ordersDoc {
		t.Fatal("the other documents should not have been touched")
	}
	if len(changes) != 1 || changes[0].Routes[0] != "shipping" {
		t.Fatalf("the creation should have been reported: %+v", changes)
	}
}

func TestCreateRouteConflicts(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	var es config.Errors
	if _, err := w.CreateRoute(newRoute("payments", "/other/*"), CauseAPI); !errors.As(err, &es) || !es.HasConflict() {
		t.Fatalf("a duplicate name should be a conflict: %v", err)
	}
	if _, err := w.CreateRoute(newRoute("clone", "/api/orders/*"), CauseAPI); !errors.As(err, &es) || !es.HasConflict() ||
		!strings.Contains(err.Error(), "orders.yaml") {
		t.Fatalf("an identical match should be a conflict naming the file: %v", err)
	}
	// An orphan file in the way is not overwritten.
	orphan := filepath.Join(dir, "orphan.yaml")
	os.WriteFile(orphan, []byte("# by hand\n"), 0o644)
	if _, err := w.CreateRoute(newRoute("orphan", "/orphan/*"), CauseAPI); !errors.As(err, &es) || !es.HasConflict() {
		t.Fatalf("an existing file should be a conflict: %v", err)
	}
	if read(t, orphan) != "# by hand\n" {
		t.Fatal("the existing file should not have been overwritten")
	}
	// An invalid name is rejected before it becomes a file path.
	if _, err := w.CreateRoute(newRoute("../outside", "/outside/*"), CauseAPI); err == nil || (errors.As(err, &es) && es.HasConflict()) {
		t.Fatalf("an invalid name should be rejected as invalid, not as a conflict: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "outside.yaml")); err == nil {
		t.Fatal("nothing should have been written outside the routes directory")
	}
}

func TestCreateRouteMakesMissingDirectory(t *testing.T) {
	dir := filepath.Join(tempDir(t), "routes")
	live := config.NewLive(config.NewSnapshot(config.Settings{RoutesDir: dir}, nil, nil))
	if _, err := New(live).CreateRoute(newRoute("a", "/a/*"), CauseAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.yaml")); err != nil {
		t.Fatalf("the routes directory should have been created: %v", err)
	}
}

func TestWriteDocumentKeepsTextAsSent(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	text := strings.Replace(paymentsDoc, "9001", "9005", 1) + "# trailing comment\n"
	res, err := w.WriteDocument(DocumentWrite{Route: "payments", Data: []byte(text), Cause: CauseAPI})
	if err != nil || !res.Changed || res.Created {
		t.Fatalf("write: %+v %v", res, err)
	}
	if read(t, filepath.Join(dir, "payments.yaml")) != text || live.Load().Route("payments").Doc.Upstream != "http://localhost:9005" {
		t.Fatal("the text should be written as sent and take effect in the snapshot")
	}
	// Error located in the text that was sent.
	_, err = w.WriteDocument(DocumentWrite{Route: "payments", Data: []byte(text + "timeout: -1s\n")})
	var es config.Errors
	if !errors.As(err, &es) || es[0].Field != "timeout" || es[0].Line != 8 {
		t.Fatalf("the error should point at the field and the line: %v", err)
	}
	// Mismatched name.
	if _, err := w.WriteDocument(DocumentWrite{Route: "payments", Data: []byte(ordersDoc)}); !errors.As(err, &es) || es[0].Field != "name" {
		t.Fatalf("a mismatched name should be rejected: %v", err)
	}
	// New route.
	res, err = w.WriteDocument(DocumentWrite{Route: "fresh", Data: []byte("schemaVersion: 1\nname: fresh\nmatch:\n  path: /fresh/*\noverrides:\n  - name: ok\n    match:\n      path: /fresh/x\n    respond:\n      status: 204\n")})
	if err != nil || !res.Created || live.Load().Route("fresh") == nil {
		t.Fatalf("creation through the document: %+v %v", res, err)
	}
	if _, err := w.WriteDocument(DocumentWrite{Route: "other", IfMatch: `"x"`, Data: []byte("schemaVersion: 1\nname: other\nmatch:\n  path: /o\n")}); !errors.Is(err, ErrStale) {
		t.Fatalf("If-Match on a route that does not exist should be ErrStale: %v", err)
	}
}

func TestDeleteRouteRemovesDocument(t *testing.T) {
	dir, live := setup(t)
	w := New(live)
	if _, err := w.DeleteRoute("payments", `"old"`, CauseAPI); !errors.Is(err, ErrStale) {
		t.Fatalf("stale If-Match: %v", err)
	}
	if _, err := w.DeleteRoute("payments", "", CauseAPI); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "payments.yaml")); err == nil || live.Load().Route("payments") != nil {
		t.Fatal("the route and the document should be gone")
	}
	if read(t, filepath.Join(dir, "orders.yaml")) != ordersDoc {
		t.Fatal("the other documents should not have been touched")
	}
	if _, err := w.DeleteRoute("payments", "", CauseAPI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeated removal: %v", err)
	}
}

func TestUpdateRenamesRouteInSameFile(t *testing.T) {
	dir, live := setup(t)
	res, err := New(live).Update(RouteUpdate{Route: "orders", Apply: func(r *config.Route) (bool, error) {
		r.Name = "purchases"
		return true, nil
	}})
	if err != nil || res.Route != "purchases" || res.File != filepath.Join(dir, "orders.yaml") {
		t.Fatalf("rename: %+v %v", res, err)
	}
	if live.Load().Route("orders") != nil || live.Load().Route("purchases") == nil {
		t.Fatal("the snapshot should carry the route under its new name")
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
	os.WriteFile(filepath.Join(dir, "fresh.yaml"), []byte("schemaVersion: 1\nname: fresh\nmatch:\n  path: /fresh\n"), 0o644)

	// A failure to apply preserves the configuration in force.
	before := live.Load()
	if _, _, err := w.Reload(load, func(_, _ *config.Snapshot) error { return errors.New("port already in use") }); err == nil || live.Load() != before {
		t.Fatalf("a failure to apply should preserve the snapshot: %v", err)
	}
	c, snap, err := w.Reload(load, nil)
	if err != nil || live.Load() != snap || snap.Route("fresh") == nil {
		t.Fatalf("reload: %v", err)
	}
	if c.Cause != CauseReload || len(c.Routes) != 1 || c.Routes[0] != "fresh" {
		t.Fatalf("the change should name the new route: %+v", c)
	}
	// Invalid document: rejected, and the configuration is preserved.
	os.WriteFile(filepath.Join(dir, "fresh.yaml"), []byte("schemaVersion: 1\nname: fresh\nmatch:\n  path: fresh\n"), 0o644)
	if _, _, err := w.Reload(load, nil); err == nil || live.Load() != snap {
		t.Fatalf("an invalid reload should preserve the snapshot: %v", err)
	}
}
