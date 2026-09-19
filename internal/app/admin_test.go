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

	"github.com/joaovillas/devgateway/internal/config"
)

// The admin API end to end: writes through the admin port, with the effect
// observed in the documents on disk and in the traffic.

type adminEnv struct {
	*App
	dir     string // directory holding gateway.json
	routes  string // routes directory
	api     string // base URL of the API
	traffic string // base URL of the traffic port
}

// startAdmin brings the process up with the given route documents.
func startAdmin(t *testing.T, gatewayJSON string, routes map[string]string) *adminEnv {
	t.Helper()
	return startAdminWith(t, gatewayJSON, routes, Options{})
}

// startAdminWith is startAdmin with the given options; an empty gatewayJSON
// creates no gateway.json.
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
		t.Fatalf("the body is not JSON: %v: %s", err, r.body)
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

// call makes a request to the API; an empty contentType uses JSON when there
// is a body.
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

// snapshotDir reads every file in the routes directory.
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
	return "# route " + name + "\nschemaVersion: 1\nname: " + name + "\nupstream: " + upstream + "\nmatch:\n  path: " + path + "\n"
}

// adminRoutes are three routes with a comment, so that we can check a write
// to one of them does not touch the others.
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

// Requirement: Admin API — routes

func TestAdminRouteCRUD(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, adminRoutes(t))

	var list struct{ Items []routeRes }
	if r := e.call(t, "GET", "/routes", "", ""); r.status != 200 {
		t.Fatalf("listing: %d %s", r.status, r.body)
	} else {
		r.decode(t, &list)
	}
	if len(list.Items) != 3 || !list.Items[0].HasComments {
		t.Fatalf("the listing should return all three routes, with hasComments: %+v", list.Items)
	}

	// Creation.
	r := e.call(t, "POST", "/routes", "", `{"name":"shipping","upstream":"`+up.URL+`","match":{"path":"/shipping/*"},"timeout":"3s"}`)
	if r.status != http.StatusCreated || r.header.Get("Location") != "/api/routes/shipping" || r.header.Get("ETag") == "" {
		t.Fatalf("creation: %d %v %s", r.status, r.header, r.body)
	}
	var created routeRes
	r.decode(t, &created)
	if created.Route.SchemaVersion != config.SchemaVersion || created.HasComments || created.Route.Timeout == nil {
		t.Fatalf("created resource: %+v", created)
	}
	// Scenario: Creating a route produces a document of its own.
	doc := filepath.Join(e.routes, "shipping.yaml")
	if data, err := os.ReadFile(doc); err != nil || !strings.Contains(string(data), "name: shipping") {
		t.Fatalf("the creation should produce routes/shipping.yaml: %v %s", err, data)
	}
	if st, body := getBody(t, e.traffic+"/shipping/x"); st != 200 || body != "upstream:/shipping/x" {
		t.Fatalf("the created route should serve without a restart: %d %q", st, body)
	}

	// Read.
	var got routeRes
	r = e.call(t, "GET", "/routes/shipping", "", "")
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || got.Route.Upstream != up.URL || got.File != doc || r.header.Get("ETag") == "" {
		t.Fatalf("read: %d %+v", r.status, got)
	}

	// Replacement.
	r = e.call(t, "PUT", "/routes/shipping", "", `{"schemaVersion":1,"name":"shipping","upstream":"`+up.URL+`","match":{"path":"/shipping/*"},"timeout":"5s"}`)
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || got.Route.Timeout == nil || time.Duration(*got.Route.Timeout) != 5*time.Second {
		t.Fatalf("replacement: %d %s", r.status, r.body)
	}

	// Change by merge patch: null removes the field.
	r = e.call(t, "PATCH", "/routes/shipping", "application/merge-patch+json", `{"stripPrefix":true,"timeout":null}`)
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || got.Route.Timeout != nil || !got.Route.StripPrefix {
		t.Fatalf("merge patch: %d %s", r.status, r.body)
	}
	if st, body := getBody(t, e.traffic+"/shipping/x"); st != 200 || body != "upstream:/x" {
		t.Fatalf("the change should take effect on the traffic: %d %q", st, body)
	}
	if data, _ := os.ReadFile(doc); strings.Contains(string(data), "timeout") {
		t.Fatalf("the removed field should not be in the document: %s", data)
	}
	if r := e.call(t, "PATCH", "/routes/shipping", "application/merge-patch+json", `{"overrides":[]}`); r.status != 422 || r.err(t).Field != "overrides" {
		t.Fatalf("overrides in a route patch should be refused: %d %s", r.status, r.body)
	}

	// Conflicts.
	if r := e.call(t, "POST", "/routes", "", `{"name":"shipping","match":{"path":"/other/*"},"upstream":"`+up.URL+`"}`); r.status != 409 || r.err(t).Error != "conflict" {
		t.Fatalf("repeated name: %d %s", r.status, r.body)
	}
	r = e.call(t, "POST", "/routes", "", `{"name":"clone","match":{"path":"/shipping/*"},"upstream":"`+up.URL+`"}`)
	if ae := r.err(t); r.status != 409 || ae.Error != "conflict" || !strings.Contains(ae.Message, "shipping.yaml") {
		t.Fatalf("an identical match should name the conflicting file: %d %s", r.status, r.body)
	}
	if _, err := os.Stat(filepath.Join(e.routes, "clone.yaml")); err == nil {
		t.Fatal("a refused creation should produce no document")
	}
	r = e.call(t, "POST", "/routes", "", `{"name":"bad","match":{"path":"/bad/*"},"upstream":"ftp://x"}`)
	if ae := r.err(t); r.status != 422 || ae.Error != "invalid" || ae.Field != "upstream" || !strings.Contains(ae.Message, "ftp://x") {
		t.Fatalf("invalid upstream: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes", "", `{"name":"x","match":{"path":"/x"},"upstrem":"http://a"}`); r.status != 422 || r.err(t).Field != "upstrem" {
		t.Fatalf("unknown field: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes", "", `{"name":`); r.status != 400 || r.err(t).Error != "bad_request" {
		t.Fatalf("malformed JSON: %d %s", r.status, r.body)
	}
	if r := e.call(t, "POST", "/routes", "text/csv", `a,b`); r.status != 415 {
		t.Fatalf("unexpected Content-Type: %d %s", r.status, r.body)
	}

	// Removal.
	if r := e.call(t, "DELETE", "/routes/shipping", "", ""); r.status != http.StatusNoContent {
		t.Fatalf("removal: %d %s", r.status, r.body)
	}
	if _, err := os.Stat(doc); err == nil {
		t.Fatal("the removal should delete the document")
	}
	if r := e.call(t, "GET", "/routes/shipping", "", ""); r.status != 404 || r.err(t).Error != "not_found" {
		t.Fatalf("removed route: %d %s", r.status, r.body)
	}
	if st, _ := getBody(t, e.traffic+"/shipping/x"); st != http.StatusNotFound {
		t.Fatalf("the removed route should no longer serve: %d", st)
	}
	if r := e.call(t, "DELETE", "/routes/shipping", "", ""); r.status != 404 {
		t.Fatalf("removing a route that does not exist: %d", r.status)
	}
	if r := e.call(t, "POST", "/routes/payments", "", `{}`); r.status != http.StatusMethodNotAllowed || !strings.Contains(r.header.Get("Allow"), "PATCH") {
		t.Fatalf("unsupported method: %d %v", r.status, r.header)
	}
}

func TestAdminRouteRenameKeepsFile(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	var got routeRes
	r := e.call(t, "PATCH", "/routes/orders", "application/merge-patch+json", `{"name":"purchases"}`)
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || got.Route.Name != "purchases" || got.File != filepath.Join(e.routes, "orders.yaml") {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	if e.call(t, "GET", "/routes/orders", "", "").status != 404 || e.call(t, "GET", "/routes/purchases", "", "").status != 200 {
		t.Fatal("the route should answer under the new name")
	}

	// The old name becomes free: since orders.yaml is still taken by
	// purchases, the recreated route gets the first free file, without
	// touching that one.
	purchases, _ := os.ReadFile(filepath.Join(e.routes, "orders.yaml"))
	up := e.Live.Load().Route("purchases").Upstream.String()
	r = e.call(t, "POST", "/routes", "", `{"name":"orders","upstream":"`+up+`","match":{"path":"/orders-v2/*"}}`)
	got = routeRes{}
	r.decode(t, &got)
	if r.status != http.StatusCreated || got.Route.Name != "orders" || got.File != filepath.Join(e.routes, "orders-2.yaml") {
		t.Fatalf("recreating the old name: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(filepath.Join(e.routes, "orders.yaml")); string(now) != string(purchases) {
		t.Fatalf("the purchases document should not be touched:\n%s", now)
	}
	// The same goes for creation through the raw document; a file that does
	// not belong to any route is not overwritten, and the next one is used.
	if r := e.call(t, "PATCH", "/routes/inventory", "application/merge-patch+json", `{"name":"stock"}`); r.status != 200 {
		t.Fatalf("rename: %d %s", r.status, r.body)
	}
	orphan := filepath.Join(e.routes, "inventory-2.yaml")
	writeFile(t, orphan, "# file from another tool\n")
	r = e.call(t, "PUT", "/routes/inventory/document", "application/yaml", routeDoc("inventory", up, "/inventory-v2/*"))
	got = routeRes{}
	r.decode(t, &got)
	if r.status != http.StatusCreated || got.File != filepath.Join(e.routes, "inventory-3.yaml") {
		t.Fatalf("recreating through the document: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(orphan); string(now) != "# file from another tool\n" {
		t.Fatal("a file that belongs to no route should not be overwritten")
	}
}

func TestAdminRouteDocument(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	path := filepath.Join(e.routes, "payments.yaml")
	disk, _ := os.ReadFile(path)

	r := e.call(t, "GET", "/routes/payments/document", "", "")
	etag := r.header.Get("ETag")
	if r.status != 200 || string(r.body) != string(disk) || !strings.HasPrefix(r.header.Get("Content-Type"), "application/yaml") || etag == "" {
		t.Fatalf("raw document: %d %q %v", r.status, r.body, r.header)
	}

	// The text is written exactly as sent, comments included.
	text := strings.Replace(string(disk), "# route payments", "# payments, hand-edited", 1) + "stripPrefix: true\n"
	r = e.call(t, "PUT", "/routes/payments/document", "application/yaml", text, "If-Match", etag)
	var got routeRes
	got = routeRes{}
	r.decode(t, &got)
	if r.status != 200 || !got.Route.StripPrefix || !got.HasComments || r.header.Get("ETag") == etag {
		t.Fatalf("writing the document: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(path); string(now) != text {
		t.Fatalf("the document should be written exactly as sent:\n%s", now)
	}
	if st, body := getBody(t, e.traffic+"/payments/x"); st != 200 || body != "upstream:/x" {
		t.Fatalf("the written document should take effect on the traffic: %d %q", st, body)
	}

	// Stale If-Match.
	if r := e.call(t, "PUT", "/routes/payments/document", "application/yaml", text+"rewriteHost: true\n", "If-Match", etag); r.status != 412 || r.err(t).Error != "stale" {
		t.Fatalf("stale If-Match: %d %s", r.status, r.body)
	}
	// A name that does not match the path.
	r = e.call(t, "PUT", "/routes/payments/document", "application/yaml", strings.Replace(text, "name: payments", "name: other", 1))
	if ae := r.err(t); r.status != 422 || ae.Field != "name" || ae.Line != 3 {
		t.Fatalf("mismatched name: %d %s", r.status, r.body)
	}
	// A validation error located in the text that was sent.
	bad := text + "overrides:\n  - name: flaky\n    match:\n      path: /payments/x\n    respond:\n      status: 503\n    probability: 1.5\n"
	r = e.call(t, "PUT", "/routes/payments/document", "application/yaml", bad)
	if ae := r.err(t); r.status != 422 || ae.Field != "overrides[0].probability" || ae.Line == 0 || ae.Column == 0 || ae.File != path {
		t.Fatalf("located validation: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(path); string(now) != text {
		t.Fatal("the invalid document should not be written")
	}
	if r := e.call(t, "PUT", "/routes/payments/document", "application/json", text); r.status != 415 {
		t.Fatalf("unexpected Content-Type: %d", r.status)
	}

	// A route that does not exist is created.
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	r = e.call(t, "PUT", "/routes/new/document", "application/yaml", routeDoc("new", up.URL, "/new/*"))
	if r.status != 201 {
		t.Fatalf("creation through the document: %d %s", r.status, r.body)
	}
	if now, _ := os.ReadFile(filepath.Join(e.routes, "new.yaml")); string(now) != routeDoc("new", up.URL, "/new/*") {
		t.Fatalf("the created document should be the one that was sent: %s", now)
	}
	if e.call(t, "GET", "/routes/none/document", "", "").status != 404 {
		t.Fatal("the document of a route that does not exist should answer 404")
	}
}

// Requirement: Admin API — overrides

func TestAdminOverrideCRUD(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	base := "/routes/payments/overrides"

	r := e.call(t, "POST", base, "", `{"name":"flaky","match":{"path":"/payments/charge","method":"POST"},"respond":{"status":503,"body":{"error":"unavailable"}},"ttl":"10m"}`)
	if r.status != http.StatusCreated || r.header.Get("Location") != "/api/routes/payments/overrides/flaky" {
		t.Fatalf("creating the override: %d %s", r.status, r.body)
	}
	var o overrideRes
	o = overrideRes{}
	r.decode(t, &o)
	if o.Route != "payments" || o.Override.Name != "flaky" || o.State == nil || !o.State.Active {
		t.Fatalf("override resource: %s", r.body)
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
	if st, body := post(); st != 503 || !strings.Contains(body, "unavailable") {
		t.Fatalf("the created override should intercept: %d %q", st, body)
	}

	if r := e.call(t, "POST", base, "", `{"name":"flaky","match":{"path":"/x"},"drop":true}`); r.status != 409 || r.err(t).Error != "conflict" {
		t.Fatalf("repeated name: %d %s", r.status, r.body)
	}

	// Read and list.
	r = e.call(t, "GET", base+"/flaky", "", "")
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.State.Applications != 1 || r.header.Get("ETag") == "" {
		t.Fatalf("reading the override: %d %s", r.status, r.body)
	}
	var list struct{ Items []overrideRes }
	e.call(t, "GET", base, "", "").decode(t, &list)
	if len(list.Items) != 1 || list.Items[0].Override.Name != "flaky" {
		t.Fatalf("listing the overrides: %+v", list)
	}
	var route routeRes
	e.call(t, "GET", "/routes/payments", "", "").decode(t, &route)
	if len(route.Route.Overrides) != 1 || route.State["flaky"] == nil {
		t.Fatalf("the route resource should carry the override and its state: %+v", route)
	}

	// Turning it on and off preserves the other fields.
	r = e.call(t, "PATCH", base+"/flaky", "application/merge-patch+json", `{"enabled":false}`)
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.Override.Enabled() || o.Override.Respond == nil || o.Override.Respond.Status != 503 || o.Override.TTL == nil {
		t.Fatalf("turning it off: %d %s", r.status, r.body)
	}
	if st, body := post(); st != 200 || body != "upstream:/payments/charge" {
		t.Fatalf("while off, the override should not intercept: %d %q", st, body)
	}
	r = e.call(t, "PATCH", base+"/flaky", "application/merge-patch+json", `{"enabled":true,"probability":1}`)
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || !o.Override.Enabled() || o.Override.Probability == nil {
		t.Fatalf("turning it back on: %d %s", r.status, r.body)
	}
	if st, _ := post(); st != 503 {
		t.Fatalf("back on, the override should intercept: %d", st)
	}

	// Replacement in the same position, with a rename.
	r = e.call(t, "PUT", base+"/flaky", "", `{"name":"slow","match":{"path":"/payments/charge"},"respond":{"status":500}}`)
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.Override.Name != "slow" || o.Override.Respond.Status != 500 {
		t.Fatalf("replacement: %d %s", r.status, r.body)
	}
	if e.call(t, "GET", base+"/flaky", "", "").status != 404 {
		t.Fatal("the old name should no longer exist")
	}

	// Live state and reset.
	post()
	var states struct {
		Now   time.Time
		Items []struct {
			Route, Override string
			Applications    int64
		}
	}
	e.call(t, "GET", "/overrides/state", "", "").decode(t, &states)
	if len(states.Items) != 1 || states.Items[0].Override != "slow" || states.Items[0].Applications != 1 || states.Now.IsZero() {
		t.Fatalf("live state: %+v", states)
	}
	r = e.call(t, "POST", base+"/slow/reset", "", "")
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.State.Applications != 0 {
		t.Fatalf("reset: %d %s", r.status, r.body)
	}

	// Removal.
	if r := e.call(t, "DELETE", base+"/slow", "", ""); r.status != http.StatusNoContent {
		t.Fatalf("removing the override: %d %s", r.status, r.body)
	}
	if st, _ := post(); st != 200 {
		t.Fatalf("once removed, the override should not intercept: %d", st)
	}
	for _, p := range []string{base + "/slow", "/routes/none/overrides", base + "/slow/reset"} {
		method := "GET"
		if strings.HasSuffix(p, "reset") {
			method = "POST"
		}
		if r := e.call(t, method, p, "", ""); r.status != 404 || r.err(t).Error != "not_found" {
			t.Fatalf("%s does not exist: %d %s", p, r.status, r.body)
		}
	}
	if r := e.call(t, "PATCH", base+"/slow", "application/merge-patch+json", `{"enabled":false}`); r.status != 404 {
		t.Fatalf("patching an override that does not exist: %d %s", r.status, r.body)
	}
}

// Requirement: Frequency of each effect

// The override's JSON representation carries the frequency of each effect,
// the long form of drop and latency included, and a merge patch reaches them
// one at a time.
func TestAdminEffectFrequencies(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	base := "/routes/payments/overrides"
	r := e.call(t, "POST", base, "", `{"name":"flaky","match":{"path":"/payments/charge"},`+
		`"respond":{"status":503,"chance":0.3},"latency":{"fixed":"2s","chance":0.5},"drop":{"chance":0.05}}`)
	if r.status != http.StatusCreated {
		t.Fatalf("creating the override: %d %s", r.status, r.body)
	}
	var o overrideRes
	r.decode(t, &o)
	if o.Override.RespondChance() != 0.3 || o.Override.LatencyChance() != 0.5 || o.Override.DropChance() != 0.05 {
		t.Fatalf("the resource should carry each effect's frequency: %s", r.body)
	}
	for _, want := range []string{`"chance": 0.3`, `"chance": 0.5`, `"chance": 0.05`} {
		if !strings.Contains(string(r.body), want) {
			t.Fatalf("the JSON should carry %s: %s", want, r.body)
		}
	}
	doc, err := os.ReadFile(filepath.Join(e.routes, "payments.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "chance: 0.05") || strings.Contains(string(doc), "probability") {
		t.Fatalf("the document should carry the frequencies and no legacy probability:\n%s", doc)
	}

	// One frequency at a time, and the short form of drop turns it off.
	r = e.call(t, "PATCH", base+"/flaky", "application/merge-patch+json", `{"respond":{"chance":1},"drop":false}`)
	o = overrideRes{}
	r.decode(t, &o)
	if r.status != 200 || o.Override.RespondChance() != 1 || o.Override.Drop.Declared() || o.Override.LatencyChance() != 0.5 {
		t.Fatalf("patching the frequencies: %d %s", r.status, r.body)
	}

	// Out of range is refused, naming the effect that holds it.
	r = e.call(t, "PATCH", base+"/flaky", "application/merge-patch+json", `{"latency":{"chance":1.5}}`)
	if ae := r.err(t); r.status != 422 || ae.Field != "overrides[0].latency.chance" {
		t.Fatalf("a frequency out of range should name the field: %d %s", r.status, r.body)
	}
}

// Scenario: A change touches only the route's document

func TestAdminWriteTouchesOnlyRouteDocument(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	before := snapshotDir(t, e.routes)
	r := e.call(t, "POST", "/routes/payments/overrides", "", `{"name":"flaky","match":{"path":"/payments/x"},"respond":{"status":503}}`)
	if r.status != 201 {
		t.Fatalf("creating the override: %d %s", r.status, r.body)
	}
	after := snapshotDir(t, e.routes)
	if len(after) != len(before) {
		t.Fatalf("no file should appear or disappear: %v", slices.Sorted(maps.Keys(after)))
	}
	for name, content := range before {
		if name == "payments.yaml" {
			if after[name] == content {
				t.Fatal("payments.yaml should have been rewritten")
			}
			continue
		}
		if after[name] != content {
			t.Fatalf("%s should stay byte for byte the same:\nbefore:\n%s\nafter:\n%s", name, content, after[name])
		}
	}
}

// Scenario: Concurrent writes are serialized

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
			t.Fatalf("%s should hold the %d overrides that were written, it holds %d", route, perRoute, len(doc.Overrides))
		}
		if got := len(e.Live.Load().Route(route).Overrides); got != perRoute {
			t.Fatalf("the snapshot of %s should hold the %d overrides, it holds %d", route, perRoute, got)
		}
	}
}

// Scenario: An invalid change is refused without touching the disk

func TestAdminInvalidChangeLeavesDiskUntouched(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	before := snapshotDir(t, e.routes)
	mtimes := map[string]time.Time{}
	for name := range before {
		st, _ := os.Stat(filepath.Join(e.routes, name))
		mtimes[name] = st.ModTime()
	}
	snap := e.Live.Load()

	r := e.call(t, "POST", "/routes/payments/overrides", "", `{"name":"slow","match":{"path":"/payments/x"},"latency":{"min":"2s","max":"1s"}}`)
	ae := r.err(t)
	if r.status != 422 || ae.Error != "invalid" || ae.Field != "overrides[0].latency.min" || !strings.Contains(ae.Message, "greater than the maximum") ||
		!strings.HasSuffix(ae.File, "payments.yaml") {
		t.Fatalf("minimum latency greater than the maximum: %d %s", r.status, r.body)
	}
	r = e.call(t, "PATCH", "/routes/orders", "application/merge-patch+json", `{"upstream":"not a url"}`)
	if r.status != 422 || r.err(t).Field != "upstream" {
		t.Fatalf("invalid upstream: %d %s", r.status, r.body)
	}
	after := snapshotDir(t, e.routes)
	if len(after) != len(before) {
		t.Fatalf("no file should appear, not even a temporary one: %d -> %d", len(before), len(after))
	}
	for name, content := range before {
		st, _ := os.Stat(filepath.Join(e.routes, name))
		if after[name] != content || !st.ModTime().Equal(mtimes[name]) {
			t.Fatalf("%s should not be touched", name)
		}
	}
	if e.Live.Load() != snap {
		t.Fatal("the configuration in effect should not change")
	}
}

// Requirement: Reloading without a restart

func TestReloadAppliesNewRoute(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, adminRoutes(t))
	if st, _ := getBody(t, e.traffic+"/new/x"); st != 404 {
		t.Fatalf("before the reload the route does not exist: %d", st)
	}
	writeFile(t, filepath.Join(e.routes, "new.yaml"), routeDoc("new", up.URL, "/new/*"))
	r := e.call(t, "POST", "/reload", "", "")
	var res struct {
		Routes   int
		Changed  struct{ Routes, Settings []string }
		Warnings []string
	}
	r.decode(t, &res)
	if r.status != 200 || res.Routes != 4 || !slices.Equal(res.Changed.Routes, []string{"new"}) || len(res.Changed.Settings) != 0 || res.Warnings == nil {
		t.Fatalf("reload: %d %s", r.status, r.body)
	}
	if st, body := getBody(t, e.traffic+"/new/x"); st != 200 || body != "upstream:/new/x" {
		t.Fatalf("the new route should serve without a restart: %d %q", st, body)
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
		t.Fatalf("invalid reload: %d %s", r.status, r.body)
	}
	if e.Live.Load() != snap {
		t.Fatal("the previous configuration should stay in effect")
	}
	if st, body := getBody(t, e.traffic+"/payments/x"); st != 200 || body != "upstream:/payments/x" {
		t.Fatalf("the previous routes should keep serving: %d %q", st, body)
	}
}

func TestReloadCollisionIsConflict(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, adminRoutes(t))
	writeFile(t, filepath.Join(e.routes, "copy.yaml"), routeDoc("payments", up.URL, "/copy/*"))
	r := e.call(t, "POST", "/reload", "", "")
	if ae := r.err(t); r.status != 409 || ae.Error != "conflict" || !strings.Contains(ae.Message, "payments.yaml") || !strings.Contains(ae.Message, "copy.yaml") {
		t.Fatalf("collision on reload: %d %s", r.status, r.body)
	}
}

// Scenario: Requests in flight survive the reload

func TestReloadInFlightRequestsFinishUnderOldConfig(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	old := httpServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		io.WriteString(w, "old")
	})
	next := httpServer(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "new") })
	e := startAdmin(t, freePorts, map[string]string{"svc.yaml": routeDoc("svc", old, "/svc/*")})

	type result struct {
		status int
		body   string
		gw     string
	}
	done := make(chan result, 1)
	go func() {
		res, err := http.Get(e.traffic + "/svc/slow")
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
		t.Fatalf("reload: %d %s", r.status, r.body)
	}
	if st, body := getBody(t, e.traffic+"/svc/x"); st != 200 || body != "new" {
		t.Fatalf("new requests should use the new configuration: %d %q", st, body)
	}
	close(release)
	select {
	case res := <-done:
		if res.status != 200 || res.body != "old" || !strings.Contains(res.gw, "route=svc") {
			t.Fatalf("the request in flight should finish under the old configuration: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request in flight did not finish")
	}
}

func httpServer(t *testing.T, fn http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(fn)
	t.Cleanup(s.Close)
	return s.URL
}

// A reload follows the same rules as a change through the API: the new port
// is opened live.
func TestReloadAppliesPortChange(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	old := e.TrafficAddr()
	port := freePort(t)
	writeFile(t, filepath.Join(e.dir, "gateway.json"), `{"ports":{"traffic":`+fmt.Sprint(port)+`,"admin":0},"seed":7}`)
	r := e.call(t, "POST", "/reload", "", "")
	var res struct{ Changed struct{ Settings []string } }
	r.decode(t, &res)
	if r.status != 200 || !slices.Contains(res.Changed.Settings, "ports.traffic") {
		t.Fatalf("port swap on reload: %d %s", r.status, r.body)
	}
	if st, body := getBody(t, fmt.Sprintf("http://127.0.0.1:%d/payments/x", port)); st != 200 || body != "upstream:/payments/x" {
		t.Fatalf("the new port should serve: %d %q", st, body)
	}
	waitRefused(t, old)
	if s := e.Live.Load().Settings; s.TrafficPort != port || s.Seed == nil || *s.Seed != 7 {
		t.Fatalf("the reloaded configuration should take effect: %+v", s)
	}
}

func TestReloadUnavailablePortKeepsCurrent(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	busy := occupy(t)
	snap := e.Live.Load()
	writeFile(t, filepath.Join(e.dir, "gateway.json"), `{"ports":{"traffic":`+fmt.Sprint(busy)+`,"admin":0},"seed":7}`)
	r := e.call(t, "POST", "/reload", "", "")
	if ae := r.err(t); r.status != 409 || ae.Error != "port_unavailable" || ae.Field != "ports.traffic" || !strings.Contains(ae.Message, fmt.Sprint(busy)) {
		t.Fatalf("busy port on reload: %d %s", r.status, r.body)
	}
	if e.Live.Load() != snap || e.Live.Load().Settings.Seed != nil {
		t.Fatal("the previous configuration should stay in effect, seed included")
	}
	if st, _ := getBody(t, e.traffic+"/payments/x"); st != 200 {
		t.Fatalf("the current port should keep serving: %d", st)
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
		t.Fatalf("reloading the process settings: %d %s", r.status, r.body)
	}
	if s := e.Live.Load().Settings; s.Seed == nil || *s.Seed != 42 || e.History.Backend() != config.BackendSQLite {
		t.Fatalf("seed and backend should take effect without a restart: %+v, backend %s", s, e.History.Backend())
	}
	// Exposure turned off by the reload answers "disabled", not an empty
	// list.
	if r := e.call(t, "GET", "/exchanges", "", ""); r.status != 403 || r.err(t).Error != "history_disabled" {
		t.Fatalf("exposure turned off by the reload: %d %s", r.status, r.body)
	}
}

func TestReloadUnavailableBackendKeepsCurrent(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	blocker := filepath.Join(e.dir, "plain-file")
	writeFile(t, blocker, "x")
	writeFile(t, filepath.Join(e.dir, "gateway.json"),
		`{"ports":{"traffic":0,"admin":0},"seed":1,"history":{"backend":"sqlite","path":"plain-file/history.db"}}`)
	snap := e.Live.Load()
	r := e.call(t, "POST", "/reload", "", "")
	if ae := r.err(t); r.status != 409 || ae.Error != "backend_unavailable" || !strings.Contains(ae.Message, "sqlite") || !strings.Contains(ae.Message, "memory") {
		t.Fatalf("unavailable backend on reload: %d %s", r.status, r.body)
	}
	if e.History.Backend() != config.BackendMemory || e.Live.Load() != snap {
		t.Fatal("the current backend and configuration should stay in use")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Scenario: The effective origin can be queried

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
		t.Fatalf("effective configuration: %d %s", r.status, r.body)
	}
	byKey := map[string]int{}
	for i, x := range v.Values {
		byKey[x.Key] = i
	}
	traffic, seed, backend := v.Values[byKey["ports.traffic"]], v.Values[byKey["seed"]], v.Values[byKey["history.backend"]]
	if traffic.Source != (config.Source{Origin: config.OriginEnv, Name: "GATEWAY_TRAFFIC_PORT"}) || !traffic.Locked {
		t.Fatalf("the port should come from the environment, locked: %+v", traffic)
	}
	if seed.Source != (config.Source{Origin: config.OriginFile, Name: v.File.Path}) || seed.Locked || seed.Value != float64(42) {
		t.Fatalf("the seed should come from the file: %+v", seed)
	}
	if backend.Source.Origin != config.OriginDefault || backend.Value != config.BackendMemory || backend.Env != "GATEWAY_HISTORY_BACKEND" {
		t.Fatalf("the backend should come from the default: %+v", backend)
	}
	if len(v.Values) != len(e.Live.Load().Settings.Effective()) {
		t.Fatalf("every value should show up: %d", len(v.Values))
	}
}

// Requirement: Admin API — every write accepts If-Match: the current version
// writes and returns the new ETag; an older version answers 412 stale without
// touching the document.

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
			t.Fatalf("reading the route: %d %v", r.status, r.header)
		}
		return r.header.Get("ETag")
	}
	writes := []struct {
		name, method, path, contentType, body string
		want                                  int
	}{
		{"route PUT", "PUT", route, "", `{"schemaVersion":1,"name":"payments","upstream":"` + upstream + `","match":{"path":"/payments/*"},"timeout":"5s"}`, 200},
		{"route PATCH", "PATCH", route, "application/merge-patch+json", `{"stripPrefix":true}`, 200},
		{"override POST", "POST", base, "", `{"name":"flaky","match":{"path":"/payments/x"},"respond":{"status":503}}`, 201},
		{"override PUT", "PUT", base + "/flaky", "", `{"name":"flaky","match":{"path":"/payments/x"},"respond":{"status":500}}`, 200},
		{"override PATCH", "PATCH", base + "/flaky", "application/merge-patch+json", `{"enabled":false}`, 200},
		{"override DELETE", "DELETE", base + "/flaky", "", "", 204},
		{"route DELETE", "DELETE", route, "", "", 204},
	}
	for _, w := range writes {
		cur := etag()
		// A version that is not the current one is refused without writing.
		stale := `"` + strings.Repeat("0", 16) + `"`
		before := stateOf(t, path)
		r := e.call(t, w.method, w.path, w.contentType, w.body, "If-Match", stale)
		if r.status != http.StatusPreconditionFailed || r.err(t).Error != "stale" {
			t.Fatalf("%s with a stale If-Match: %d %s", w.name, r.status, r.body)
		}
		if stateOf(t, path) != before {
			t.Fatalf("a refused %s should not touch the document", w.name)
		}
		// The current version writes.
		r = e.call(t, w.method, w.path, w.contentType, w.body, "If-Match", cur)
		if r.status != w.want {
			t.Fatalf("%s with the current If-Match: %d %s", w.name, r.status, r.body)
		}
		if w.want == 204 {
			continue
		}
		if next := r.header.Get("ETag"); next == "" || next == cur {
			t.Fatalf("%s should return the new ETag: before %q, after %q", w.name, cur, next)
		}
		// The ETag from before the write is now stale.
		before = stateOf(t, path)
		if r := e.call(t, w.method, w.path, w.contentType, w.body, "If-Match", cur); r.status != http.StatusPreconditionFailed {
			t.Fatalf("%s with the ETag from before the write: %d %s", w.name, r.status, r.body)
		}
		if stateOf(t, path) != before {
			t.Fatalf("a refused %s should not touch the document", w.name)
		}
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the removal with the current If-Match should delete the document")
	}
}
