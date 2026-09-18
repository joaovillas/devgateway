package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gamerjp64/gateway/internal/config"
)

// Requirement: Derivação de override a partir de troca capturada

// chargeUpstream responde como um serviço de pagamentos, com cabeçalhos
// próprios, um cabeçalho repetido e corpo JSON compacto.
func chargeUpstream(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.Copy(io.Discard, r.Body)
		switch r.URL.Path {
		case "/payments/charge":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Request-Cost", "7")
			w.Header().Add("Set-Cookie", "a=1")
			w.Header().Add("Set-Cookie", "b=2")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"amount":100,"id":"ch_1","status":"paid"}`)
		case "/payments/receipt":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			io.WriteString(w, "recibo 42\nobrigado")
		default:
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, strings.Repeat("abcdefghij", 10))
		}
	}))
	t.Cleanup(s.Close)
	return s
}

type deriveDraft struct {
	Route    string          `json:"route"`
	Override config.Override `json:"override"`
	Warnings []string        `json:"warnings"`
}

// lastExchangeID devolve o identificador da troca mais nova do histórico.
func (e *adminEnv) lastExchangeID(t *testing.T) string {
	t.Helper()
	var l listBody
	if r := e.call(t, "GET", "/exchanges?limit=1", "", ""); r.status != 200 {
		t.Fatalf("listagem: %d %s", r.status, r.body)
	} else {
		r.decode(t, &l)
	}
	if len(l.Items) == 0 {
		t.Fatal("o histórico deveria ter a troca")
	}
	return l.Items[0].ID
}

type observed struct {
	status int
	header http.Header
	body   string
}

func post(t *testing.T, url, body string) observed {
	t.Helper()
	res, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return observed{res.StatusCode, res.Header, string(b)}
}

// Scenario: Override derivado reproduz a troca observada — com revisão antes
// de valer: o rascunho não grava nada, e o override criado a partir dele
// passa a responder como o upstream respondeu.

func TestDerivedOverrideReproducesExchange(t *testing.T) {
	var hits atomic.Int64
	up := chargeUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{"payments.yaml": routeDoc("payments", up.URL, "/payments/*")})
	doc := filepath.Join(e.routes, "payments.yaml")

	real := post(t, e.traffic+"/payments/charge", `{"amount":100}`)
	id := e.lastExchangeID(t)
	before := stateOf(t, doc)

	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`"}`)
	var d deriveDraft
	r.decode(t, &d)
	o := d.Override
	if r.status != 200 || d.Route != "payments" || len(d.Warnings) != 0 {
		t.Fatalf("rascunho: %d %s", r.status, r.body)
	}
	if o.Name != "post-payments-charge" || o.Match.Path != "/payments/charge" || o.Match.Method != "POST" ||
		o.Respond == nil || o.Respond.Status != 201 || o.Enabled() != true {
		t.Fatalf("critérios e resposta do rascunho: %+v", o)
	}
	if o.Source == nil || o.Source.Kind != config.SourceDerived || o.Source.Exchange != id || o.Source.At.IsZero() || o.Source.BodyIncomplete {
		t.Fatalf("origem do rascunho: %+v", o.Source)
	}
	if !slices.Equal(o.Respond.Headers["Set-Cookie"], []string{"a=1", "b=2"}) || o.Respond.Headers["X-Request-Cost"][0] != "7" {
		t.Fatalf("cabeçalhos do rascunho: %v", o.Respond.Headers)
	}
	for _, k := range []string{"Date", "Content-Length", "X-Gateway"} {
		if _, ok := o.Respond.Headers[k]; ok {
			t.Fatalf("%s não deveria ir para a resposta declarada", k)
		}
	}
	if stateOf(t, doc) != before {
		t.Fatal("o rascunho não deveria gravar nada")
	}
	// Até ser criado, o rascunho não vale: o upstream segue respondendo.
	if post(t, e.traffic+"/payments/charge", `{}`); hits.Load() != 2 {
		t.Fatalf("o rascunho não deveria interceptar: %d chamadas ao upstream", hits.Load())
	}

	// Revisão: o nome é trocado e o override é criado.
	o.Name = "charge-ok"
	body, _ := json.Marshal(o)
	if r := e.call(t, "POST", "/routes/payments/overrides", "", string(body)); r.status != 201 {
		t.Fatalf("criação do override revisado: %d %s", r.status, r.body)
	}
	got := post(t, e.traffic+"/payments/charge", `{"amount":100}`)
	if hits.Load() != 2 {
		t.Fatalf("o override deveria responder sem o upstream: %d chamadas", hits.Load())
	}
	if got.status != real.status || got.body != real.body {
		t.Fatalf("a resposta deveria reproduzir a observada:\nreal: %d %s\nderivada: %d %s", real.status, real.body, got.status, got.body)
	}
	for _, k := range []string{"Content-Type", "X-Request-Cost", "Set-Cookie", "Content-Length"} {
		if !slices.Equal(got.header.Values(k), real.header.Values(k)) {
			t.Fatalf("cabeçalho %s: real %q, derivado %q", k, real.header.Values(k), got.header.Values(k))
		}
	}
	if gw := got.header.Get("X-Gateway"); !strings.Contains(gw, "override=payments/charge-ok") || !strings.Contains(gw, "intervention=synthesized") {
		t.Fatalf("a resposta deveria identificar a intervenção: %q", gw)
	}
}

// save grava direto; corpo de texto é reproduzido byte a byte.
func TestDeriveAndSave(t *testing.T) {
	var hits atomic.Int64
	up := chargeUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{"payments.yaml": routeDoc("payments", up.URL, "/payments/*")})

	real := fetch(e.traffic + "/payments/receipt")
	id := e.lastExchangeID(t)
	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`","name":"recibo","save":true}`)
	var res struct {
		overrideRes
		Warnings []string `json:"warnings"`
	}
	r.decode(t, &res)
	if r.status != 201 || r.header.Get("Location") != "/api/routes/payments/overrides/recibo" || res.Override.Name != "recibo" ||
		res.Override.Source == nil || res.Override.Source.Kind != config.SourceDerived || res.Warnings == nil {
		t.Fatalf("derivação gravada: %d %s", r.status, r.body)
	}
	data, _ := os.ReadFile(filepath.Join(e.routes, "payments.yaml"))
	if !strings.Contains(string(data), "kind: derived") || !strings.Contains(string(data), "exchange: "+id) {
		t.Fatalf("o documento deveria registrar a origem:\n%s", data)
	}
	got := fetch(e.traffic + "/payments/receipt")
	if hits.Load() != 1 || got.status != real.status || got.body != real.body {
		t.Fatalf("o override gravado deveria reproduzir a troca: %+v, real %+v", got, real)
	}
	// O nome já usado é conflito.
	r = e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`","name":"recibo","save":true}`)
	if r.status != 409 || r.err(t).Error != "conflict" {
		t.Fatalf("nome repetido: %d %s", r.status, r.body)
	}
}

// Scenario: Troca inexistente

func TestDeriveUnknownExchange(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	before := snapshotDir(t, e.routes)
	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"01K5E3V3C8Q2M4Z8N6P0R2T4W6","save":true}`)
	if ae := r.err(t); r.status != 404 || ae.Error != "not_found" || !strings.Contains(ae.Message, "01K5E3V3C8Q2M4Z8N6P0R2T4W6") ||
		!strings.Contains(ae.Message, "não encontrada") {
		t.Fatalf("troca inexistente: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes/nenhuma/overrides/derive", "", `{"exchange":"x"}`); r.status != 404 {
		t.Fatalf("rota inexistente: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{}`); r.status != 422 || r.err(t).Field != "exchange" {
		t.Fatalf("sem troca: %d %s", r.status, r.body)
	}
	if after := snapshotDir(t, e.routes); !mapsEqual(before, after) {
		t.Fatal("nenhum documento deveria mudar")
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Scenario: Derivação a partir de troca truncada — o override é criado
// sinalizando explicitamente que o corpo está incompleto.

func TestDeriveTruncatedExchange(t *testing.T) {
	var hits atomic.Int64
	up := chargeUpstream(t, &hits)
	e := startAdmin(t, `{"ports":{"traffic":0,"admin":0},"capture":{"maxBodyBytes":16}}`,
		map[string]string{"payments.yaml": routeDoc("payments", up.URL, "/payments/*")})
	getBody(t, e.traffic+"/payments/longo")
	id := e.lastExchangeID(t)

	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`"}`)
	var d deriveDraft
	r.decode(t, &d)
	if r.status != 200 || d.Override.Source == nil || !d.Override.Source.BodyIncomplete || len(d.Warnings) != 1 ||
		!strings.Contains(d.Warnings[0], "truncado") || !strings.Contains(d.Warnings[0], "16 de 100") {
		t.Fatalf("rascunho de troca truncada: %d %s", r.status, r.body)
	}
	r = e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`","save":true}`)
	if r.status != 201 {
		t.Fatalf("gravação de troca truncada: %d %s", r.status, r.body)
	}
	data, _ := os.ReadFile(filepath.Join(e.routes, "payments.yaml"))
	if !strings.Contains(string(data), "bodyIncomplete: true") {
		t.Fatalf("o documento deveria sinalizar o corpo incompleto:\n%s", data)
	}
}

// Só uma resposta do upstream é reproduzível; e sem exposição do histórico a
// troca não é lida.

func TestDeriveRefusesExchangeWithoutUpstreamResponse(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	getBody(t, e.traffic+"/sem-rota")
	id := e.lastExchangeID(t)
	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"`+id+`"}`)
	if ae := r.err(t); r.status != 422 || ae.Field != "exchange" || !strings.Contains(ae.Message, "gateway") {
		t.Fatalf("troca respondida pelo gateway: %d %s", r.status, r.body)
	}
}

func TestDeriveWithHistoryHidden(t *testing.T) {
	e := startAdmin(t, `{"ports":{"traffic":0,"admin":0},"history":{"expose":false}}`, adminRoutes(t))
	getBody(t, e.traffic+"/payments/x")
	r := e.call(t, "POST", "/routes/payments/overrides/derive", "", `{"exchange":"01K5E3V3C8Q2M4Z8N6P0R2T4W6"}`)
	if r.status != 403 || r.err(t).Error != "history_disabled" {
		t.Fatalf("derivação sem exposição: %d %s", r.status, r.body)
	}
}
