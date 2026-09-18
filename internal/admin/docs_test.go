package admin

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gamerjp64/gateway/internal/capture"
	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/config/writer"
	"github.com/gamerjp64/gateway/internal/override"
	"github.com/gamerjp64/gateway/internal/store"
)

// A referência da API (docs/api.md) é conferida contra o código: cada
// operação do sumário tem um exemplo executável por curl e está registrada
// no handler com esse método e path.

var (
	summaryOp = regexp.MustCompile("^\\|[^|]*\\| `(GET|POST|PUT|PATCH|DELETE) (/api/[^`]+)` \\|(.*)\\|$")
	pathParam = regexp.MustCompile(`\{[a-z]+\}`)
	curlMeth  = regexp.MustCompile(`-X (GET|POST|PUT|PATCH|DELETE)\b`)
	curlURL   = regexp.MustCompile(`\$A(/api/[^\s"'?]*)`)
)

type docOp struct {
	method, path string
	pending      bool // marcada no sumário como ainda não implementada
}

func readAPIDoc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/api.md")
	if err != nil {
		t.Fatalf("docs/api.md: %v", err)
	}
	return string(b)
}

func summaryOps(t *testing.T, doc string) []docOp {
	t.Helper()
	var ops []docOp
	for line := range strings.SplitSeq(doc, "\n") {
		if m := summaryOp.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			ops = append(ops, docOp{m[1], m[2], strings.Contains(m[3], "ainda não implementado")})
		}
	}
	if len(ops) < 30 {
		t.Fatalf("o sumário deveria listar todas as operações; achei %d", len(ops))
	}
	return ops
}

// curlCalls extrai método e path de cada curl dos blocos sh, juntando as
// linhas continuadas com barra invertida.
func curlCalls(doc string) [][2]string {
	var out [][2]string
	inSh := false
	var cmd strings.Builder
	for line := range strings.SplitSeq(doc, "\n") {
		switch {
		case strings.HasPrefix(line, "```sh"):
			inSh = true
			continue
		case strings.HasPrefix(line, "```"):
			inSh = false
			continue
		}
		if !inSh {
			continue
		}
		cmd.WriteString(strings.TrimSuffix(line, "\\") + " ")
		if strings.HasSuffix(line, "\\") {
			continue
		}
		c := cmd.String()
		cmd.Reset()
		if !strings.Contains(c, "curl") {
			continue
		}
		method := "GET"
		if m := curlMeth.FindStringSubmatch(c); m != nil {
			method = m[1]
		}
		if u := curlURL.FindStringSubmatch(c); u != nil {
			out = append(out, [2]string{method, u[1]})
		}
	}
	return out
}

func TestAPIDocHasCurlForEveryOperation(t *testing.T) {
	doc := readAPIDoc(t)
	calls := curlCalls(doc)
	for _, op := range summaryOps(t, doc) {
		// Cada parâmetro do path casa com um segmento qualquer.
		re := regexp.MustCompile("^" + pathParam.ReplaceAllString(op.path, `[^/]+`) + "$")
		found := false
		for _, c := range calls {
			if c[0] == op.method && re.MatchString(c[1]) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s %s não tem exemplo curl em docs/api.md", op.method, op.path)
		}
	}
}

func TestAPIDocMatchesHandler(t *testing.T) {
	s, _, err := config.SettingsFrom("gateway.json", []byte(`{}`), func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(config.NewSnapshot(s, nil, nil))
	hist := store.NewSwitchable(config.BackendMemory, store.NewMemory(10))
	rec := capture.NewRecorder(hist, nil, nil)
	defer rec.Close()
	h := New(Deps{
		Live:      live,
		History:   hist,
		Recorder:  rec,
		Writer:    writer.New(live),
		Overrides: override.NewTracker(live),
		Loader:    config.Loader{ConfigPath: "gateway.json"},
	})
	for _, op := range summaryOps(t, readAPIDoc(t)) {
		path := pathParam.ReplaceAllStringFunc(op.path, func(p string) string {
			return map[string]string{"{route}": "payments", "{override}": "flaky", "{id}": "01K5E3V3C8Q2M4Z8N6P0R2T4W6"}[p]
		})
		_, pattern := h.mux.Handler(httptest.NewRequest(op.method, path, nil))
		registered := strings.HasPrefix(pattern, op.method+" ")
		switch {
		case op.pending && registered:
			t.Errorf("%s %s está implementado, mas o sumário o marca como ainda não implementado", op.method, op.path)
		case !op.pending && !registered:
			t.Errorf("%s %s está no sumário, mas o handler não o atende (padrão %q)", op.method, op.path, pattern)
		}
	}
}
