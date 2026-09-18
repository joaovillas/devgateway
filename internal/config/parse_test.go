package config

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

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
