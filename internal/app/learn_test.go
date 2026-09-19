package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
)

// Requirement: Endpoint learning

// learningUpstream answers per path with a JSON body of its own, a custom
// header and the headers that learning throws away.
func learningUpstream(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream", "real")
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"path":"`+r.URL.Path+`","n":1}`)
	}))
	t.Cleanup(s.Close)
	return s
}

type learnEnv struct {
	*App
	dir  string
	doc  string // path of the api route's document
	base string // URL of the traffic port
}

// startLearning brings the process up with the api route pointing at upstream
// and the given gateway.json; extra adds more route documents.
func startLearning(t *testing.T, gatewayJSON, upstream string, extra map[string]string) *learnEnv {
	t.Helper()
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "gateway.json"), gatewayJSON)
	doc := filepath.Join(dir, "routes", "api.yaml")
	writeFile(t, doc, "# API route\nschemaVersion: 1\nname: api\nupstream: "+upstream+"\nmatch:\n  path: /api/*\n")
	for name, content := range extra {
		writeFile(t, filepath.Join(dir, "routes", name), content)
	}
	a, err := Start(Options{Loader: loaderFor(filepath.Join(dir, "gateway.json")), Web: testWeb, Log: quietLog()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	return &learnEnv{App: a, dir: dir, doc: doc, base: "http://" + a.TrafficAddr()}
}

const learningOn = `{"ports":{"traffic":0,"admin":0},"learning":{"enabled":true}}`

// route reads the api route's document from disk.
func (e *learnEnv) route(t *testing.T) config.Route {
	t.Helper()
	b, err := os.ReadFile(e.doc)
	if err != nil {
		t.Fatal(err)
	}
	r, err := config.ParseRoute(e.doc, b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// settle waits for the recording and the learning of the requests already
// answered.
func (e *learnEnv) settle(t *testing.T) {
	t.Helper()
	if err := e.Recorder.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Learner.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// learned waits until the document holds n overrides and returns them.
func (e *learnEnv) learned(t *testing.T, n int) []config.Override {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.settle(t)
		ovs := e.route(t).Overrides
		if len(ovs) >= n || time.Now().After(deadline) {
			if len(ovs) != n {
				t.Fatalf("want %d overrides in the document, got %d: %+v", n, len(ovs), ovs)
			}
			return ovs
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (e *learnEnv) get(t *testing.T, path string) (int, string) {
	t.Helper()
	return getBody(t, e.base+path)
}

func TestNewEndpointsAreLearned(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	env.get(t, "/api/test")
	env.learned(t, 1)
	env.get(t, "/api/test2")
	ovs := env.learned(t, 2)

	for i, path := range []string{"/api/test", "/api/test2"} {
		o := ovs[i]
		if o.Enabled() || o.Match.Path != path || o.Match.Method != http.MethodGet || o.Match.PathRegex != "" {
			t.Fatalf("the override learned for %s should be off, with an exact path and method: %+v", path, o)
		}
		r := o.Respond
		if r == nil || r.Status != http.StatusAccepted {
			t.Fatalf("the prefilled response should carry the real status: %+v", r)
		}
		if !slices.Equal(r.Headers["X-Upstream"], config.HeaderValues{"real"}) || !slices.Equal(r.Headers["Content-Type"], config.HeaderValues{"application/json"}) {
			t.Fatalf("the real headers should be prefilled: %v", r.Headers)
		}
		for _, k := range []string{"Date", "Content-Length", "X-Gateway"} {
			if _, ok := r.Headers[k]; ok {
				t.Fatalf("header %s should not be written: %v", k, r.Headers)
			}
		}
		body, ok := r.Body.(map[string]any)
		if !ok || body["path"] != path {
			t.Fatalf("the real JSON body should be written as a structure: %#v", r.Body)
		}
		s := o.Source
		if s == nil || s.Kind != config.SourceLearned || s.Exchange == "" || s.At.IsZero() || s.BodyIncomplete {
			t.Fatalf("the origin of the learned override is incomplete: %+v", s)
		}
		if ex, err := env.History.Get(t.Context(), s.Exchange); err != nil || ex.Path != path {
			t.Fatalf("the source exchange should be in the history: %v %+v", err, ex)
		}
	}
	if ovs[0].Name != "get-api-test" || ovs[1].Name != "get-api-test2" {
		t.Fatalf("unexpected derived names: %s, %s", ovs[0].Name, ovs[1].Name)
	}
	// The snapshot in effect was rebuilt with the learned overrides.
	rt := env.Live.Load().Route("api")
	if len(rt.Overrides) != 2 || rt.Override("get-api-test") == nil {
		t.Fatalf("the snapshot should hold the learned overrides: %d", len(rt.Overrides))
	}
	if st, ok := env.Overrides.State("api", "get-api-test"); !ok || st.Enabled || st.Active {
		t.Fatalf("the live state should show the override as off: %+v", st)
	}
}

func TestLearnedEndpointDoesNotIntercept(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	env.get(t, "/api/test")
	env.learned(t, 1)
	status, body := env.get(t, "/api/test")
	if status != http.StatusAccepted || hits.Load() != 2 || body != `{"path":"/api/test","n":1}` {
		t.Fatalf("the repeated request should go to the upstream: %d %q, %d arrivals", status, body, hits.Load())
	}
}

func TestKnownEndpointIsNotDuplicated(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	env.get(t, "/api/test")
	env.learned(t, 1)
	env.get(t, "/api/test")
	env.settle(t)
	if ovs := env.learned(t, 1); ovs[0].Match.Path != "/api/test" {
		t.Fatalf("the document should still hold a single override: %+v", ovs)
	}
	// Another method on the same path is another combination.
	req, _ := http.NewRequest(http.MethodPost, env.base+"/api/test", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if ovs := env.learned(t, 2); ovs[1].Match.Method != http.MethodPost || ovs[1].Name != "post-api-test" {
		t.Fatalf("the POST should be learned separately: %+v", ovs[1])
	}
}

// Two simultaneous requests to the same new endpoint produce a single
// override.
func TestConcurrentRequestsLearnOnce(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			res, err := http.Get(env.base + "/api/new")
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		})
	}
	wg.Wait()
	env.learned(t, 1)
	// The override learned for /api/new, with an exact path, does not make a
	// path below it known.
	env.get(t, "/api/new/sub")
	env.learned(t, 2)
}

func TestLearningDisabledWritesNothing(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, freePorts, up.URL, nil)
	before, _ := os.ReadFile(env.doc)
	for _, p := range []string{"/api/a", "/api/b", "/api/c"} {
		env.get(t, p)
	}
	env.settle(t)
	after, _ := os.ReadFile(env.doc)
	if string(after) != string(before) {
		t.Fatalf("with the mode off the document should not change:\n%s", after)
	}
	if err := env.Recorder.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := env.History.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || len(res.Items) != 3 {
		t.Fatalf("the exchanges should be in the history: %v %d", err, len(res.Items))
	}
}

func TestOnlyUpstreamResponsesAreLearned(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	closed := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		ln.Close()
		return "http://" + addr
	}()
	env := startLearning(t, learningOn, up.URL, map[string]string{
		"mock.yaml": "schemaVersion: 1\nname: mock\nupstream: " + up.URL + "\nmatch:\n  path: /mock/*\n" +
			"overrides:\n  - name: fixed\n    match:\n      path: /mock/*\n    respond:\n      body: synthesized\n",
		"down.yaml": "schemaVersion: 1\nname: down\nupstream: " + closed + "\nmatch:\n  path: /down/*\n",
	})
	mockDoc := filepath.Join(env.dir, "routes", "mock.yaml")
	downDoc := filepath.Join(env.dir, "routes", "down.yaml")
	mockBefore, _ := os.ReadFile(mockDoc)
	downBefore, _ := os.ReadFile(downDoc)
	apiBefore, _ := os.ReadFile(env.doc)

	if _, body := env.get(t, "/mock/x"); body != "synthesized" {
		t.Fatalf("the mock route should answer through the override: %q", body)
	}
	if status, _ := env.get(t, "/no-route"); status != http.StatusNotFound {
		t.Fatalf("want 404 with no route, got %d", status)
	}
	if status, _ := env.get(t, "/down/x"); status != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", status)
	}
	env.settle(t)
	for _, c := range []struct {
		path   string
		before []byte
	}{{mockDoc, mockBefore}, {downDoc, downBefore}, {env.doc, apiBefore}} {
		if after, _ := os.ReadFile(c.path); string(after) != string(c.before) {
			t.Fatalf("%s should not change:\n%s", c.path, after)
		}
	}
}

func TestTruncatedBodyIsFlagged(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, `{"ports":{"traffic":0,"admin":0},"learning":{"enabled":true},"capture":{"maxBodyBytes":8}}`, up.URL, nil)
	env.get(t, "/api/large")
	o := env.learned(t, 1)[0]
	if o.Source == nil || !o.Source.BodyIncomplete {
		t.Fatalf("the learned override should flag the incomplete body: %+v", o.Source)
	}
	if o.Respond.Body != `{"path":` {
		t.Fatalf("the truncated body should be written as text: %#v", o.Respond.Body)
	}
}

// Learning works with history recording off: the exchange is observed without
// reaching the history, and the learned override does not point at an
// exchange that does not exist — source.exchange is left out of the document.
func TestLearningWorksWithRecordingDisabled(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, `{"ports":{"traffic":0,"admin":0},"learning":{"enabled":true},"history":{"record":false}}`, up.URL, nil)
	env.get(t, "/api/test")
	o := env.learned(t, 1)[0]
	if o.Respond.Status != http.StatusAccepted || o.Source == nil || o.Source.Kind != config.SourceLearned || o.Source.At.IsZero() {
		t.Fatalf("the endpoint should be learned with the observed response: %+v", o)
	}
	if o.Source.Exchange != "" {
		t.Fatalf("with recording off the origin should not point at an exchange: %q", o.Source.Exchange)
	}
	if b, err := os.ReadFile(env.doc); err != nil || strings.Contains(string(b), "exchange:") {
		t.Fatalf("the document should not write source.exchange: %v\n%s", err, b)
	}
	if err := env.Recorder.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := env.History.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || len(res.Items) != 0 {
		t.Fatalf("with recording off nothing should reach the history: %v %d", err, len(res.Items))
	}
}

// An injected delay does not change the upstream response: the delayed
// endpoint is learned, and the wildcard override that delayed it does not
// make it known.
func TestDelayedUpstreamResponseIsLearned(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, map[string]string{
		"slow.yaml": "schemaVersion: 1\nname: slow\nupstream: " + up.URL + "\nmatch:\n  path: /slow/*\n" +
			"overrides:\n  - name: delay\n    match:\n      path: /slow/*\n      method: GET\n    latency: 1ms\n",
	})
	env.get(t, "/slow/x")
	doc := filepath.Join(env.dir, "routes", "slow.yaml")
	deadline := time.Now().Add(5 * time.Second)
	for {
		env.settle(t)
		b, _ := os.ReadFile(doc)
		r, err := config.ParseRoute(doc, b)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Overrides) == 2 {
			if o := r.Overrides[1]; o.Match.Path != "/slow/x" || o.Enabled() || o.Respond.Status != http.StatusAccepted {
				t.Fatalf("the delayed endpoint should be learned with the upstream response: %+v", o)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the delayed endpoint was not learned: %+v", r.Overrides)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIdentifierBecomesParam(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	env.get(t, "/api/zip/40415345/json")
	env.learned(t, 1)
	env.get(t, "/api/zip/01001000/json")
	env.settle(t)
	ovs := env.learned(t, 1)
	o := ovs[0]
	if o.Match.Path != "/api/zip/:id/json" || o.Match.Method != http.MethodGet || o.Enabled() {
		t.Fatalf("there should be a single override, off, with the generalized path: %+v", o)
	}
	if o.Name != "get-api-zip-id-json" {
		t.Fatalf("unexpected name derived from the generalized path: %s", o.Name)
	}
	// The prefilled response is the one from the first exchange, which taught
	// the endpoint.
	if body, ok := o.Respond.Body.(map[string]any); !ok || body["path"] != "/api/zip/40415345/json" {
		t.Fatalf("the response should be the one from the source exchange: %#v", o.Respond.Body)
	}
	// Being off, it intercepts none of the records.
	if status, body := env.get(t, "/api/zip/99999999/json"); status != http.StatusAccepted || body != `{"path":"/api/zip/99999999/json","n":1}` {
		t.Fatalf("a learned override that is off should not intercept: %d %q", status, body)
	}
}

func TestLiteralSegmentPreserved(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	env.get(t, "/api/users/me")
	env.learned(t, 1)
	env.get(t, "/api/users/42")
	ovs := env.learned(t, 2)
	if ovs[0].Match.Path != "/api/users/me" || ovs[1].Match.Path != "/api/users/:id" {
		t.Fatalf("want /api/users/me and /api/users/:id as distinct overrides: %s, %s", ovs[0].Match.Path, ovs[1].Match.Path)
	}
	if ovs[1].Name != "get-api-users-id" {
		t.Fatalf("unexpected name: %s", ovs[1].Name)
	}
	// The literal stays known through its own override; another record,
	// through the generalized one.
	env.get(t, "/api/users/me")
	env.get(t, "/api/users/7")
	env.settle(t)
	env.learned(t, 2)
}

// An override learned before generalization, with an exact path, is replaced
// by the generalized one that covers it, in the same spot in the document; one
// the user created for another record stays put.
func TestLearnedExactIsAbsorbed(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	exact := "  - name: get-cep-40415345-json\n    enabled: false\n    match:\n      path: /cep/40415345/json\n      method: GET\n" +
		"    respond:\n      status: 200\n      body: old\n    source:\n      kind: learned\n      at: 2026-09-18T00:00:00Z\n"
	user := "  - name: mine\n    enabled: false\n    match:\n      path: /cep/11111111/json\n      method: GET\n    respond:\n      status: 418\n"
	env := startLearning(t, learningOn, up.URL, map[string]string{
		"cep.yaml": "schemaVersion: 1\nname: cep\nupstream: " + up.URL + "\nmatch:\n  path: /cep/*\noverrides:\n" + exact + user,
	})
	cep := *env
	cep.doc = filepath.Join(env.dir, "routes", "cep.yaml")
	// The learned record itself stays known: nothing changes.
	cep.get(t, "/cep/40415345/json")
	cep.settle(t)
	cep.learned(t, 2)
	cep.get(t, "/cep/01001000/json")
	cep.settle(t)
	ovs := cep.learned(t, 2)
	if ovs[0].Match.Path != "/cep/:id/json" || ovs[0].Name != "get-cep-id-json" || ovs[0].Source.Kind != config.SourceLearned || ovs[0].Enabled() {
		t.Fatalf("the exact learned override should be replaced by the generalized one: %+v", ovs[0])
	}
	if ovs[1].Name != "mine" {
		t.Fatalf("the user's override should stay: %+v", ovs[1])
	}
	if rt := env.Live.Load().Route("cep"); rt.Override("get-cep-40415345-json") != nil || rt.Override("get-cep-id-json") == nil {
		t.Fatal("the snapshot should reflect the replacement")
	}
}

// Simultaneous requests to different records of the same endpoint produce a
// single generalized override.
func TestConcurrentIdentifiersLearnOnce(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			res, err := http.Get(env.base + "/api/orders/" + strconv.Itoa(1000+i))
			if err != nil {
				t.Error(err)
				return
			}
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		})
	}
	wg.Wait()
	env.settle(t)
	if ovs := env.learned(t, 1); ovs[0].Match.Path != "/api/orders/:id" {
		t.Fatalf("want a single generalized override: %+v", ovs[0].Match)
	}
}
