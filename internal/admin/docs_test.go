package admin

import (
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/gamerjp64/devgateway/internal/capture"
	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/config/writer"
	"github.com/gamerjp64/devgateway/internal/override"
	"github.com/gamerjp64/devgateway/internal/store"
)

// The API reference (docs/api.md) is checked against the code: every
// operation in the summary has a runnable curl example and is registered
// on the handler with that method and path.

var (
	summaryOp = regexp.MustCompile("^\\|[^|]*\\| `(GET|POST|PUT|PATCH|DELETE) (/api/[^`]+)` \\|(.*)\\|$")
	pathParam = regexp.MustCompile(`\{[a-z]+\}`)
	curlMeth  = regexp.MustCompile(`-X (GET|POST|PUT|PATCH|DELETE)\b`)
	curlURL   = regexp.MustCompile(`\$A(/api/[^\s"'?]*)`)
)

type docOp struct {
	method, path string
	pending      bool // marked in the summary as not implemented yet
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
			ops = append(ops, docOp{m[1], m[2], strings.Contains(m[3], "not implemented yet")})
		}
	}
	if len(ops) < 30 {
		t.Fatalf("the summary should list every operation; found %d", len(ops))
	}
	return ops
}

// curlCalls extracts the method and path of every curl in the sh blocks,
// joining the lines continued with a backslash.
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
		// Every path parameter matches any one segment.
		re := regexp.MustCompile("^" + pathParam.ReplaceAllString(op.path, `[^/]+`) + "$")
		found := false
		for _, c := range calls {
			if c[0] == op.method && re.MatchString(c[1]) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s %s has no curl example in docs/api.md", op.method, op.path)
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
			t.Errorf("%s %s is implemented, but the summary marks it as not implemented yet", op.method, op.path)
		case !op.pending && !registered:
			t.Errorf("%s %s is in the summary, but the handler does not serve it (pattern %q)", op.method, op.path, pattern)
		}
	}
}
