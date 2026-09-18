package config

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func readTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// genericYAML decodifica sem tipo, para comparar documentos chave a chave.
func genericYAML(t *testing.T, b []byte) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestGatewayFileRoundTrip(t *testing.T) {
	src := readTestdata(t, "gateway.json")
	g, err := ParseGatewayFile("gateway.json", src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalGatewayFile(g)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := ParseGatewayFile("gateway.json", out)
	if err != nil {
		t.Fatalf("reserialização não recarrega: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(g, g2) {
		t.Fatalf("estrutura mudou na ida e volta:\n%+v\n%+v", g, g2)
	}
	var a, b any
	json.Unmarshal(src, &a)
	json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("campos perdidos na reserialização:\noriginal: %v\nsaída:    %v", a, b)
	}
}

func TestRouteRoundTrip(t *testing.T) {
	src := readTestdata(t, "route-full.yaml")
	r, err := ParseRoute("payments.yaml", src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := MarshalRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ParseRoute("payments.yaml", out)
	if err != nil {
		t.Fatalf("reserialização não recarrega: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(r, r2) {
		t.Fatalf("estrutura mudou na ida e volta:\n%+v\n%+v", r, r2)
	}
	if a, b := genericYAML(t, src), genericYAML(t, out); !reflect.DeepEqual(a, b) {
		t.Fatalf("campos perdidos na reserialização:\noriginal: %v\nsaída:    %v", a, b)
	}
}

func TestRouteRoundTripThroughJSON(t *testing.T) {
	// A API de administração recebe e devolve rotas em JSON.
	r, err := ParseRoute("payments.yaml", readTestdata(t, "route-full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var r2 Route
	if err := json.Unmarshal(b, &r2); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	y1, _ := MarshalRoute(r)
	y2, _ := MarshalRoute(r2)
	if string(y1) != string(y2) {
		t.Fatalf("rota mudou ao passar por JSON:\n%s\n---\n%s", y1, y2)
	}
}

func TestUnknownFieldIsLocated(t *testing.T) {
	src := []byte("schemaVersion: 1\nname: x\nupstream: http://a\nmatch:\n  path: /a/*\n  paht: /b\n")
	_, err := ParseRoute("routes/x.yaml", src)
	es, ok := err.(Errors)
	if !ok || len(es) != 1 {
		t.Fatalf("esperado um erro localizado, recebido %v", err)
	}
	e := es[0]
	if e.File != "routes/x.yaml" || e.Field != "match.paht" || e.Line != 6 {
		t.Fatalf("localização errada: %+v", e)
	}
}

func TestNewRouteFieldsParsed(t *testing.T) {
	r, err := ParseRoute("payments.yaml", readTestdata(t, "route-full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.RewriteHost {
		t.Fatal("rewriteHost deveria ser lido")
	}
	byName := map[string]Override{}
	for _, o := range r.Overrides {
		byName[o.Name] = o
	}
	if o := byName["bilulu"]; !o.Enabled() || o.On != nil || o.Source != nil {
		t.Fatalf("sem enabled o override é ligado e sem origem: %+v", o)
	}
	learned := byName["learned-get"]
	if learned.Enabled() {
		t.Fatal("enabled: false deveria desligar o override")
	}
	want := &OverrideSource{
		Kind:           SourceLearned,
		Exchange:       "01J8ZK3Q4N6V7W8X9Y0Z1A2B3C",
		At:             time.Date(2026, 9, 18, 10, 15, 30, 123456789, time.UTC),
		BodyIncomplete: true,
	}
	if s := learned.Source; s == nil || s.Kind != want.Kind || s.Exchange != want.Exchange ||
		!s.At.Equal(want.At) || s.BodyIncomplete != want.BodyIncomplete {
		t.Fatalf("origem lida errada: %+v, esperada %+v", learned.Source, want)
	}
	derived := byName["derived-charge"]
	if !derived.Enabled() || derived.On == nil || derived.Source == nil || derived.Source.Kind != SourceDerived {
		t.Fatalf("enabled: true explícito e origem derivada deveriam ser lidos: %+v", derived)
	}
	if want := time.Date(2026, 9, 18, 10, 15, 30, 0, time.UTC); !derived.Source.At.Equal(want) {
		t.Fatalf("instante com fuso deveria ser lido: %v", derived.Source.At)
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"payments.yaml": string(readTestdata(t, "route-full.yaml"))})
	if _, _, err := loadDir(t, dir); err != nil {
		t.Fatalf("documento completo deveria ser válido: %v", err)
	}
}

func TestOverrideToggleKeepsFields(t *testing.T) {
	// Desligar e religar um override preserva todos os seus demais campos,
	// inclusive ao passar pelo documento serializado.
	r, err := ParseRoute("payments.yaml", readTestdata(t, "route-full.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	orig := r.Overrides[0]
	off := false
	r.Overrides[0].On = &off
	out, err := MarshalRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := ParseRoute("payments.yaml", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if r2.Overrides[0].Enabled() {
		t.Fatalf("override deveria continuar desligado após reserialização:\n%s", out)
	}
	on := true
	r2.Overrides[0].On = &on
	got := r2.Overrides[0]
	got.On = nil
	if !reflect.DeepEqual(got, orig) || !r2.Overrides[0].Enabled() {
		t.Fatalf("religar deveria restaurar o override:\n%+v\n%+v", got, orig)
	}
}

func TestNewFieldsThroughJSON(t *testing.T) {
	// Os campos novos chegam pela API em JSON com as mesmas chaves do YAML.
	src := []byte(`{"schemaVersion":1,"name":"j","upstream":"http://localhost:9000",
"match":{"path":"/j/*"},"rewriteHost":true,
"overrides":[{"name":"l","enabled":false,"match":{"path":"/j/x","method":"GET"},
"respond":{"status":200},
"source":{"kind":"learned","exchange":"01J8ZK3Q4N6V7W8X9Y0Z1A2B3C","at":"2026-09-18T10:15:30Z","bodyIncomplete":true}}]}`)
	var viaJSON Route
	if err := json.Unmarshal(src, &viaJSON); err != nil {
		t.Fatal(err)
	}
	viaYAML, err := ParseRoute("j.json", src)
	if err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]Route{"json": viaJSON, "documento": viaYAML} {
		o := r.Overrides[0]
		if !r.RewriteHost || o.Enabled() || o.Source == nil || o.Source.Kind != SourceLearned ||
			!o.Source.BodyIncomplete || !o.Source.At.Equal(time.Date(2026, 9, 18, 10, 15, 30, 0, time.UTC)) {
			t.Fatalf("%s: campos novos lidos errado: %+v %+v", name, r, o.Source)
		}
	}
	b, err := json.Marshal(viaJSON)
	if err != nil {
		t.Fatal(err)
	}
	var a, c any
	json.Unmarshal(src, &a)
	json.Unmarshal(b, &c)
	if !reflect.DeepEqual(a, c) {
		t.Fatalf("campos perdidos na ida e volta por JSON:\noriginal: %v\nsaída:    %v", a, c)
	}
}

func TestGatewayFileLearning(t *testing.T) {
	g, err := ParseGatewayFile("gateway.json", readTestdata(t, "gateway.json"))
	if err != nil {
		t.Fatal(err)
	}
	if g.Learning == nil || g.Learning.Enabled == nil || !*g.Learning.Enabled {
		t.Fatalf("learning.enabled deveria ser lido: %+v", g.Learning)
	}
}

// Um cabeçalho da resposta declarada tem um valor (texto) ou vários (lista),
// em YAML e em JSON, e a forma sobrevive à ida e volta.
func TestRespondHeaderValues(t *testing.T) {
	src := []byte(`schemaVersion: 1
name: h
upstream: http://localhost:9000
match:
  path: /h/*
overrides:
  - name: cookies
    match:
      path: /h/x
    respond:
      headers:
        Content-Type: text/plain
        Set-Cookie:
          - a=1; Path=/
          - b=2
`)
	r, err := ParseRoute("h.yaml", src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]HeaderValues{"Content-Type": {"text/plain"}, "Set-Cookie": {"a=1; Path=/", "b=2"}}
	if got := r.Overrides[0].Respond.Headers; !reflect.DeepEqual(got, want) {
		t.Fatalf("cabeçalhos lidos errado: %q", got)
	}
	out, err := MarshalRoute(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Content-Type: text/plain\n") {
		t.Fatalf("um valor único deveria ser gravado como texto:\n%s", out)
	}
	back, err := ParseRoute("h.yaml", out)
	if err != nil || !reflect.DeepEqual(back.Overrides[0].Respond.Headers, want) {
		t.Fatalf("a lista deveria sobreviver à ida e volta: %v %q", err, back.Overrides[0].Respond.Headers)
	}
	var viaJSON Respond
	if err := json.Unmarshal([]byte(`{"headers":{"Content-Type":"text/plain","Set-Cookie":["a=1; Path=/","b=2"]}}`), &viaJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(viaJSON.Headers, want) {
		t.Fatalf("cabeçalhos lidos errado por JSON: %q", viaJSON.Headers)
	}
	b, _ := json.Marshal(viaJSON)
	if string(b) != `{"headers":{"Content-Type":"text/plain","Set-Cookie":["a=1; Path=/","b=2"]}}` {
		t.Fatalf("serialização JSON inesperada: %s", b)
	}
	if err := json.Unmarshal([]byte(`{"headers":{"X":1}}`), &viaJSON); err == nil {
		t.Fatal("um valor numérico deveria ser recusado")
	}
}

func TestRespondHeaderWithoutValuesRejected(t *testing.T) {
	o := Override{Name: "v", Match: OverrideMatch{Path: "/x"}, Respond: &Respond{Headers: map[string]HeaderValues{"X-Vazio": {}}}}
	err := ValidateOverride("v.yaml", o)
	var es Errors
	if !errors.As(err, &es) || len(es) != 1 || es[0].Field != "override.respond.headers.X-Vazio" {
		t.Fatalf("a lista vazia deveria ser recusada: %v", err)
	}
	o.Respond.Headers["X-Vazio"] = HeaderValues{"v"}
	if err := ValidateOverride("v.yaml", o); err != nil {
		t.Fatalf("o override deveria ser válido: %v", err)
	}
}
