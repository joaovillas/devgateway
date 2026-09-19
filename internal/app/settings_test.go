package app

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
)

// Process configuration through the API and live port swaps.

type settingsRes struct {
	Settings struct {
		File struct {
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
		} `json:"file"`
		Values []struct {
			Key    string        `json:"key"`
			Value  any           `json:"value"`
			Source config.Source `json:"source"`
			Locked bool          `json:"locked"`
		} `json:"values"`
	} `json:"settings"`
	Applied []string `json:"applied"`
	Notes   []string `json:"notes"`
}

func (e *adminEnv) patchSettings(t *testing.T, patch string, header ...string) apiResponse {
	t.Helper()
	return e.call(t, "PATCH", "/settings", "application/merge-patch+json", patch, header...)
}

// gatewayFile reads and parses the process's gateway.json.
func (e *adminEnv) gatewayFile(t *testing.T) (config.GatewayFile, string) {
	t.Helper()
	path := filepath.Join(e.dir, "gateway.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := config.ParseGatewayFile(path, data)
	if err != nil {
		t.Fatalf("the gateway.json that was written is not valid: %v\n%s", err, data)
	}
	return g, string(data)
}

// fileState holds a file's content and modification time, so that we can
// check it was left untouched.
type fileState struct {
	data  string
	mtime time.Time
}

func stateOf(t *testing.T, path string) fileState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	return fileState{string(data), st.ModTime()}
}

// occupy takes a free port for the rest of the test and returns its number.
func occupy(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// waitRefused waits until the address stops accepting connections.
func waitRefused(t *testing.T, addr string) {
	t.Helper()
	_, port, _ := net.SplitHostPort(addr)
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 3*time.Second)
		if err != nil {
			return
		}
		c.Close()
		if time.Now().After(deadline) {
			t.Fatalf("port %s should have stopped accepting connections", port)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Scenario: Process configuration changed through the API

func TestSettingsSeedChangedByAPI(t *testing.T) {
	flaky := map[string]string{"flaky.yaml": "schemaVersion: 1\nname: flaky\nmatch:\n  path: /flaky/*\n" +
		"overrides:\n  - name: half\n    match:\n      path: /flaky/*\n    respond:\n      status: 503\n    probability: 0.5\n"}
	// No upstream: whatever the override does not intercept answers 501.
	pattern := func(traffic string) string {
		var b strings.Builder
		for i := range 30 {
			st, _ := getBody(t, fmt.Sprintf("%s/flaky/%d", traffic, i))
			b.WriteString(strconv.Itoa(st) + " ")
		}
		return b.String()
	}

	e := startAdmin(t, freePorts, flaky)
	r := e.patchSettings(t, `{"seed":42}`)
	var res settingsRes
	r.decode(t, &res)
	if r.status != 200 || !slices.Equal(res.Applied, []string{"seed"}) || r.header.Get("ETag") == "" {
		t.Fatalf("changing the seed: %d %s", r.status, r.body)
	}
	if s := e.Live.Load().Settings; s.Seed == nil || *s.Seed != 42 {
		t.Fatalf("the new seed should take effect without a restart: %v", s.Seed)
	}
	g, raw := e.gatewayFile(t)
	if g.Seed == nil || *g.Seed != 42 || g.Ports == nil || g.Ports.Traffic == nil || *g.Ports.Traffic != 0 {
		t.Fatalf("gateway.json should declare the seed and keep the ports:\n%s", raw)
	}
	for _, v := range res.Settings.Values {
		if v.Key == "seed" && (v.Source.Origin != config.OriginFile || v.Value != float64(42)) {
			t.Fatalf("the response should report the seed as coming from the file: %+v", v)
		}
	}
	got := pattern(e.traffic)

	// A process that starts up with the same seed decides the same way: the
	// seed changed through the API counts as if it had been read at load time.
	fresh := startAdmin(t, `{"ports":{"traffic":0,"admin":0},"seed":42}`, flaky)
	if want := pattern(fresh.traffic); got != want {
		t.Fatalf("the decisions should follow seed 42:\napi:  %s\nload: %s", got, want)
	}
	if !strings.Contains(got, "503") || !strings.Contains(got, "501") {
		t.Fatalf("a 50%% probability should mix the responses: %s", got)
	}

	// null drops the key from the file and the value goes back to the default.
	r = e.patchSettings(t, `{"seed":null}`)
	if g, raw := e.gatewayFile(t); r.status != 200 || g.Seed != nil || e.Live.Load().Settings.Seed != nil {
		t.Fatalf("removing the seed: %d %s\n%s", r.status, r.body, raw)
	}
}

// Scenario: A value from the environment is locked

func TestSettingsEnvValueIsLocked(t *testing.T) {
	t.Setenv("GATEWAY_TRAFFIC_PORT", "0")
	e := startAdmin(t, `{"ports":{"admin":0},"seed":1}`, nil)
	path := filepath.Join(e.dir, "gateway.json")
	before := stateOf(t, path)
	snap := e.Live.Load()

	for _, patch := range []string{`{"ports":{"traffic":9090}}`, `{"ports":null}`, `{"seed":5,"ports":{"traffic":9090}}`} {
		r := e.patchSettings(t, patch)
		ae := r.err(t)
		if r.status != 409 || ae.Error != "locked" || ae.Env != "GATEWAY_TRAFFIC_PORT" || ae.Field != "ports.traffic" ||
			!strings.Contains(ae.Message, "GATEWAY_TRAFFIC_PORT") {
			t.Fatalf("%s: %d %s", patch, r.status, r.body)
		}
	}
	// A raw document that changes the locked port is refused too.
	r := e.call(t, "PUT", "/settings/document", "application/json", `{"ports":{"traffic":9090,"admin":0},"seed":1}`)
	if ae := r.err(t); r.status != 409 || ae.Env != "GATEWAY_TRAFFIC_PORT" {
		t.Fatalf("document that changes the locked port: %d %s", r.status, r.body)
	}
	if after := stateOf(t, path); after != before {
		t.Fatalf("gateway.json should not be modified:\n%s", after.data)
	}
	if e.Live.Load() != snap {
		t.Fatal("the configuration in effect should not change")
	}

	// Keys that are not locked stay changeable, and a document that leaves the
	// locked key alone is accepted.
	if r := e.patchSettings(t, `{"seed":2}`); r.status != 200 {
		t.Fatalf("seed with the port locked: %d %s", r.status, r.body)
	}
	if r := e.call(t, "PUT", "/settings/document", "application/json", `{"ports":{"admin":0},"seed":3}`); r.status != 200 {
		t.Fatalf("document that keeps the locked port: %d %s", r.status, r.body)
	}
	if s := e.Live.Load().Settings; *s.Seed != 3 || s.Sources["ports.traffic"].Origin != config.OriginEnv {
		t.Fatalf("configuration after the document: %+v", s)
	}
}

func TestSettingsPatchRefusesInvalid(t *testing.T) {
	e := startAdmin(t, `{"ports":{"traffic":0,"admin":0}}`, nil)
	path := filepath.Join(e.dir, "gateway.json")
	before := stateOf(t, path)
	for _, c := range []struct {
		patch, code, field string
		status             int
	}{
		{`{"history":{"capacity":0}}`, "invalid", "history.capacity", 422},
		{`{"history":{"backend":"redis"}}`, "invalid", "history.backend", 422},
		{`{"ports":{"traffic":"x"}}`, "invalid", "ports.traffic", 422},
		{`{"unknown":1}`, "invalid", "unknown", 422},
		{`{"seed":`, "bad_request", "", 400},
		{`[1]`, "bad_request", "", 400},
	} {
		r := e.patchSettings(t, c.patch)
		ae := r.err(t)
		if r.status != c.status || ae.Error != c.code || ae.Field != c.field {
			t.Fatalf("%s: %d %s", c.patch, r.status, r.body)
		}
	}
	// A mismatched If-Match writes nothing.
	if r := e.patchSettings(t, `{"seed":1}`, "If-Match", `"other"`); r.status != 412 || r.err(t).Error != "stale" {
		t.Fatalf("mismatched If-Match: %d %s", r.status, r.body)
	}
	if after := stateOf(t, path); after != before {
		t.Fatalf("gateway.json should not be modified:\n%s", after.data)
	}
	// If-Match with the current version writes.
	etag := e.call(t, "GET", "/settings/document", "", "").header.Get("ETag")
	if r := e.patchSettings(t, `{"seed":1}`, "If-Match", etag); r.status != 200 {
		t.Fatalf("If-Match with the current version: %d %s", r.status, r.body)
	}
}

func TestSettingsDocument(t *testing.T) {
	t.Setenv("GATEWAY_TRAFFIC_PORT", "0")
	t.Setenv("GATEWAY_ADMIN_PORT", "0")
	e := startAdminWith(t, "", nil, Options{})
	path := filepath.Join(e.dir, "gateway.json")

	r := e.call(t, "GET", "/settings/document", "", "")
	if r.status != 200 || strings.TrimSpace(string(r.body)) != "{}" || r.header.Get("X-Gateway-File-Exists") != "false" {
		t.Fatalf("missing document: %d %v %s", r.status, r.header, r.body)
	}
	// The first change creates the file, declaring the schema version.
	if r := e.patchSettings(t, `{"seed":9}`); r.status != 200 {
		t.Fatalf("creation through PATCH: %d %s", r.status, r.body)
	}
	if g, raw := e.gatewayFile(t); g.SchemaVersion == nil || *g.SchemaVersion != config.SchemaVersion || *g.Seed != 9 {
		t.Fatalf("gateway.json as created:\n%s", raw)
	}

	// The raw document is written exactly as sent and applied live.
	doc := "{\n  \"schemaVersion\": 1,\n  \"seed\": 11,\n  \"history\": { \"record\": false }\n}\n"
	r = e.call(t, "PUT", "/settings/document", "application/json", doc)
	var res settingsRes
	r.decode(t, &res)
	if r.status != 200 || !slices.Contains(res.Applied, "seed") || !slices.Contains(res.Applied, "history.record") {
		t.Fatalf("writing the document: %d %s", r.status, r.body)
	}
	if data, _ := os.ReadFile(path); string(data) != doc {
		t.Fatalf("the document should be written exactly as sent:\n%s", data)
	}
	if s := e.Live.Load().Settings; *s.Seed != 11 || s.HistoryRecord {
		t.Fatalf("the document should take effect without a restart: %+v", s)
	}
	r = e.call(t, "GET", "/settings/document", "", "")
	if string(r.body) != doc || r.header.Get("X-Gateway-File-Exists") != "true" || r.header.Get("ETag") == "" {
		t.Fatalf("reading the document: %v %s", r.header, r.body)
	}

	before := stateOf(t, path)
	r = e.call(t, "PUT", "/settings/document", "application/json", "{\n  \"seed\": 1,\n}\n")
	if ae := r.err(t); r.status != 422 || ae.Line != 3 || ae.Column == 0 {
		t.Fatalf("invalid JSON: %d %s", r.status, r.body)
	}
	r = e.call(t, "PUT", "/settings/document", "application/json", `{"ports":{"traffic":1,"admin":1}}`)
	if r.status != 409 || r.err(t).Error != "locked" {
		t.Fatalf("ports coming from the environment: %d %s", r.status, r.body)
	}
	if r := e.call(t, "PUT", "/settings/document", "application/yaml", "seed: 1\n"); r.status != 415 {
		t.Fatalf("wrong content type: %d %s", r.status, r.body)
	}
	if after := stateOf(t, path); after != before {
		t.Fatal("gateway.json should not be modified by the refused writes")
	}
}

// Turning learning mode on and off through the API.

func TestLearningToggledByAPI(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{
		"api.yaml": "schemaVersion: 1\nname: api\nupstream: " + up.URL + "\nmatch:\n  path: /api/*\n",
	})
	type learning struct {
		Enabled bool           `json:"enabled"`
		Source  config.Source  `json:"source"`
		Locked  bool           `json:"locked"`
		Learned map[string]int `json:"learned"`
	}
	var l learning
	r := e.call(t, "GET", "/learning", "", "")
	r.decode(t, &l)
	if r.status != 200 || l.Enabled || l.Locked || l.Source.Origin != config.OriginDefault || l.Learned["api"] != 0 {
		t.Fatalf("initial learning state: %d %s", r.status, r.body)
	}

	r = e.call(t, "PUT", "/learning", "", `{"enabled":true}`)
	l = learning{}
	r.decode(t, &l)
	if r.status != 200 || !l.Enabled || l.Source.Origin != config.OriginFile {
		t.Fatalf("turning learning on: %d %s", r.status, r.body)
	}
	if g, raw := e.gatewayFile(t); g.Learning == nil || g.Learning.Enabled == nil || !*g.Learning.Enabled {
		t.Fatalf("gateway.json should declare learning as on:\n%s", raw)
	}
	getBody(t, e.traffic+"/api/new")
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.Recorder.Wait(t.Context())
		e.Learner.Sync(t.Context())
		l = learning{}
		e.call(t, "GET", "/learning", "", "").decode(t, &l)
		if l.Learned["api"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the new endpoint should be learned: %+v", l)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if r := e.call(t, "PUT", "/learning", "", `{"enabled":false}`); r.status != 200 {
		t.Fatalf("turning learning off: %d %s", r.status, r.body)
	}
	getBody(t, e.traffic+"/api/other")
	e.Recorder.Wait(t.Context())
	e.Learner.Sync(t.Context())
	l = learning{}
	e.call(t, "GET", "/learning", "", "").decode(t, &l)
	if l.Enabled || l.Learned["api"] != 1 {
		t.Fatalf("with learning off nothing should be learned: %+v", l)
	}
	if r := e.call(t, "PUT", "/learning", "", `{}`); r.status != 422 || r.err(t).Field != "enabled" {
		t.Fatalf("body without enabled: %d %s", r.status, r.body)
	}
}

func TestLearningLockedByEnv(t *testing.T) {
	t.Setenv("GATEWAY_LEARNING", "false")
	e := startAdmin(t, freePorts, nil)
	before := stateOf(t, filepath.Join(e.dir, "gateway.json"))
	r := e.call(t, "PUT", "/learning", "", `{"enabled":true}`)
	if ae := r.err(t); r.status != 409 || ae.Error != "locked" || ae.Env != "GATEWAY_LEARNING" {
		t.Fatalf("learning locked by the environment: %d %s", r.status, r.body)
	}
	if stateOf(t, filepath.Join(e.dir, "gateway.json")) != before || e.Live.Load().Settings.LearningEnabled {
		t.Fatal("nothing should change")
	}
	var l struct{ Locked bool }
	e.call(t, "GET", "/learning", "", "").decode(t, &l)
	if !l.Locked {
		t.Fatal("the read should report the lock")
	}
}

// Scenarios: History backend swapped live, a new backend that is unavailable
// keeps the current one — through the API.

func TestSettingsSwitchesHistoryBackend(t *testing.T) {
	e := startAdmin(t, freePorts, statusRoutes(t))
	getBody(t, e.traffic+"/payments/memory")
	e.Recorder.Sync(t.Context())
	old := e.History.Backend()

	dbPath := filepath.Join(e.dir, "data", "history.db")
	r := e.patchSettings(t, `{"history":{"backend":"sqlite","path":"data/history.db"}}`)
	var res settingsRes
	r.decode(t, &res)
	if r.status != 200 || !slices.Contains(res.Applied, "history.backend") || len(res.Notes) != 1 ||
		!strings.Contains(res.Notes[0], "not migrated") || !strings.Contains(res.Notes[0], old) {
		t.Fatalf("backend swap: %d %s", r.status, r.body)
	}
	if e.History.Backend() != config.BackendSQLite {
		t.Fatalf("the backend in use should be sqlite: %s", e.History.Backend())
	}
	getBody(t, e.traffic+"/payments/sqlite")
	var l listBody
	e.call(t, "GET", "/exchanges", "", "").decode(t, &l)
	if l.Backend != config.BackendSQLite || len(l.Items) != 1 || l.Items[0].Path != "/payments/sqlite" {
		t.Fatalf("later exchanges should go into sqlite, with no migration: %s %v", l.Backend, l.Items)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("the database should sit next to gateway.json: %v", err)
	}

	// A backend that does not initialize: refused, the current one stays in
	// use.
	writeFile(t, filepath.Join(e.dir, "plain-file"), "x")
	before := stateOf(t, filepath.Join(e.dir, "gateway.json"))
	r = e.patchSettings(t, `{"history":{"backend":"ndjson","path":"plain-file/history.ndjson"}}`)
	if ae := r.err(t); r.status != 409 || ae.Error != "backend_unavailable" || !strings.Contains(ae.Message, "ndjson") || !strings.Contains(ae.Message, "sqlite") {
		t.Fatalf("unavailable backend: %d %s", r.status, r.body)
	}
	if e.History.Backend() != config.BackendSQLite || stateOf(t, filepath.Join(e.dir, "gateway.json")) != before {
		t.Fatal("the current backend and gateway.json should be left as they were")
	}
	getBody(t, e.traffic+"/payments/after")
	e.Recorder.Sync(t.Context())
	if res, err := e.History.List(t.Context(), exchange.Filter{}, store.Page{}); err != nil || len(res.Items) != 2 {
		t.Fatalf("sqlite should keep recording: %d %v", len(res.Items), err)
	}
}

// Scenario: Port swapped live

func TestTrafficPortSwitchedHot(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	slow := httpServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		io.WriteString(w, "slow")
	})
	var hits atomic.Int64
	fast := countingUpstream(t, &hits)
	e := startAdmin(t, freePorts, map[string]string{
		"slow.yaml": routeDoc("slow", slow, "/slow/*"),
		"fast.yaml": routeDoc("fast", fast.URL, "/fast/*"),
	})
	oldAddr := e.TrafficAddr()

	done := make(chan result, 1)
	go func() { done <- fetch(e.traffic + "/slow/x") }()
	<-arrived

	port := freePort(t)
	r := e.patchSettings(t, fmt.Sprintf(`{"ports":{"traffic":%d}}`, port))
	var res settingsRes
	r.decode(t, &res)
	if r.status != 200 || !slices.Equal(res.Applied, []string{"ports.traffic"}) || len(res.Notes) != 1 || !strings.Contains(res.Notes[0], strconv.Itoa(port)) {
		t.Fatalf("traffic port swap: %d %s", r.status, r.body)
	}
	newBase := fmt.Sprintf("http://127.0.0.1:%d", port)
	if st, body := getBody(t, newBase+"/fast/y"); st != 200 || body != "upstream:/fast/y" {
		t.Fatalf("the new port should serve: %d %q", st, body)
	}
	if !strings.HasSuffix(e.TrafficAddr(), ":"+strconv.Itoa(port)) {
		t.Fatalf("the traffic address should be the new one: %s", e.TrafficAddr())
	}
	waitRefused(t, oldAddr)
	if g, raw := e.gatewayFile(t); g.Ports == nil || g.Ports.Traffic == nil || *g.Ports.Traffic != port {
		t.Fatalf("gateway.json should declare the new port:\n%s", raw)
	}

	// The request in flight on the old port finishes normally.
	close(release)
	select {
	case res := <-done:
		if res.err != nil || res.status != 200 || res.body != "slow" {
			t.Fatalf("the request in flight should finish: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the request in flight did not finish")
	}
}

// Scenario: A new port that is unavailable keeps the current one

func TestTrafficPortUnavailableKeepsCurrent(t *testing.T) {
	e := startAdmin(t, freePorts, adminRoutes(t))
	busy := occupy(t)
	path := filepath.Join(e.dir, "gateway.json")
	before := stateOf(t, path)
	snap := e.Live.Load()

	r := e.patchSettings(t, fmt.Sprintf(`{"ports":{"traffic":%d},"seed":3}`, busy))
	ae := r.err(t)
	if r.status != 409 || ae.Error != "port_unavailable" || ae.Field != "ports.traffic" ||
		!strings.Contains(ae.Message, strconv.Itoa(busy)) || !strings.Contains(ae.Message, "bind") {
		t.Fatalf("busy port: %d %s", r.status, r.body)
	}
	if !strings.Contains(ae.Message, "stays on") {
		t.Fatalf("the refusal should say which port traffic stays on: %s", ae.Message)
	}
	if st, body := getBody(t, e.traffic+"/payments/x"); st != 200 || body != "upstream:/payments/x" {
		t.Fatalf("the current port should keep serving: %d %q", st, body)
	}
	if stateOf(t, path) != before || e.Live.Load() != snap {
		t.Fatal("nothing should change: neither gateway.json nor the configuration in effect (seed included)")
	}
}

func TestAdminPortSwitchedHot(t *testing.T) {
	e := startAdminWith(t, freePorts, nil, Options{Heartbeat: time.Hour})
	oldAddr := e.AdminAddr()

	// An event stream open on the old port does not hold up the swap: it ends
	// when the port goes out of service, and the client reconnects.
	stream := openEvents(t, "http://"+oldAddr+"/api/events")
	stream.next(t, "hello")

	port := freePort(t)
	r := e.patchSettings(t, fmt.Sprintf(`{"ports":{"admin":%d}}`, port))
	var res settingsRes
	r.decode(t, &res)
	if r.status != 200 || !slices.Equal(res.Applied, []string{"ports.admin"}) {
		t.Fatalf("the response should go out over the old port: %d %s", r.status, r.body)
	}
	for _, v := range res.Settings.Values {
		if v.Key == "ports.admin" && v.Value != float64(port) {
			t.Fatalf("the response should report the new port: %+v", v)
		}
	}
	e.api = fmt.Sprintf("http://127.0.0.1:%d/api", port)
	if r := e.call(t, "GET", "/status", "", ""); r.status != 200 {
		t.Fatalf("the API should serve on the new port: %d", r.status)
	}
	waitRefused(t, oldAddr)
	stream.closed(t)
}
