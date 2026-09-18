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

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/config/writer"
	"github.com/gamerjp64/gateway/internal/exchange"
)

func setup(t *testing.T) (string, *config.Live, *Learner) {
	t.Helper()
	dir := t.TempDir()
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

// Uma requisição que ainda segue com o snapshot anterior ao aprendizado não
// grava de novo o mesmo endpoint: a verificação final é feita sob o mutex de
// escrita, contra o documento em disco.
func TestStaleSnapshotDoesNotDuplicate(t *testing.T) {
	doc, live, l := setup(t)
	stale := live.Load().Route("api")
	l.Observe(stale, upstreamExchange("A", http.MethodGet, "/api/teste"))
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	l.Observe(stale, upstreamExchange("B", http.MethodGet, "/api/teste"))
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	ovs := overridesOf(t, doc)
	if len(ovs) != 1 || ovs[0].Source.Exchange != "A" {
		t.Fatalf("o endpoint deveria ser gravado uma única vez, pela primeira troca: %+v", ovs)
	}
	if len(live.Load().Route("api").Overrides) != 1 {
		t.Fatal("o snapshot deveria ter o override aprendido")
	}
}

func TestIneligibleExchangesAreIgnored(t *testing.T) {
	doc, live, l := setup(t)
	route := live.Load().Route("api")
	base := upstreamExchange("A", http.MethodGet, "/api/x")
	for name, mod := range map[string]func(*exchange.Exchange){
		"sintetizada":  func(e *exchange.Exchange) { e.Outcome = exchange.OutcomeSynthesized },
		"derrubada":    func(e *exchange.Exchange) { e.Outcome, e.Status = exchange.OutcomeDropped, 0 },
		"erro":         func(e *exchange.Exchange) { e.Outcome, e.Status = exchange.OutcomeGateway, http.StatusBadGateway },
		"interrompida": func(e *exchange.Exchange) { e.Error = "a transferência da resposta foi interrompida" },
		"upgrade":      func(e *exchange.Exchange) { e.Status = http.StatusSwitchingProtocols },
		"sem rota":     func(e *exchange.Exchange) { e.Route = "" },
	} {
		e := base
		mod(&e)
		if Eligible(e) {
			t.Errorf("troca %s não deveria ser elegível", name)
		}
		l.Observe(route, e)
	}
	l.Observe(nil, base)
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ovs := overridesOf(t, doc); len(ovs) != 0 {
		t.Fatalf("nenhum override deveria ser aprendido: %+v", ovs)
	}
	if !Eligible(base) {
		t.Fatal("a troca respondida pelo upstream deveria ser elegível")
	}
}

// Um * literal no path é aprendido como expressão regular exata, sem virar
// curinga, e o endpoint passa a ser conhecido.
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
		t.Fatalf("os dois endpoints deveriam ser aprendidos: %+v", ovs)
	}
	for i, p := range []string{"/api/a*b", "/api/files/*"} {
		if m := ovs[i].Match; m.Path != "" || m.PathRegex != "^"+regexp.QuoteMeta(p)+"$" {
			t.Errorf("%s: critério deveria ser a expressão regular exata: %+v", p, m)
		}
	}
	for _, o := range live.Load().Route("api").Overrides {
		if o.Path != nil && o.Path.Wildcard {
			t.Fatalf("nenhum override aprendido deveria ser curinga: %+v", o.Doc.Match)
		}
	}
	// De novo, com o snapshot novo: já são conhecidos.
	for _, p := range []string{"/api/a*b", "/api/files/*"} {
		l.Observe(live.Load().Route("api"), upstreamExchange("B", http.MethodGet, p))
	}
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n := len(overridesOf(t, doc)); n != 2 {
		t.Fatalf("os endpoints não deveriam ser gravados de novo: %d overrides", n)
	}
}

// countWarnings conta os avisos registrados no log.
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

// Um override montado que não seria válido não é gravado, e o endpoint não é
// tentado de novo a cada requisição.
func TestInvalidLearnedOverrideNotRetried(t *testing.T) {
	doc, live, _ := setup(t)
	var warns atomic.Int64
	l := New(writer.New(live), slog.New(countWarnings{slog.NewTextHandler(io.Discard, nil), &warns}))
	t.Cleanup(l.Close)
	e := upstreamExchange("A", http.MethodGet, "/api/ruim")
	// Um nome de cabeçalho que o documento de rota não aceita.
	e.Response.Headers = http.Header{"X Ruim": {"v"}}
	for range 3 {
		l.Observe(live.Load().Route("api"), e)
		if err := l.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if ovs := overridesOf(t, doc); len(ovs) != 0 {
		t.Fatalf("nada deveria ser gravado: %+v", ovs)
	}
	if n := warns.Load(); n != 1 {
		t.Fatalf("o endpoint inválido deveria ser avisado uma única vez, foi %d", n)
	}
	// Outros endpoints continuam sendo aprendidos.
	l.Observe(live.Load().Route("api"), upstreamExchange("B", http.MethodGet, "/api/bom"))
	if err := l.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ovs := overridesOf(t, doc); len(ovs) != 1 || ovs[0].Match.Path != "/api/bom" {
		t.Fatalf("o endpoint válido deveria ser aprendido: %+v", ovs)
	}
}
