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

// singleError exige exatamente um erro localizado.
func singleError(t *testing.T, err error) *Error {
	t.Helper()
	var es Errors
	if !errors.As(err, &es) || len(es) != 1 {
		t.Fatalf("esperado exatamente um erro localizado, recebido: %v", err)
	}
	return es[0]
}

// Requirement: Um documento por rota

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
		t.Fatalf("esperadas 3 rotas, recebidas %d", len(routes))
	}
}

func TestRoutesDirEmptyOrMissing(t *testing.T) {
	for name, dir := range map[string]string{
		"vazio":   t.TempDir(),
		"ausente": filepath.Join(t.TempDir(), "nao-existe"),
	} {
		t.Run(name, func(t *testing.T) {
			routes, warnings, err := loadDir(t, dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(routes) != 0 {
				t.Fatalf("esperado nenhuma rota, recebidas %d", len(routes))
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], dir) {
				t.Fatalf("esperado um aviso nomeando %s, recebido %v", dir, warnings)
			}
		})
	}
}

func TestInvalidDocumentNamesFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"ok.yaml":  routeYAML("ok", "/ok/*"),
		"bad.yaml": "schemaVersion: 1\nname: bad\nupstream: http://a\nmatch:\n  path: /bad/*\nstripPrefix: talvez\n",
	})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	if filepath.Base(e.File) != "bad.yaml" || e.Field != "stripPrefix" || e.Line != 6 || e.Column == 0 {
		t.Fatalf("erro deveria nomear bad.yaml, stripPrefix e a linha 6: %+v", e)
	}
}

func TestUnrecognizedExtensionIgnored(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"a.yaml":       routeYAML("a", "/a/*"),
		"README.md":    "# não é rota: {{{",
		"notas.txt":    "texto qualquer",
		"sub/x.yaml":   "inválido: [",
		"a.yaml.bkp":   "lixo",
		"sem-extensao": "lixo",
	})
	routes, _, err := loadDir(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("esperada 1 rota, recebidas %d", len(routes))
	}
}

// Requirement: Detecção de colisão entre documentos

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
			t.Fatalf("mensagem deveria conter %s: %s", want, msg)
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
			t.Fatalf("mensagem deveria conter %s: %s", want, msg)
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
		t.Fatalf("o curinga mais longo deveria vir primeiro: %v, %v", routes[0].Name(), routes[1].Name())
	}
}

// Requirement: Validação da configuração

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
		t.Fatalf("erro deveria apontar p.yaml, overrides[0].probability, linha 12 coluna 5: %+v", e)
	}
	if !strings.Contains(e.Msg, "1.5") {
		t.Fatalf("mensagem deveria citar o valor: %s", e.Msg)
	}
}

func TestInvalidUpstream(t *testing.T) {
	for _, up := range []string{"localhost:9000", "ftp://x", "http://", "::nada"} {
		t.Run(up, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, map[string]string{"u.yaml": "schemaVersion: 1\nname: u\nupstream: \"" + up + "\"\nmatch:\n  path: /u/*\n"})
			_, _, err := loadDir(t, dir)
			e := singleError(t, err)
			if filepath.Base(e.File) != "u.yaml" || e.Field != "upstream" || !strings.Contains(e.Msg, up) {
				t.Fatalf("erro deveria nomear u.yaml e o valor %q: %+v", up, e)
			}
		})
	}
}

func TestSchemaVersionAboveSupported(t *testing.T) {
	dir := t.TempDir()
	// Um documento futuro pode ter campos desconhecidos; o erro é o de versão.
	writeFiles(t, dir, map[string]string{"f.yaml": "schemaVersion: 7\nname: f\nmatch:\n  path: /f/*\nnovidade: sim\n"})
	_, _, err := loadDir(t, dir)
	e := singleError(t, err)
	if e.Field != "schemaVersion" || !strings.Contains(e.Msg, "7") || !strings.Contains(e.Msg, "1") {
		t.Fatalf("erro deveria nomear as duas versões: %+v", e)
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
		t.Fatalf("erro deveria apontar latency.min na linha 10: %+v", e)
	}
}
