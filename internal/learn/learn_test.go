package learn

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync/atomic"
	"testing"

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/config/writer"
	"github.com/gamerjp64/devgateway/internal/exchange"
)

func setup(t *testing.T) (string, *config.Live, *Learner) {
	t.Helper()
	dir := tempDir(t)
	doc := filepath.Join(dir, "api.yaml")
	if err := os.WriteFile(doc, []byte("schemaVersion: 1\nname: api\nupstream: http://localhost:9\nmatch:\n  path: /api/*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	docs, _, err := config.ReadRoutesDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := config.BuildRoutes(docs)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(config.NewSnapshot(config.Settings{}, routes, nil))
	l := New(writer.New(live), slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(l.Close)
	return doc, live, l
}

func upstreamExchange(id, method, path string) exchange.Exchange {
	return exchange.Exchange{
		ID: id, Method: method, Path: path, Route: "api", Outcome: exchange.OutcomeUpstream, Status: http.StatusOK,
		Response: exchange.Message{Headers: http.Header{"Content-Type": {"text/plain"}}, Body: []byte("ok"), Size: 2},
	}
}

func overridesOf(t *testing.T, doc string) []config.Override {
	t.Helper()
	b, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}
	r, err := config.ParseRoute(doc, b)
	if err != nil {
		t.Fatal(err)
	}
	return r.Overrides
}

// A request still running with the snapshot from before the learning does not
// write the same endpoint again: the final check is done under the write
// mutex, against the document on disk.
func TestStaleSnapshotDoesNotDuplicate(t *testing.T) {
	doc, live, l := setup(t)
	stale := live.Load().Route("api")
	l.Observe(stale, upstreamExchange("A", http.MethodGet, "/api/test"))
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	l.Observe(stale, upstreamExchange("B", http.MethodGet, "/api/test"))
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	ovs := overridesOf(t, doc)
	if len(ovs) != 1 || ovs[0].Source.Exchange != "A" {
		t.Fatalf("the endpoint should be written exactly once, by the first exchange: %+v", ovs)
	}
	if len(live.Load().Route("api").Overrides) != 1 {
		t.Fatal("the snapshot should have the learned override")
	}
}

func TestIneligibleExchangesAreIgnored(t *testing.T) {
	doc, live, l := setup(t)
	route := live.Load().Route("api")
	base := upstreamExchange("A", http.MethodGet, "/api/x")
	for name, mod := range map[string]func(*exchange.Exchange){
		"synthesized": func(e *exchange.Exchange) { e.Outcome = exchange.OutcomeSynthesized },
		"dropped":     func(e *exchange.Exchange) { e.Outcome, e.Status = exchange.OutcomeDropped, 0 },
		"error":       func(e *exchange.Exchange) { e.Outcome, e.Status = exchange.OutcomeGateway, http.StatusBadGateway },
		"interrupted": func(e *exchange.Exchange) { e.Error = "the response transfer was interrupted" },
		"upgrade":     func(e *exchange.Exchange) { e.Status = http.StatusSwitchingProtocols },
		"no route":    func(e *exchange.Exchange) { e.Route = "" },
	} {
		e := base
		mod(&e)
		if Eligible(e) {
			t.Errorf("the %s exchange should not be eligible", name)
		}
		l.Observe(route, e)
	}
	l.Observe(nil, base)
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ovs := overridesOf(t, doc); len(ovs) != 0 {
		t.Fatalf("no override should be learned: %+v", ovs)
	}
	if !Eligible(base) {
		t.Fatal("the exchange answered by the upstream should be eligible")
	}
}

// A literal * in the path is learned as an exact regular expression, without
// becoming a wildcard, and the endpoint becomes known.
func TestLiteralStarPathLearnedExactly(t *testing.T) {
	doc, live, l := setup(t)
	for _, p := range []string{"/api/a*b", "/api/files/*"} {
		l.Observe(live.Load().Route("api"), upstreamExchange("A", http.MethodGet, p))
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	ovs := overridesOf(t, doc)
	if len(ovs) != 2 {
		t.Fatalf("both endpoints should be learned: %+v", ovs)
	}
	for i, p := range []string{"/api/a*b", "/api/files/*"} {
		if m := ovs[i].Match; m.Path != "" || m.PathRegex != "^"+regexp.QuoteMeta(p)+"$" {
			t.Errorf("%s: the criterion should be the exact regular expression: %+v", p, m)
		}
	}
	for _, o := range live.Load().Route("api").Overrides {
		if o.Path != nil && o.Path.Wildcard {
			t.Fatalf("no learned override should be a wildcard: %+v", o.Doc.Match)
		}
	}
	// Again, with the new snapshot: they are known by now.
	for _, p := range []string{"/api/a*b", "/api/files/*"} {
		l.Observe(live.Load().Route("api"), upstreamExchange("B", http.MethodGet, p))
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(overridesOf(t, doc)); n != 2 {
		t.Fatalf("the endpoints should not be written again: %d overrides", n)
	}
}

// countWarnings counts the warnings written to the log.
type countWarnings struct {
	slog.Handler
	n *atomic.Int64
}

func (c countWarnings) Handle(ctx context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		c.n.Add(1)
	}
	return nil
}

// An override that is built but would not be valid is not written, and the
// endpoint is not tried again on every request.
func TestInvalidLearnedOverrideNotRetried(t *testing.T) {
	doc, live, _ := setup(t)
	var warns atomic.Int64
	l := New(writer.New(live), slog.New(countWarnings{slog.NewTextHandler(io.Discard, nil), &warns}))
	t.Cleanup(l.Close)
	e := upstreamExchange("A", http.MethodGet, "/api/bad")
	// A header name that the route document does not accept.
	e.Response.Headers = http.Header{"X Bad": {"v"}}
	for range 3 {
		l.Observe(live.Load().Route("api"), e)
		if err := l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if ovs := overridesOf(t, doc); len(ovs) != 0 {
		t.Fatalf("nothing should be written: %+v", ovs)
	}
	if n := warns.Load(); n != 1 {
		t.Fatalf("the invalid endpoint should be warned about exactly once, got %d", n)
	}
	// Other endpoints keep being learned.
	l.Observe(live.Load().Route("api"), upstreamExchange("B", http.MethodGet, "/api/good"))
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ovs := overridesOf(t, doc); len(ovs) != 1 || ovs[0].Match.Path != "/api/good" {
		t.Fatalf("the valid endpoint should be learned: %+v", ovs)
	}
}
