package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func envMap(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func writeGateway(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"gateway.json": content})
	return filepath.Join(dir, "gateway.json")
}

// Requirement: Configuração do processo em gateway.json

func TestGatewayFileLoaded(t *testing.T) {
	path := writeGateway(t, `{"schemaVersion":1,"ports":{"traffic":9000,"admin":9001},"seed":7}`)
	s, warnings, err := LoadSettings(path, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.TrafficPort != 9000 || s.AdminPort != 9001 || s.Seed == nil || *s.Seed != 7 {
		t.Fatalf("valores do arquivo não aplicados: %+v", s)
	}
	if len(warnings) != 0 {
		t.Fatalf("avisos inesperados: %v", warnings)
	}
}

func TestGatewayFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.json")
	s, warnings, err := LoadSettings(path, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.TrafficPort != 8080 || s.AdminPort != 8081 || s.HistoryBackend != BackendMemory {
		t.Fatalf("padrões não aplicados: %+v", s)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], path) {
		t.Fatalf("esperado aviso nomeando %s, recebido %v", path, warnings)
	}
}

// Requirement: Variáveis de ambiente sobrepõem os arquivos

func TestEnvOverridesFile(t *testing.T) {
	path := writeGateway(t, `{"history":{"backend":"memory"}}`)
	s, _, err := LoadSettings(path, envMap(map[string]string{"GATEWAY_HISTORY_BACKEND": "sqlite"}))
	if err != nil {
		t.Fatal(err)
	}
	if s.HistoryBackend != BackendSQLite {
		t.Fatalf("ambiente deveria vencer o arquivo: %q", s.HistoryBackend)
	}
}

func TestFileOverridesDefault(t *testing.T) {
	path := writeGateway(t, `{"ports":{"traffic":9999}}`)
	s, _, err := LoadSettings(path, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.TrafficPort != 9999 {
		t.Fatalf("arquivo deveria vencer o padrão: %d", s.TrafficPort)
	}
}

func TestEffectiveOrigin(t *testing.T) {
	path := writeGateway(t, `{"seed":42}`)
	s, _, err := LoadSettings(path, envMap(map[string]string{"GATEWAY_TRAFFIC_PORT": "7000"}))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]EffectiveValue{}
	for _, v := range s.Effective() {
		got[v.Key] = v
	}
	want := map[string]Source{
		"ports.traffic": {Origin: OriginEnv, Name: "GATEWAY_TRAFFIC_PORT"},
		"seed":          {Origin: OriginFile, Name: path},
		"ports.admin":   {Origin: OriginDefault},
	}
	for key, src := range want {
		if got[key].Source != src {
			t.Errorf("%s: origem %+v, esperada %+v", key, got[key].Source, src)
		}
	}
	if got["ports.traffic"].Value != 7000 || got["seed"].Value != uint64(42) {
		t.Errorf("valores efetivos errados: %v, %v", got["ports.traffic"].Value, got["seed"].Value)
	}
	if len(got) != len(settingFields) {
		t.Errorf("a consulta deveria listar todos os %d valores, listou %d", len(settingFields), len(got))
	}
}

func TestInvalidEnvNamesVariable(t *testing.T) {
	_, _, err := LoadSettings(filepath.Join(t.TempDir(), "x.json"), envMap(map[string]string{"GATEWAY_ADMIN_PORT": "abc"}))
	e := singleError(t, err)
	if !strings.Contains(e.Error(), "GATEWAY_ADMIN_PORT") {
		t.Fatalf("erro deveria nomear a variável: %v", e)
	}
}

// Requirement: Validação da configuração — Portas iguais

func TestEqualPortsRefused(t *testing.T) {
	path := writeGateway(t, "{\n  \"ports\": {\n    \"traffic\": 9000,\n    \"admin\": 9000\n  }\n}\n")
	_, _, err := LoadSettings(path, envMap(nil))
	e := singleError(t, err)
	if e.File != path || e.Field != "ports.admin" || e.Line != 4 || !strings.Contains(e.Msg, "9000") {
		t.Fatalf("erro deveria apontar ports.admin na linha 4 de %s: %+v", path, e)
	}
}

func TestEqualPortsFromEnvNamesVariable(t *testing.T) {
	path := writeGateway(t, `{"ports":{"traffic":9000}}`)
	_, _, err := LoadSettings(path, envMap(map[string]string{"GATEWAY_ADMIN_PORT": "9000"}))
	e := singleError(t, err)
	if !strings.Contains(e.Error(), "GATEWAY_ADMIN_PORT") {
		t.Fatalf("erro deveria nomear a variável de ambiente: %v", e)
	}
}

func TestGatewayFileSchemaAboveSupported(t *testing.T) {
	path := writeGateway(t, `{"schemaVersion": 3}`)
	_, _, err := LoadSettings(path, envMap(nil))
	e := singleError(t, err)
	if e.Field != "schemaVersion" || !strings.Contains(e.Msg, "3") {
		t.Fatalf("erro de versão esperado: %+v", e)
	}
}

func TestRelativePathsResolveFromConfigDir(t *testing.T) {
	path := writeGateway(t, `{"routesDir":"rotas","history":{"backend":"ndjson","path":"h.ndjson"}}`)
	s, _, err := LoadSettings(path, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if s.RoutesDir != filepath.Join(dir, "rotas") || s.HistoryPath != filepath.Join(dir, "h.ndjson") {
		t.Fatalf("caminhos deveriam partir de %s: %q, %q", dir, s.RoutesDir, s.HistoryPath)
	}
}

func TestDefaultPathsSitNextToGatewayFile(t *testing.T) {
	path := writeGateway(t, `{"history":{"backend":"sqlite"}}`)
	s, _, err := LoadSettings(path, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if s.RoutesDir != filepath.Join(dir, "routes") || s.HistoryPath != filepath.Join(dir, "gateway-history.db") {
		t.Fatalf("padrões deveriam ficar ao lado de gateway.json: %q, %q", s.RoutesDir, s.HistoryPath)
	}
	s, _, err = LoadSettings(path, envMap(map[string]string{"GATEWAY_ROUTES_DIR": "rel"}))
	if err != nil {
		t.Fatal(err)
	}
	if s.RoutesDir != "rel" {
		t.Fatalf("caminho do ambiente parte do diretório de trabalho: %q", s.RoutesDir)
	}
}

// Requirement: Configuração do processo em gateway.json — modo aprendizado

func TestLearningSetting(t *testing.T) {
	s, _, err := LoadSettings(filepath.Join(t.TempDir(), "gateway.json"), envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.LearningEnabled || s.Sources["learning.enabled"].Origin != OriginDefault {
		t.Fatalf("aprendizado deveria vir desligado do padrão: %v %+v", s.LearningEnabled, s.Sources["learning.enabled"])
	}

	path := writeGateway(t, `{"learning":{"enabled":true}}`)
	s, _, err = LoadSettings(path, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !s.LearningEnabled || s.Sources["learning.enabled"] != (Source{Origin: OriginFile, Name: path}) {
		t.Fatalf("aprendizado deveria vir ligado do arquivo: %v %+v", s.LearningEnabled, s.Sources["learning.enabled"])
	}

	s, _, err = LoadSettings(path, envMap(map[string]string{"GATEWAY_LEARNING": "false"}))
	if err != nil {
		t.Fatal(err)
	}
	if s.LearningEnabled || s.Sources["learning.enabled"] != (Source{Origin: OriginEnv, Name: "GATEWAY_LEARNING"}) {
		t.Fatalf("GATEWAY_LEARNING deveria vencer o arquivo: %v %+v", s.LearningEnabled, s.Sources["learning.enabled"])
	}
	var found bool
	for _, v := range s.Effective() {
		if v.Key == "learning.enabled" {
			found = v.Env == "GATEWAY_LEARNING" && v.Value == false && v.Source.Origin == OriginEnv
		}
	}
	if !found {
		t.Fatal("learning.enabled deveria constar da configuração efetiva com sua origem")
	}

	_, _, err = LoadSettings(path, envMap(map[string]string{"GATEWAY_LEARNING": "talvez"}))
	if e := singleError(t, err); !strings.Contains(e.Error(), "GATEWAY_LEARNING") || e.Field != "learning.enabled" {
		t.Fatalf("erro deveria nomear a variável e a chave: %+v", e)
	}
}
