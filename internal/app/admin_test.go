package app

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
)

// API de administração de ponta a ponta: escritas pela porta de
// administração, efeito observado nos documentos em disco e no tráfego.

type adminEnv struct {
	*App
	dir     string // diretório do gateway.json
	routes  string // diretório de rotas
	api     string // URL base da API
	traffic string // URL base da porta de tráfego
}

// startAdmin sobe o processo com os documentos de rota dados.
func startAdmin(t *testing.T, gatewayJSON string, routes map[string]string) *adminEnv {
	t.Helper()
	return startAdminWith(t, gatewayJSON, routes, Options{})
}

// startAdminWith é startAdmin com as opções dadas; gatewayJSON vazio não
// cria o gateway.json.
func startAdminWith(t *testing.T, gatewayJSON string, routes map[string]string, opts Options) *adminEnv {
	t.Helper()
	dir := tempDir(t)
	if gatewayJSON != "" {
		writeFile(t, filepath.Join(dir, "gateway.json"), gatewayJSON)
	}
	for name, doc := range routes {
		writeFile(t, filepath.Join(dir, "routes", name), doc)
	}
	opts.Loader, opts.Web, opts.Log = loaderFor(filepath.Join(dir, "gateway.json")), testWeb, quietLog()
	a, err := Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Shutdown(t.Context()) })
	return &adminEnv{App: a, dir: dir, routes: filepath.Join(dir, "routes"),
		api: "http://" + a.AdminAddr() + "/api", traffic: "http://" + a.TrafficAddr()}
}

type apiResponse struct {
	status int
	header http.Header
	body   []byte
}

func (r apiResponse) decode(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("corpo não é JSON: %v: %s", err, r.body)
	}
}

func (r apiResponse) err(t *testing.T) apiErr {
	t.Helper()
	var e apiErr
	r.decode(t, &e)
	return e
}

type apiErr struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Field   string `json:"field"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Env     string `json:"env"`
}

// call faz uma requisição à API; contentType vazio usa JSON quando há corpo.
func (e *adminEnv) call(t *testing.T, method, path, contentType, body string, header ...string) apiResponse {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.api+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return apiResponse{res.StatusCode, res.Header, b}
}

// snapshotDir lê todos os arquivos do diretório de rotas.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range entries {
		b, err := os.ReadFile(filepath.Join(dir, en.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[en.Name()] = string(b)
	}
	return out
}

func routeDoc(name, upstream, path string) string {
	return "# rota " + name + "\nschemaVersion: 1\nname: " + name + "\nupstream: " + upstream + "\nmatch:\n  path: " + path + "\n"
}

// adminRoutes são três rotas com comentário, para verificar que a escrita
// numa delas não toca as outras.
func adminRoutes(t *testing.T) map[string]string {
	t.Helper()
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	return map[string]string{
		"payments.yaml":  routeDoc("payments", up.URL, "/payments/*"),
		"orders.yaml":    routeDoc("orders", up.URL, "/orders/*"),
		"inventory.yaml": routeDoc("inventory", up.URL, "/inventory/*"),
	}
}

type routeRes struct {
	File        string                     `json:"file"`
	HasComments bool                       `json:"hasComments"`
	Order       int                        `json:"order"`
	Route       config.Route               `json:"route"`
	State       map[string]json.RawMessage `json:"state"`
}

type overrideRes struct {
	Route    string          `json:"route"`
	Order    int             `json:"order"`
	Override config.Override `json:"override"`
	State    *struct {
		Active       bool    `json:"active"`
		Applications int64   `json:"applications"`
		Expired      *string `json:"expired"`
	} `json:"state"`
}

// Requirement: API de administração — rotas

func TestAdminRouteCRUD(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, adminRoutes(t))

	var list struct{ Items []routeRes }
	if r := e.call(t, "GET", "/routes", "", ""); r.status != 200 {
		t.Fatalf("listagem: %d %s", r.status, r.body)
	} else {
		r.decode(t, &list)
	}
	if len(list.Items) != 3 || !list.Items[0].HasComments {
		t.Fatalf("a listagem deveria trazer as três rotas, com hasComments: %+v", list.Items)
	}

	// Criação.
	r := e.call(t, "POST", "/routes", "", `{"name":"shipping","upstream":"`+up.URL+`","match":{"path":"/shipping/*"},"timeout":"3s"}`)
	if r.status != http.StatusCreated || r.header.Get("Location") != "/api/routes/shipping" || r.header.Get("ETag") == "" {
		t.Fatalf("criação: %d %v %s", r.status, r.header, r.body)
	}
	var created routeRes
	r.decode(t, &created)
	if created.Route.SchemaVersion != config.SchemaVersion || created.HasComments || created.Route.Timeout == nil {
		t.Fatalf("recurso criado: %+v", created)
	}
	// Scenario: Criação de rota gera documento próprio.
	doc := filepath.Join(e.routes, "shipping.yaml")
	if data, err := os.ReadFile(doc); err != nil || !strings.Contains(string(data), "name: shipping") {
		t.Fatalf("a criação deveria gerar routes/shipping.yaml: %v %s", err, data)
	}
	if st, body := getBody(t, e.traffic+"/shipping/x"); st != 200 || body != "upstream:/shipping/x" {
		t.Fatalf("a rota criada deveria atender sem reinício: %d %q", st, body)
	}

	// Leitura.
	var got routeRes
	r = e.call(t, "GET", "/routes/shipping", "", "")
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || got.Route.Upstream != up.URL || got.File != doc || r.header.Get("ETag") == "" {
		t.Fatalf("leitura: %d %+v", r.status, got)
	}

	// Substituição.
	r = e.call(t, "PUT", "/routes/shipping", "", `{"schemaVersion":1,"name":"shipping","upstream":"`+up.URL+`","match":{"path":"/shipping/*"},"timeout":"5s"}`)
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || got.Route.Timeout == nil || time.Duration(*got.Route.Timeout) != 5*time.Second {
		t.Fatalf("substituição: %d %s", r.status, r.body)
	}

	// Alteração por merge patch: null remove o campo.
	r = e.call(t, "PATCH", "/routes/shipping", "application/merge-patch+json", `{"stripPrefix":true,"timeout":null}`)
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || got.Route.Timeout != nil || !got.Route.StripPrefix {
		t.Fatalf("merge patch: %d %s", r.status, r.body)
	}
	if st, body := getBody(t, e.traffic+"/shipping/x"); st != 200 || body != "upstream:/x" {
		t.Fatalf("a alteração deveria valer no tráfego: %d %q", st, body)
	}
	if data, _ := os.ReadFile(doc); strings.Contains(string(data), "timeout") {
		t.Fatalf("o campo removido não deveria constar do documento: %s", data)
	}
	if r := e.call(t, "PATCH", "/routes/shipping", "application/merge-patch+json", `{"overrides":[]}`); r.status != 422 || r.err(t).Field != "overrides" {
		t.Fatalf("overrides no patch da rota deveria ser recusado: %d %s", r.status, r.body)
	}

	// Conflitos.
	if r := e.call(t, "POST", "/routes", "", `{"name":"shipping","match":{"path":"/outro/*"},"upstream":"`+up.URL+`"}`); r.status != 409 || r.err(t).Error != "conflict" {
		t.Fatalf("nome repetido: %d %s", r.status, r.body)
	}
	r = e.call(t, "POST", "/routes", "", `{"name":"clone","match":{"path":"/shipping/*"},"upstream":"`+up.URL+`"}`)
	if ae := r.err(t); r.status != 409 || ae.Error != "conflict" || !strings.Contains(ae.Message, "shipping.yaml") {
		t.Fatalf("casamento idêntico deveria nomear o arquivo em conflito: %d %s", r.status, r.body)
	}
	if _, err := os.Stat(filepath.Join(e.routes, "clone.yaml")); err == nil {
		t.Fatal("a criação recusada não deveria gerar documento")
	}
	r = e.call(t, "POST", "/routes", "", `{"name":"bad","match":{"path":"/bad/*"},"upstream":"ftp://x"}`)
	if ae := r.err(t); r.status != 422 || ae.Error != "invalid" || ae.Field != "upstream" || !strings.Contains(ae.Message, "ftp://x") {
		t.Fatalf("upstream inválido: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes", "", `{"name":"x","match":{"path":"/x"},"upstrem":"http://a"}`); r.status != 422 || r.err(t).Field != "upstrem" {
		t.Fatalf("campo desconhecido: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes", "", `{"name":`); r.status != 400 || r.err(t).Error != "bad_request" {
		t.Fatalf("JSON malformado: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes", "text/csv", `a,b`); r.status != 415 {
		t.Fatalf("Content-Type inesperado: %d %s", r.status, r.body)
	}

	// Remoção.
	if r := e.call(t, "DELETE", "/routes/shipping", "", ""); r.status != http.StatusNoContent {
		t.Fatalf("remoção: %d %s", r.status, r.body)
	}
	if _, err := os.Stat(doc); err == nil {
		t.Fatal("a remoção deveria apagar o documento")
	}
	if r := e.call(t, "GET", "/routes/shipping", "", ""); r.status != 404 || r.err(t).Error != "not_found" {
		t.Fatalf("rota removida: %d %s", r.status, r.body)
	}
	if st, _ := getBody(t, e.traffic+"/shipping/x"); st != http.StatusNotFound {
		t.Fatalf("a rota removida não deveria mais atender: %d", st)
	}
	if r := e.call(t, "DELETE", "/routes/shipping", "", ""); r.status != 404 {
		t.Fatalf("remoção de rota inexistente: %d", r.status)
	}
	if r := e.call(t, "POST", "/routes/payments", "", `{}`); r.status != http.StatusMethodNotAllowed || !strings.Contains(r.header.Get("Allow"), "PATCH") {
		t.Fatalf("método não suportado: %d %v", r.status, r.header)
	}
}

func TestAdminRouteRenameKeepsFile(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	var got routeRes
	r := e.call(t, "PATCH", "/routes/orders", "application/merge-patch+json", `{"name":"pedidos"}`)
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || got.Route.Name != "pedidos" || got.File != filepath.Join(e.routes, "orders.yaml") {
		t.Fatalf("renomeação: %d %s", r.status, r.body)
	}
	if e.call(t, "GET", "/routes/orders", "", "").status != 404 || e.call(t, "GET", "/routes/pedidos", "", "").status != 200 {
		t.Fatal("a rota deveria responder pelo nome novo")
	}

	// O nome antigo fica livre: como orders.yaml segue ocupado por pedidos,
	// a rota recriada ganha o primeiro arquivo livre, sem tocar o dele.
	pedidos, _ := os.ReadFile(filepath.Join(e.routes, "orders.yaml"))
	up := e.Live.Load().Route("pedidos").Upstream.String()
	r = e.call(t, "POST", "/routes", "", `{"name":"orders","upstream":"`+up+`","match":{"path":"/orders-v2/*"}}`)
	got = routeRes{}
	r.decode(t, &got)
	if r.status != http.StatusCreated || got.Route.Name != "orders" || got.File != filepath.Join(e.routes, "orders-2.yaml") {
		t.Fatalf("recriação do nome antigo: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(filepath.Join(e.routes, "orders.yaml")); string(now) != string(pedidos) {
		t.Fatalf("o documento de pedidos não deveria ser tocado:\n%s", now)
	}
	// O mesmo vale para a criação pelo documento bruto; um arquivo que não
	// corresponde a rota nenhuma não é sobrescrito, e o seguinte é usado.
	if r := e.call(t, "PATCH", "/routes/inventory", "application/merge-patch+json", `{"name":"estoque"}`); r.status != 200 {
		t.Fatalf("renomeação: %d %s", r.status, r.body)
	}
	orphan := filepath.Join(e.routes, "inventory-2.yaml")
	writeFile(t, orphan, "# arquivo de outra ferramenta\n")
	r = e.call(t, "PUT", "/routes/inventory/document", "application/yaml", routeDoc("inventory", up, "/inventory-v2/*"))
	got = routeRes{}
	r.decode(t, &got)
	if r.status != http.StatusCreated || got.File != filepath.Join(e.routes, "inventory-3.yaml") {
		t.Fatalf("recriação pelo documento: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(orphan); string(now) != "# arquivo de outra ferramenta\n" {
		t.Fatal("um arquivo que não é de rota nenhuma não deveria ser sobrescrito")
	}
}

func TestAdminRouteDocument(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	path := filepath.Join(e.routes, "payments.yaml")
	disk, _ := os.ReadFile(path)

	r := e.call(t, "GET", "/routes/payments/document", "", "")
	etag := r.header.Get("ETag")
	if r.status != 200 || string(r.body) != string(disk) || !strings.HasPrefix(r.header.Get("Content-Type"), "application/yaml") || etag == "" {
		t.Fatalf("documento bruto: %d %q %v", r.status, r.body, r.header)
	}

	// O texto é gravado como enviado, comentários incluídos.
	text := strings.Replace(string(disk), "# rota payments", "# pagamentos, editado à mão", 1) + "stripPrefix: true\n"
	r = e.call(t, "PUT", "/routes/payments/document", "application/yaml", text, "If-Match", etag)
	var got routeRes
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || !got.Route.StripPrefix || !got.HasComments || r.header.Get("ETag") == etag {
		t.Fatalf("gravação do documento: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(path); string(now) != text {
		t.Fatalf("o documento deveria ser gravado como enviado:\n%s", now)
	}
	if st, body := getBody(t, e.traffic+"/payments/x"); st != 200 || body != "upstream:/x" {
		t.Fatalf("o documento gravado deveria valer no tráfego: %d %q", st, body)
	}

	// If-Match desatualizado.
	if r := e.call(t, "PUT", "/routes/payments/document", "application/yaml", text+"rewriteHost: true\n", "If-Match", etag); r.status != 412 || r.err(t).Error != "stale" {
		t.Fatalf("If-Match desatualizado: %d %s", r.status, r.body)
	}
	// Nome diferente do path.
	r = e.call(t, "PUT", "/routes/payments/document", "application/yaml", strings.Replace(text, "name: payments", "name: outra", 1))
	if ae := r.err(t); r.status != 422 || ae.Field != "name" || ae.Line != 3 {
		t.Fatalf("nome divergente: %d %s", r.status, r.body)
	}
	// Erro de validação localizado no texto enviado.
	bad := text + "overrides:\n  - name: flaky\n    match:\n      path: /payments/x\n    respond:\n      status: 503\n    probability: 1.5\n"
	r = e.call(t, "PUT", "/routes/payments/document", "application/yaml", bad)
	if ae := r.err(t); r.status != 422 || ae.Field != "overrides[0].probability" || ae.Line == 0 || ae.Column == 0 || ae.File != path {
		t.Fatalf("validação localizada: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(path); string(now) != text {
		t.Fatal("o documento inválido não deveria ser gravado")
	}
	if r := e.call(t, "PUT", "/routes/payments/document", "application/json", text); r.status != 415 {
		t.Fatalf("Content-Type inesperado: %d", r.status)
	}

	// Rota inexistente é criada.
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	r = e.call(t, "PUT", "/routes/novo/document", "application/yaml", routeDoc("novo", up.URL, "/novo/*"))
	if r.status != 201 {
		t.Fatalf("criação pelo documento: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(filepath.Join(e.routes, "novo.yaml")); string(now) != routeDoc("novo", up.URL, "/novo/*") {
		t.Fatalf("o documento criado deveria ser o enviado: %s", now)
	}
	if e.call(t, "GET", "/routes/nada/document", "", "").status != 404 {
		t.Fatal("documento de rota inexistente deveria responder 404")
	}
}

// Requirement: API de administração — overrides

func TestAdminOverrideCRUD(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	base := "/routes/payments/overrides"

	r := e.call(t, "POST", base, "", `{"name":"flaky","match":{"path":"/payments/charge","method":"POST"},"respond":{"status":503,"body":{"erro":"indisponível"}},"ttl":"10m"}`)
	if r.status != http.StatusCreated || r.header.Get("Location") != "/api/routes/payments/overrides/flaky" {
		t.Fatalf("criação do override: %d %s", r.status, r.body)
	}
	var o overrideRes
	o = overrideRes{}
	r.decode(t, &o)
	if o.Route != "payments" || o.Override.Name != "flaky" || o.State == nil || !o.State.Active {
		t.Fatalf("recurso do override: %s", r.body)
	}
	post := func() (int, string) {
		res, err := http.Post(e.traffic+"/payments/charge", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, body := post(); st != 503 || !strings.Contains(body, "indisponível") {
		t.Fatalf("o override criado deveria interceptar: %d %q", st, body)
	}

	if r := e.call(t, "POST", base, "", `{"name":"flaky","match":{"path":"/x"},"drop":true}`); r.status != 409 || r.err(t).Error != "conflict" {
		t.Fatalf("nome repetido: %d %s", r.status, r.body)
	}

	// Leitura e listagem.
	r = e.call(t, "GET", base+"/flaky", "", "")
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.State.Applications != 1 || r.header.Get("ETag") == "" {
		t.Fatalf("leitura do override: %d %s", r.status, r.body)
	}
	var list struct{ Items []overrideRes }
	e.call(t, "GET", base, "", "").decode(t, &list)
	if len(list.Items) != 1 || list.Items[0].Override.Name != "flaky" {
		t.Fatalf("listagem dos overrides: %+v", list)
	}
	var route routeRes
	e.call(t, "GET", "/routes/payments", "", "").decode(t, &route)
	if len(route.Route.Overrides) != 1 || route.State["flaky"] == nil {
		t.Fatalf("o recurso rota deveria trazer o override e seu estado: %+v", route)
	}

	// Liga/desliga preserva os demais campos.
	r = e.call(t, "PATCH", base+"/flaky", "application/merge-patch+json", `{"enabled":false}`)
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.Override.Enabled() || o.Override.Respond == nil || o.Override.Respond.Status != 503 || o.Override.TTL == nil {
		t.Fatalf("desligar: %d %s", r.status, r.body)
	}
	if st, body := post(); st != 200 || body != "upstream:/payments/charge" {
		t.Fatalf("desligado, o override não deveria interceptar: %d %q", st, body)
	}
	r = e.call(t, "PATCH", base+"/flaky", "application/merge-patch+json", `{"enabled":true,"probability":1}`)
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || !o.Override.Enabled() || o.Override.Probability == nil {
		t.Fatalf("religar: %d %s", r.status, r.body)
	}
	if st, _ := post(); st != 503 {
		t.Fatalf("religado, o override deveria interceptar: %d", st)
	}

	// Substituição na mesma posição, com renomeação.
	r = e.call(t, "PUT", base+"/flaky", "", `{"name":"lento","match":{"path":"/payments/charge"},"respond":{"status":500}}`)
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.Override.Name != "lento" || o.Override.Respond.Status != 500 {
		t.Fatalf("substituição: %d %s", r.status, r.body)
	}
	if e.call(t, "GET", base+"/flaky", "", "").status != 404 {
		t.Fatal("o nome antigo não deveria mais existir")
	}

	// Estado vivo e reinício.
	post()
	var states struct {
		Now   time.Time
		Items []struct {
			Route, Override string
			Applications    int64
		}
	}
	e.call(t, "GET", "/overrides/state", "", "").decode(t, &states)
	if len(states.Items) != 1 || states.Items[0].Override != "lento" || states.Items[0].Applications != 1 || states.Now.IsZero() {
		t.Fatalf("estado vivo: %+v", states)
	}
	r = e.call(t, "POST", base+"/lento/reset", "", "")
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.State.Applications != 0 {
		t.Fatalf("reinício: %d %s", r.status, r.body)
	}

	// Remoção.
	if r := e.call(t, "DELETE", base+"/lento", "", ""); r.status != http.StatusNoContent {
		t.Fatalf("remoção do override: %d %s", r.status, r.body)
	}
	if st, _ := post(); st != 200 {
		t.Fatalf("removido, o override não deveria interceptar: %d", st)
	}
	for _, p := range []string{base + "/lento", "/routes/nada/overrides", base + "/lento/reset"} {
		method := "GET"
		if strings.HasSuffix(p, "reset") {
			method = "POST"
		}
		if r := e.call(t, method, p, "", ""); r.status != 404 || r.err(t).Error != "not_found" {
			t.Fatalf("%s inexistente: %d %s", p, r.status, r.body)
		}
	}
	if r := e.call(t, "PATCH", base+"/lento", "application/merge-patch+json", `{"enabled":false}`); r.status != 404 {
		t.Fatalf("patch de override inexistente: %d %s", r.status, r.body)
	}
}

// Scenario: Alteração atinge apenas o documento da rota

func TestAdminWriteTouchesOnlyRouteDocument(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	before := snapshotDir(t, e.routes)
	r := e.call(t, "POST", "/routes/payments/overrides", "", `{"name":"flaky","match":{"path":"/payments/x"},"respond":{"status":503}}`)
	if r.status != 201 {
		t.Fatalf("criação do override: %d %s", r.status, r.body)
	}
	after := snapshotDir(t, e.routes)
	if len(after) != len(before) {
		t.Fatalf("nenhum arquivo deveria surgir ou sumir: %v", slices.Sorted(maps.Keys(after)))
	}
	for name, content := range before {
		if name == "payments.yaml" {
			if after[name] == content {
				t.Fatal("payments.yaml deveria ter sido reescrito")
			}
			continue
		}
		if after[name] != content {
			t.Fatalf("%s deveria permanecer byte a byte igual:\nantes:\n%s\ndepois:\n%s", name, content, after[name])
		}
	}
}

// Scenario: Escritas concorrentes são serializadas

func TestAdminConcurrentWritesSerialized(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	routes := []string{"payments", "orders", "inventory"}
	const perRoute = 5
	var wg sync.WaitGroup
	for _, route := range routes {
		for i := range perRoute {
			wg.Go(func() {
				body := fmt.Sprintf(`{"name":"o%d","match":{"path":"/%s/o%d"},"respond":{"status":418}}`, i, route, i)
				if r := e.call(t, "POST", "/routes/"+route+"/overrides", "", body); r.status != 201 {
					t.Errorf("%s/o%d: %d %s", route, i, r.status, r.body)
				}
			})
		}
	}
	wg.Wait()
	for _, route := range routes {
		data, err := os.ReadFile(filepath.Join(e.routes, route+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := config.ParseRoute(route, data)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Overrides) != perRoute {
			t.Fatalf("%s deveria ter os %d overrides gravados, tem %d", route, perRoute, len(doc.Overrides))
		}
		if got := len(e.Live.Load().Route(route).Overrides); got != perRoute {
			t.Fatalf("o snapshot de %s deveria ter os %d overrides, tem %d", route, perRoute, got)
		}
	}
}

// Scenario: Alteração inválida é recusada sem tocar o disco

func TestAdminInvalidChangeLeavesDiskUntouched(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	before := snapshotDir(t, e.routes)
	mtimes := map[string]time.Time{}
	for name := range before {
		st, _ := os.Stat(filepath.Join(e.routes, name))
		mtimes[name] = st.ModTime()
	}
	snap := e.Live.Load()

	r := e.call(t, "POST", "/routes/payments/overrides", "", `{"name":"lento","match":{"path":"/payments/x"},"latency":{"min":"2s","max":"1s"}}`)
	ae := r.err(t)
	if r.status != 422 || ae.Error != "invalid" || ae.Field != "overrides[0].latency.min" || !strings.Contains(ae.Message, "maior que o máximo") ||
		!strings.HasSuffix(ae.File, "payments.yaml") {
		t.Fatalf("latência mínima maior que a máxima: %d %s", r.status, r.body)
	}
	r = e.call(t, "PATCH", "/routes/orders", "application/merge-patch+json", `{"upstream":"não é url"}`)
	if r.status != 422 || r.err(t).Field != "upstream" {
		t.Fatalf("upstream inválido: %d %s", r.status, r.body)
	}
	after := snapshotDir(t, e.routes)
	if len(after) != len(before) {
		t.Fatalf("nenhum arquivo deveria surgir, nem temporário: %d → %d", len(before), len(after))
	}
	for name, content := range before {
		st, _ := os.Stat(filepath.Join(e.routes, name))
		if after[name] != content || !st.ModTime().Equal(mtimes[name]) {
			t.Fatalf("%s não deveria ser tocado", name)
		}
	}
	if e.Live.Load() != snap {
		t.Fatal("a configuração em vigor não deveria mudar")
	}
}

// Requirement: Recarga sem reinício

func TestReloadAppliesNewRoute(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, adminRoutes(t))
	if st, _ := getBody(t, e.traffic+"/novo/x"); st != 404 {
		t.Fatalf("antes da recarga a rota não existe: %d", st)
	}
	writeFile(t, filepath.Join(e.routes, "novo.yaml"), routeDoc("novo", up.URL, "/novo/*"))
	r := e.call(t, "POST", "/reload", "", "")
	var res struct {
		Routes   int
		Changed  struct{ Routes, Settings []string }
		Warnings []string
	}
	r.decode(t, &res)
	if r.status != 200 || res.Routes != 4 || !slices.Equal(res.Changed.Routes, []string{"novo"}) || len(res.Changed.Settings) != 0 || res.Warnings == nil {
		t.Fatalf("recarga: %d %s", r.status, r.body)
	}
	if st, body := getBody(t, e.traffic+"/novo/x"); st != 200 || body != "upstream:/novo/x" {
		t.Fatalf("a nova rota deveria atender sem reinício: %d %q", st, body)
	}
}

func TestReloadInvalidKeepsPrevious(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	path := filepath.Join(e.routes, "payments.yaml")
	orig, _ := os.ReadFile(path)
	writeFile(t, path, string(orig)+"overrides:\n  - name: flaky\n    match:\n      path: /payments/x\n    respond:\n      status: 503\n    probability: 1.5\n")
	snap := e.Live.Load()

	r := e.call(t, "POST", "/reload", "", "")
	ae := r.err(t)
	if r.status != 422 || ae.Error != "invalid" || ae.File != path || ae.Field != "overrides[0].probability" || ae.Line != 13 {
		t.Fatalf("recarga inválida: %d %s", r.status, r.body)
	}
	if e.Live.Load() != snap {
		t.Fatal("a configuração anterior deveria continuar em vigor")
	}
	if st, body := getBody(t, e.traffic+"/payments/x"); st != 200 || body != "upstream:/payments/x" {
		t.Fatalf("as rotas anteriores deveriam continuar atendendo: %d %q", st, body)
	}
}

func TestReloadCollisionIsConflict(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, adminRoutes(t))
	writeFile(t, filepath.Join(e.routes, "copia.yaml"), routeDoc("payments", up.URL, "/copia/*"))
	r := e.call(t, "POST", "/reload", "", "")
	if ae := r.err(t); r.status != 409 || ae.Error != "conflict" || !strings.Contains(ae.Message, "payments.yaml") || !strings.Contains(ae.Message, "copia.yaml") {
		t.Fatalf("colisão na recarga: %d %s", r.status, r.body)
	}
}

// Scenario: Requisições em andamento sobrevivem à recarga

func TestReloadInFlightRequestsFinishUnderOldConfig(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	old := httpServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		io.WriteString(w, "antigo")
	})
	next := httpServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "novo") })
	e := startAdmin(t, freePorts, map[string]string{"svc.yaml": routeDoc("svc", old, "/svc/*")})

	type result struct {
		status int
		body   string
		gw     string
	}
	done := make(chan result, 1)
	go func() {
		res, err := http.Get(e.traffic + "/svc/lento")
		if err != nil {
			done <- result{}
			return
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		done <- result{res.StatusCode, string(b), res.Header.Get("X-Gateway")}
	}()
	<-arrived

	writeFile(t, filepath.Join(e.routes, "svc.yaml"), "schemaVersion: 1\nname: svc\nupstream: "+next+"\nmatch:\n  path: /svc/*\nstripPrefix: true\n")
	if r := e.call(t, "POST", "/reload", "", ""); r.status != 200 {
		t.Fatalf("recarga: %d %s", r.status, r.body)
	}
	if st, body := getBody(t, e.traffic+"/svc/x"); st != 200 || body != "novo" {
		t.Fatalf("requisições novas deveriam usar a configuração nova: %d %q", st, body)
	}
	close(release)
	select {
	case res := <-done:
		if res.status != 200 || res.body != "antigo" || !strings.Contains(res.gw, "route=svc") {
			t.Fatalf("a requisição em curso deveria concluir sob a configuração antiga: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a requisição em curso não concluiu")
	}
}

func httpServer(t *testing.T, fn http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(fn)
	t.Cleanup(s.Close)
	return s.URL
}

// A recarga segue as regras da alteração pela API: a porta nova é aberta a
// quente.
func TestReloadAppliesPortChange(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	old := e.TrafficAddr()
	port := freePort(t)
	writeFile(t, filepath.Join(e.dir, "gateway.json"), `{"ports":{"traffic":`+fmt.Sprint(port)+`,"admin":0},"seed":7}`)
	r := e.call(t, "POST", "/reload", "", "")
	var res struct{ Changed struct{ Settings []string } }
	r.decode(t, &res)
	if r.status != 200 || !slices.Contains(res.Changed.Settings, "ports.traffic") {
		t.Fatalf("troca de porta na recarga: %d %s", r.status, r.body)
	}
	if st, body := getBody(t, fmt.Sprintf("http://127.0.0.1:%d/payments/x", port)); st != 200 || body != "upstream:/payments/x" {
		t.Fatalf("a porta nova deveria atender: %d %q", st, body)
	}
	waitRefused(t, old)
	if s := e.Live.Load().Settings; s.TrafficPort != port || s.Seed == nil || *s.Seed != 7 {
		t.Fatalf("a configuração recarregada deveria valer: %+v", s)
	}
}

func TestReloadUnavailablePortKeepsCurrent(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	busy := occupy(t)
	snap := e.Live.Load()
	writeFile(t, filepath.Join(e.dir, "gateway.json"), `{"ports":{"traffic":`+fmt.Sprint(busy)+`,"admin":0},"seed":7}`)
	r := e.call(t, "POST", "/reload", "", "")
	if ae := r.err(t); r.status != 409 || ae.Error != "port_unavailable" || ae.Field != "ports.traffic" || !strings.Contains(ae.Message, fmt.Sprint(busy)) {
		t.Fatalf("porta ocupada na recarga: %d %s", r.status, r.body)
	}
	if e.Live.Load() != snap || e.Live.Load().Settings.Seed != nil {
		t.Fatal("a configuração anterior deveria continuar em vigor, seed incluído")
	}
	if st, _ := getBody(t, e.traffic+"/payments/x"); st != 200 {
		t.Fatalf("a porta atual deveria seguir atendendo: %d", st)
	}
}

func TestReloadAppliesProcessSettings(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	path := filepath.Join(e.dir, "history.db")
	writeFile(t, filepath.Join(e.dir, "gateway.json"),
		`{"ports":{"traffic":0,"admin":0},"seed":42,"history":{"backend":"sqlite","path":`+jsonString(path)+`,"expose":false}}`)
	r := e.call(t, "POST", "/reload", "", "")
	var res struct{ Changed struct{ Settings []string } }
	r.decode(t, &res)
	if r.status != 200 || !slices.Contains(res.Changed.Settings, "seed") || !slices.Contains(res.Changed.Settings, "history.backend") {
		t.Fatalf("recarga do processo: %d %s", r.status, r.body)
	}
	if s := e.Live.Load().Settings; s.Seed == nil || *s.Seed != 42 || e.History.Backend() != config.BackendSQLite {
		t.Fatalf("seed e backend deveriam valer sem reinício: %+v, backend %s", s, e.History.Backend())
	}
	// A exposição desligada pela recarga responde "desabilitado", não uma
	// lista vazia.
	if r := e.call(t, "GET", "/exchanges", "", ""); r.status != 403 || r.err(t).Error != "history_disabled" {
		t.Fatalf("exposição desligada pela recarga: %d %s", r.status, r.body)
	}
}

func TestReloadUnavailableBackendKeepsCurrent(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	blocker := filepath.Join(e.dir, "arquivo-comum")
	writeFile(t, blocker, "x")
	writeFile(t, filepath.Join(e.dir, "gateway.json"),
		`{"ports":{"traffic":0,"admin":0},"seed":1,"history":{"backend":"sqlite","path":"arquivo-comum/history.db"}}`)
	snap := e.Live.Load()
	r := e.call(t, "POST", "/reload", "", "")
	if ae := r.err(t); r.status != 409 || ae.Error != "backend_unavailable" || !strings.Contains(ae.Message, "sqlite") || !strings.Contains(ae.Message, "memory") {
		t.Fatalf("backend indisponível na recarga: %d %s", r.status, r.body)
	}
	if e.History.Backend() != config.BackendMemory || e.Live.Load() != snap {
		t.Fatal("o backend e a configuração atuais deveriam seguir em uso")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Scenario: Origem efetiva consultável

func TestSettingsEffectiveOrigin(t *testing.T) {
	t.Setenv("GATEWAY_TRAFFIC_PORT", "0")
	e := startAdmin(t, `{"ports":{"admin":0},"seed":42}`, nil)
	r := e.call(t, "GET", "/settings", "", "")
	var v struct {
		File struct {
			Path   string
			Exists bool
		}
		Values []struct {
			Key    string
			Env    string
			Value  any
			Source config.Source
			Locked bool
		}
	}
	r.decode(t, &v)
	if r.status != 200 || !v.File.Exists || v.File.Path != filepath.Join(e.dir, "gateway.json") {
		t.Fatalf("configuração efetiva: %d %s", r.status, r.body)
	}
	byKey := map[string]int{}
	for i, x := range v.Values {
		byKey[x.Key] = i
	}
	traffic, seed, backend := v.Values[byKey["ports.traffic"]], v.Values[byKey["seed"]], v.Values[byKey["history.backend"]]
	if traffic.Source != (config.Source{Origin: config.OriginEnv, Name: "GATEWAY_TRAFFIC_PORT"}) || !traffic.Locked {
		t.Fatalf("a porta deveria vir do ambiente, travada: %+v", traffic)
	}
	if seed.Source != (config.Source{Origin: config.OriginFile, Name: v.File.Path}) || seed.Locked || seed.Value != float64(42) {
		t.Fatalf("o seed deveria vir do arquivo: %+v", seed)
	}
	if backend.Source.Origin != config.OriginDefault || backend.Value != config.BackendMemory || backend.Env != "GATEWAY_HISTORY_BACKEND" {
		t.Fatalf("o backend deveria vir do padrão: %+v", backend)
	}
	if len(v.Values) != len(e.Live.Load().Settings.Effective()) {
		t.Fatalf("todos os valores deveriam aparecer: %d", len(v.Values))
	}
}

// Requirement: API de administração — toda escrita aceita If-Match: a versão
// atual grava e devolve o ETag novo; uma versão antiga responde 412 stale sem
// tocar o documento.

func TestAdminIfMatchOnRouteAndOverrideWrites(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	path := filepath.Join(e.routes, "payments.yaml")
	route := "/routes/payments"
	base := route + "/overrides"
	upstream := e.Live.Load().Route("payments").Upstream.String()

	etag := func() string {
		t.Helper()
		r := e.call(t, "GET", route, "", "")
		if r.status != 200 || r.header.Get("ETag") == "" {
			t.Fatalf("leitura da rota: %d %v", r.status, r.header)
		}
		return r.header.Get("ETag")
	}
	writes := []struct {
		name, method, path, contentType, body string
		want                                  int
	}{
		{"PUT da rota", "PUT", route, "", `{"schemaVersion":1,"name":"payments","upstream":"` + upstream + `","match":{"path":"/payments/*"},"timeout":"5s"}`, 200},
		{"PATCH da rota", "PATCH", route, "application/merge-patch+json", `{"stripPrefix":true}`, 200},
		{"POST de override", "POST", base, "", `{"name":"flaky","match":{"path":"/payments/x"},"respond":{"status":503}}`, 201},
		{"PUT de override", "PUT", base + "/flaky", "", `{"name":"flaky","match":{"path":"/payments/x"},"respond":{"status":500}}`, 200},
		{"PATCH de override", "PATCH", base + "/flaky", "application/merge-patch+json", `{"enabled":false}`, 200},
		{"DELETE de override", "DELETE", base + "/flaky", "", "", 204},
		{"DELETE da rota", "DELETE", route, "", "", 204},
	}
	for _, w := range writes {
		cur := etag()
		// Uma versão que não é a atual é recusada sem gravar.
		stale := `"` + strings.Repeat("0", 16) + `"`
		before := stateOf(t, path)
		r := e.call(t, w.method, w.path, w.contentType, w.body, "If-Match", stale)
		if r.status != http.StatusPreconditionFailed || r.err(t).Error != "stale" {
			t.Fatalf("%s com If-Match desatualizado: %d %s", w.name, r.status, r.body)
		}
		if stateOf(t, path) != before {
			t.Fatalf("%s recusado não deveria tocar o documento", w.name)
		}
		// A versão atual grava.
		r = e.call(t, w.method, w.path, w.contentType, w.body, "If-Match", cur)
		if r.status != w.want {
			t.Fatalf("%s com If-Match atual: %d %s", w.name, r.status, r.body)
		}
		if w.want == 204 {
			continue
		}
		if next := r.header.Get("ETag"); next == "" || next == cur {
			t.Fatalf("%s deveria devolver o ETag novo: antes %q, depois %q", w.name, cur, next)
		}
		// O ETag de antes da escrita agora é antigo.
		before = stateOf(t, path)
		if r := e.call(t, w.method, w.path, w.contentType, w.body, "If-Match", cur); r.status != http.StatusPreconditionFailed {
			t.Fatalf("%s com o ETag de antes da escrita: %d %s", w.name, r.status, r.body)
		}
		if stateOf(t, path) != before {
			t.Fatalf("%s recusado não deveria tocar o documento", w.name)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a remoção com If-Match atual deveria apagar o documento")
	}
}
