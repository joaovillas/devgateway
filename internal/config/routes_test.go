package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func routeYAML(name, path string) string {
	return "schemaVersion: 1\nname: " + name + "\nupstream: http://localhost:9000\nmatch:\n  path: " + path + "\n"
}

func loadDir(t *testing.T, dir string) ([]*CompiledRoute, []string, error) {
	t.Helper()
	docs, warnings, err := ReadRoutesDir(dir)
	if err != nil {
		return nil, warnings, err
	}
	routes, err := BuildRoutes(docs)
	return routes, warnings, err
}

// singleError requires exactly one located error.
func singleError(t *testing.T, err error) *Error {
	t.Helper()
	var es Errors
	if !errors.As(err, &es) || len(es) != 1 {
		t.Fatalf("expected exactly one located error, got: %v", err)
	}
	return es[0]
}

// Requirement: One document per route

func TestRoutesMergedIntoSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.yaml": routeYAML("a", "/a/*"),
		"b.yml":  routeYAML("b", "/b/*"),
		"c.yaml": routeYAML("c", "/c"),
	})
	routes, _, err := loadDir(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}
}

func TestRoutesDirEmptyOrMissing(t *testing.T) {
	for name, dir := range map[string]string{
		"empty":   t.TempDir(),
		"missing": filepath.Join(t.TempDir(), "does-not-exist"),
	} {
		t.Run(name, func(t *testing.T) {
			routes, warnings, err := loadDir(t, dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(routes) != 0 {
				t.Fatalf("expected no routes, got %d", len(routes))
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], dir) {
				t.Fatalf("expected a single warning naming %s, got %v", dir, warnings)
			}
		})
	}
}

func TestInvalidDocumentNamesFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"ok.yaml":  routeYAML("ok", "/ok/*"),
		"bad.yaml": "schemaVersion: 1\nname: bad\nupstream: http://a\nmatch:\n  path: /bad/*\nstripPrefix: maybe\n",
	})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	if filepath.Base(e.File) != "bad.yaml" || e.Field != "stripPrefix" || e.Line != 6 || e.Column == 0 {
		t.Fatalf("the error should name bad.yaml, stripPrefix and line 6: %+v", e)
	}
}

func TestUnrecognizedExtensionIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.yaml":       routeYAML("a", "/a/*"),
		"README.md":    "# not a route: {{{",
		"notes.txt":    "just some text",
		"sub/x.yaml":   "invalid: [",
		"a.yaml.bkp":   "junk",
		"no-extension": "junk",
	})
	routes, _, err := loadDir(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
}

// Requirement: Collision detection between documents

func TestDuplicateRouteNames(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"one.yaml": routeYAML("payments", "/a/*"),
		"two.yaml": routeYAML("payments", "/b/*"),
	})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	msg := e.Error()
	for _, want := range []string{"one.yaml", "two.yaml", `"payments"`} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the message should contain %s: %s", want, msg)
		}
	}
}

func TestIdenticalMatchPatterns(t *testing.T) {
	dir := t.TempDir()
	body := func(name string) string {
		return "schemaVersion: 1\nname: " + name + "\nupstream: http://a\nmatch:\n  host: Payments.local\n  path: /v1/*\n"
	}
	writeFiles(t, dir, map[string]string{
		"one.yaml": body("one"),
		"two.yaml": strings.Replace(body("two"), "Payments", "payments", 1),
	})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	msg := e.Error()
	for _, want := range []string{"one.yaml", "two.yaml", "/v1/*"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the message should contain %s: %s", want, msg)
		}
	}
}

func TestOverlappingPatternsAllowed(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"api.yaml":      routeYAML("api", "/api/*"),
		"payments.yaml": routeYAML("payments", "/api/payments/*"),
	})
	routes, _, err := loadDir(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[0].Name() != "payments" {
		t.Fatalf("the longest wildcard should come first: %v, %v", routes[0].Name(), routes[1].Name())
	}
}

// Requirement: Configuration validation

func TestProbabilityOutOfRange(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"p.yaml": `schemaVersion: 1
name: p
upstream: http://localhost:9000
match:
  path: /p/*
overrides:
  - name: flaky
    match:
      path: /p/x
    respond:
      status: 503
    probability: 1.5
`})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	if filepath.Base(e.File) != "p.yaml" || e.Field != "overrides[0].probability" || e.Line != 12 || e.Column != 5 {
		t.Fatalf("the error should point at p.yaml, overrides[0].probability, line 12 column 5: %+v", e)
	}
	if !strings.Contains(e.Msg, "1.5") {
		t.Fatalf("the message should quote the value: %s", e.Msg)
	}
}

func TestInvalidUpstream(t *testing.T) {
	for _, up := range []string{"localhost:9000", "ftp://x", "http://", "::nothing"} {
		t.Run(up, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"u.yaml": "schemaVersion: 1\nname: u\nupstream: \"" + up + "\"\nmatch:\n  path: /u/*\n"})
			_, _, err := loadDir(t, dir)
			e := singleError(t, err)
			if filepath.Base(e.File) != "u.yaml" || e.Field != "upstream" || !strings.Contains(e.Msg, up) {
				t.Fatalf("the error should name u.yaml and the value %q: %+v", up, e)
			}
		})
	}
}

func TestSchemaVersionAboveSupported(t *testing.T) {
	dir := t.TempDir()
	// A future document may carry unknown fields; the error is the one about the version.
	writeFiles(t, dir, map[string]string{"f.yaml": "schemaVersion: 7\nname: f\nmatch:\n  path: /f/*\nsomethingNew: yes\n"})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	if e.Field != "schemaVersion" || !strings.Contains(e.Msg, "7") || !strings.Contains(e.Msg, "1") {
		t.Fatalf("the error should name both versions: %+v", e)
	}
}

func TestLatencyRangeInverted(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"l.yaml": `schemaVersion: 1
name: l
match:
  path: /l/*
overrides:
  - name: slow
    match:
      path: /l/*
    latency:
      min: 500ms
      max: 100ms
`})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	if e.Field != "overrides[0].latency.min" || e.Line != 10 {
		t.Fatalf("the error should point at latency.min on line 10: %+v", e)
	}
}

func TestOverrideSourceKindValidated(t *testing.T) {
	doc := func(source string) string {
		return `schemaVersion: 1
name: s
upstream: http://localhost:9000
match:
  path: /s/*
overrides:
  - name: learned
    enabled: false
    match:
      path: /s/x
    respond:
      status: 200
    source:
` + source
	}
	for name, c := range map[string]struct {
		source, field, msg string
		line               int
	}{
		"unknown kind":     {"      kind: guessed\n      exchange: 01J8ZK3Q4N6V7W8X9Y0Z1A2B3C\n", "overrides[0].source.kind", "guessed", 14},
		"missing kind":     {"      exchange: 01J8ZK3Q4N6V7W8X9Y0Z1A2B3C\n", "overrides[0].source.kind", "required", 13},
		"missing exchange": {"      kind: derived\n", "overrides[0].source.exchange", "exchange", 13},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"s.yaml": doc(c.source)})
			_, _, err := loadDir(t, dir)
			e := singleError(t, err)
			if e.Field != c.field || e.Line != c.line || !strings.Contains(e.Msg, c.msg) {
				t.Fatalf("the error should point at %s on line %d quoting %q: %+v", c.field, c.line, c.msg, e)
			}
		})
	}
	for _, kind := range []string{SourceLearned, SourceDerived} {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"s.yaml": doc("      kind: " + kind + "\n      exchange: 01J8ZK3Q4N6V7W8X9Y0Z1A2B3C\n")})
		if _, _, err := loadDir(t, dir); err != nil {
			t.Fatalf("source %s should be accepted: %v", kind, err)
		}
	}
}

func TestDisabledOverrideStillValidated(t *testing.T) {
	// Being off is no excuse: the override has to hold up when it is turned back on.
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"d.yaml": `schemaVersion: 1
name: d
upstream: http://localhost:9000
match:
  path: /d/*
overrides:
  - name: off
    enabled: false
    match:
      path: /d/x
    respond:
      status: 503
    probability: 1.5
  - name: empty
    enabled: false
    match:
      path: /d/y
`})
	_, _, err := loadDir(t, dir)
	var es Errors
	if !errors.As(err, &es) || len(es) != 2 {
		t.Fatalf("expected two located errors, got: %v", err)
	}
	if es[0].Field != "overrides[0].probability" || es[0].Line != 13 {
		t.Errorf("a disabled override should still have its probability validated: %+v", es[0])
	}
	if es[1].Field != "overrides[1]" || !strings.Contains(es[1].Msg, "respond") {
		t.Errorf("a disabled override with no effect should be rejected: %+v", es[1])
	}
}

// Requirement: Override selection criteria — segment parameters

func TestSegmentParamPathValidated(t *testing.T) {
	doc := func(path string) string {
		return "schemaVersion: 1\nname: v\nmatch:\n  path: /zip/*\noverrides:\n  - name: o\n    match:\n      path: " + path + "\n    respond:\n      status: 200\n"
	}
	for _, ok := range []string{"/zip/:id/json", "/zip/:cep_1/:_x", "/zip/:id/*", "/v1/projects:batch"} {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"v.yaml": doc(ok)})
		if _, _, err := loadDir(t, dir); err != nil {
			t.Errorf("%s should be accepted: %v", ok, err)
		}
	}
	for path, want := range map[string]string{
		"/zip/:/json":       "invalid",
		"/zip/:1d/json":     "invalid",
		"/zip/:i-d/json":    "invalid",
		"/zip/:id/:id/json": "duplicate",
		"/zip/:id*":         "parameter and a wildcard",
	} {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"v.yaml": doc(path)})
		_, _, err := loadDir(t, dir)
		e := singleError(t, err)
		if e.Field != "overrides[0].match.path" || !strings.Contains(e.Msg, want) {
			t.Errorf("%s: unexpected error %+v", path, e)
		}
	}
}

// A route path still takes no parameters: only an override path accepts them.
func TestRoutePathRejectsSegmentParams(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"v.yaml": routeYAML("v", "/zip/:id/*")})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	if e.Field != "match.path" || !strings.Contains(e.Msg, "only accepted in an override path") {
		t.Fatalf("unexpected error: %+v", e)
	}
}
