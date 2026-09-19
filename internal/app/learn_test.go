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

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
)

// Requirement: Aprendizado de endpoints

// learningUpstream responde por path com um corpo JSON próprio, um cabeçalho
// personalizado e os cabeçalhos que o aprendizado descarta.
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
	doc  string // caminho do documento da rota api
	base string // URL da porta de tráfego
}

// startLearning sobe o processo com a rota api apontando para upstream e o
// gateway.json dado; extra acrescenta documentos de rota.
func startLearning(t *testing.T, gatewayJSON, upstream string, extra map[string]string) *learnEnv {
	t.Helper()
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "gateway.json"), gatewayJSON)
	doc := filepath.Join(dir, "routes", "api.yaml")
	writeFile(t, doc, "# rota da API\nschemaVersion: 1\nname: api\nupstream: "+upstream+"\nmatch:\n  path: /api/*\n")
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

// route lê do disco o documento da rota api.
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

// settle espera o registro e o aprendizado das requisições já respondidas.
func (e *learnEnv) settle(t *testing.T) {
	t.Helper()
	if err := e.Recorder.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := e.Learner.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// learned espera até que o documento tenha n overrides e os devolve.
func (e *learnEnv) learned(t *testing.T, n int) []config.Override {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		e.settle(t)
		ovs := e.route(t).Overrides
		if len(ovs) >= n || time.Now().After(deadline) {
			if len(ovs) != n {
				t.Fatalf("esperados %d overrides no documento, há %d: %+v", n, len(ovs), ovs)
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
	env.get(t, "/api/teste")
	env.learned(t, 1)
	env.get(t, "/api/teste2")
	ovs := env.learned(t, 2)

	for i, path := range []string{"/api/teste", "/api/teste2"} {
		o := ovs[i]
		if o.Enabled() || o.Match.Path != path || o.Match.Method != http.MethodGet || o.Match.PathRegex != "" {
			t.Fatalf("override aprendido para %s deveria estar desligado, com path exato e método: %+v", path, o)
		}
		r := o.Respond
		if r == nil || r.Status != http.StatusAccepted {
			t.Fatalf("a resposta pré-preenchida deveria ter o status real: %+v", r)
		}
		if !slices.Equal(r.Headers["X-Upstream"], config.HeaderValues{"real"}) || !slices.Equal(r.Headers["Content-Type"], config.HeaderValues{"application/json"}) {
			t.Fatalf("os cabeçalhos reais deveriam ser pré-preenchidos: %v", r.Headers)
		}
		for _, k := range []string{"Date", "Content-Length", "X-Gateway"} {
			if _, ok := r.Headers[k]; ok {
				t.Fatalf("o cabeçalho %s não deveria ser gravado: %v", k, r.Headers)
			}
		}
		body, ok := r.Body.(map[string]any)
		if !ok || body["path"] != path {
			t.Fatalf("o corpo JSON real deveria ser gravado como estrutura: %#v", r.Body)
		}
		s := o.Source
		if s == nil || s.Kind != config.SourceLearned || s.Exchange == "" || s.At.IsZero() || s.BodyIncomplete {
			t.Fatalf("a origem do override aprendido está incompleta: %+v", s)
		}
		if ex, err := env.History.Get(t.Context(), s.Exchange); err != nil || ex.Path != path {
			t.Fatalf("a troca de origem deveria estar no histórico: %v %+v", err, ex)
		}
	}
	if ovs[0].Name != "get-api-teste" || ovs[1].Name != "get-api-teste2" {
		t.Fatalf("nomes derivados inesperados: %s, %s", ovs[0].Name, ovs[1].Name)
	}
	// O snapshot em vigor foi reconstruído com os overrides aprendidos.
	rt := env.Live.Load().Route("api")
	if len(rt.Overrides) != 2 || rt.Override("get-api-teste") == nil {
		t.Fatalf("o snapshot deveria conter os overrides aprendidos: %d", len(rt.Overrides))
	}
	if st, ok := env.Overrides.State("api", "get-api-teste"); !ok || st.Enabled || st.Active {
		t.Fatalf("o estado vivo deveria mostrar o override desligado: %+v", st)
	}
}

func TestLearnedEndpointDoesNotIntercept(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	env.get(t, "/api/teste")
	env.learned(t, 1)
	status, body := env.get(t, "/api/teste")
	if status != http.StatusAccepted || hits.Load() != 2 || body != `{"path":"/api/teste","n":1}` {
		t.Fatalf("a requisição repetida deveria ir ao upstream: %d %q, %d chegadas", status, body, hits.Load())
	}
}

func TestKnownEndpointIsNotDuplicated(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	env.get(t, "/api/teste")
	env.learned(t, 1)
	env.get(t, "/api/teste")
	env.settle(t)
	if ovs := env.learned(t, 1); ovs[0].Match.Path != "/api/teste" {
		t.Fatalf("o documento deveria continuar com um único override: %+v", ovs)
	}
	// Outro método no mesmo path é outra combinação.
	req, _ := http.NewRequest(http.MethodPost, env.base+"/api/teste", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if ovs := env.learned(t, 2); ovs[1].Match.Method != http.MethodPost || ovs[1].Name != "post-api-teste" {
		t.Fatalf("o POST deveria ser aprendido à parte: %+v", ovs[1])
	}
}

// Duas requisições simultâneas ao mesmo endpoint novo geram um único
// override.
func TestConcurrentRequestsLearnOnce(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			res, err := http.Get(env.base + "/api/novo")
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
	// O override aprendido para /api/novo, de path exato, não torna conhecido
	// um path abaixo dele.
	env.get(t, "/api/novo/sub")
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
		t.Fatalf("com o modo desligado o documento não deveria mudar:\n%s", after)
	}
	if err := env.Recorder.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := env.History.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || len(res.Items) != 3 {
		t.Fatalf("as trocas deveriam constar do histórico: %v %d", err, len(res.Items))
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
			"overrides:\n  - name: fixo\n    match:\n      path: /mock/*\n    respond:\n      body: sintetizado\n",
		"fora.yaml": "schemaVersion: 1\nname: fora\nupstream: " + closed + "\nmatch:\n  path: /fora/*\n",
	})
	mockDoc := filepath.Join(env.dir, "routes", "mock.yaml")
	foraDoc := filepath.Join(env.dir, "routes", "fora.yaml")
	mockBefore, _ := os.ReadFile(mockDoc)
	foraBefore, _ := os.ReadFile(foraDoc)
	apiBefore, _ := os.ReadFile(env.doc)

	if _, body := env.get(t, "/mock/x"); body != "sintetizado" {
		t.Fatalf("a rota mock deveria responder pelo override: %q", body)
	}
	if status, _ := env.get(t, "/sem-rota"); status != http.StatusNotFound {
		t.Fatalf("esperado 404 sem rota, recebido %d", status)
	}
	if status, _ := env.get(t, "/fora/x"); status != http.StatusBadGateway {
		t.Fatalf("esperado 502, recebido %d", status)
	}
	env.settle(t)
	for _, c := range []struct {
		path   string
		before []byte
	}{{mockDoc, mockBefore}, {foraDoc, foraBefore}, {env.doc, apiBefore}} {
		if after, _ := os.ReadFile(c.path); string(after) != string(c.before) {
			t.Fatalf("%s não deveria mudar:\n%s", c.path, after)
		}
	}
}

func TestTruncatedBodyIsFlagged(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, `{"ports":{"traffic":0,"admin":0},"learning":{"enabled":true},"capture":{"maxBodyBytes":8}}`, up.URL, nil)
	env.get(t, "/api/grande")
	o := env.learned(t, 1)[0]
	if o.Source == nil || !o.Source.BodyIncomplete {
		t.Fatalf("o override aprendido deveria sinalizar o corpo incompleto: %+v", o.Source)
	}
	if o.Respond.Body != `{"path":` {
		t.Fatalf("o corpo truncado deveria ser gravado como texto: %#v", o.Respond.Body)
	}
}

// O aprendizado funciona com o registro do histórico desligado: a troca é
// observada sem ir para o histórico, e o override aprendido não aponta para
// uma troca que não existe — source.exchange fica ausente do documento.
func TestLearningWorksWithRecordingDisabled(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, `{"ports":{"traffic":0,"admin":0},"learning":{"enabled":true},"history":{"record":false}}`, up.URL, nil)
	env.get(t, "/api/teste")
	o := env.learned(t, 1)[0]
	if o.Respond.Status != http.StatusAccepted || o.Source == nil || o.Source.Kind != config.SourceLearned || o.Source.At.IsZero() {
		t.Fatalf("o endpoint deveria ser aprendido com a resposta observada: %+v", o)
	}
	if o.Source.Exchange != "" {
		t.Fatalf("com o registro desligado a origem não deveria apontar para uma troca: %q", o.Source.Exchange)
	}
	if b, err := os.ReadFile(env.doc); err != nil || strings.Contains(string(b), "exchange:") {
		t.Fatalf("o documento não deveria gravar source.exchange: %v\n%s", err, b)
	}
	if err := env.Recorder.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	res, err := env.History.List(t.Context(), exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || len(res.Items) != 0 {
		t.Fatalf("com o registro desligado nada deveria ir para o histórico: %v %d", err, len(res.Items))
	}
}

// Um atraso injetado não muda a resposta do upstream: o endpoint atrasado é
// aprendido, e o override de curinga que o atrasou não o torna conhecido.
func TestDelayedUpstreamResponseIsLearned(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, map[string]string{
		"lenta.yaml": "schemaVersion: 1\nname: lenta\nupstream: " + up.URL + "\nmatch:\n  path: /lenta/*\n" +
			"overrides:\n  - name: atraso\n    match:\n      path: /lenta/*\n      method: GET\n    latency: 1ms\n",
	})
	env.get(t, "/lenta/x")
	doc := filepath.Join(env.dir, "routes", "lenta.yaml")
	deadline := time.Now().Add(5 * time.Second)
	for {
		env.settle(t)
		b, _ := os.ReadFile(doc)
		r, err := config.ParseRoute(doc, b)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Overrides) == 2 {
			if o := r.Overrides[1]; o.Match.Path != "/lenta/x" || o.Enabled() || o.Respond.Status != http.StatusAccepted {
				t.Fatalf("o endpoint atrasado deveria ser aprendido com a resposta do upstream: %+v", o)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("o endpoint atrasado não foi aprendido: %+v", r.Overrides)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestIdentifierBecomesParam(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	env := startLearning(t, learningOn, up.URL, nil)
	env.get(t, "/api/viacep/40415345/json")
	env.learned(t, 1)
	env.get(t, "/api/viacep/01001000/json")
	env.settle(t)
	ovs := env.learned(t, 1)
	o := ovs[0]
	if o.Match.Path != "/api/viacep/:id/json" || o.Match.Method != http.MethodGet || o.Enabled() {
		t.Fatalf("deveria haver um único override desligado com o path generalizado: %+v", o)
	}
	if o.Name != "get-api-viacep-id-json" {
		t.Fatalf("nome derivado do path generalizado inesperado: %s", o.Name)
	}
	// A resposta pré-preenchida é a da primeira troca, que ensinou o endpoint.
	if body, ok := o.Respond.Body.(map[string]any); !ok || body["path"] != "/api/viacep/40415345/json" {
		t.Fatalf("a resposta deveria ser a da troca de origem: %#v", o.Respond.Body)
	}
	// Desligado, ele não intercepta nenhum dos registros.
	if status, body := env.get(t, "/api/viacep/99999999/json"); status != http.StatusAccepted || body != `{"path":"/api/viacep/99999999/json","n":1}` {
		t.Fatalf("o override aprendido desligado não deveria interceptar: %d %q", status, body)
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
		t.Fatalf("esperados /api/users/me e /api/users/:id como overrides distintos: %s, %s", ovs[0].Match.Path, ovs[1].Match.Path)
	}
	if ovs[1].Name != "get-api-users-id" {
		t.Fatalf("nome inesperado: %s", ovs[1].Name)
	}
	// O literal continua conhecido pelo próprio override; outro registro, pelo
	// generalizado.
	env.get(t, "/api/users/me")
	env.get(t, "/api/users/7")
	env.settle(t)
	env.learned(t, 2)
}

// Um override aprendido antes da generalização, com path exato, é substituído
// pelo generalizado que o cobre, no mesmo lugar do documento; um criado pelo
// usuário para outro registro continua lá.
func TestLearnedExactIsAbsorbed(t *testing.T) {
	var hits atomic.Int64
	up := learningUpstream(t, &hits)
	exact := "  - name: get-cep-40415345-json\n    enabled: false\n    match:\n      path: /cep/40415345/json\n      method: GET\n" +
		"    respond:\n      status: 200\n      body: antigo\n    source:\n      kind: learned\n      at: 2026-09-18T00:00:00Z\n"
	user := "  - name: meu\n    enabled: false\n    match:\n      path: /cep/11111111/json\n      method: GET\n    respond:\n      status: 418\n"
	env := startLearning(t, learningOn, up.URL, map[string]string{
		"cep.yaml": "schemaVersion: 1\nname: cep\nupstream: " + up.URL + "\nmatch:\n  path: /cep/*\noverrides:\n" + exact + user,
	})
	cep := *env
	cep.doc = filepath.Join(env.dir, "routes", "cep.yaml")
	// O próprio registro aprendido continua conhecido: nada muda.
	cep.get(t, "/cep/40415345/json")
	cep.settle(t)
	cep.learned(t, 2)
	cep.get(t, "/cep/01001000/json")
	cep.settle(t)
	ovs := cep.learned(t, 2)
	if ovs[0].Match.Path != "/cep/:id/json" || ovs[0].Name != "get-cep-id-json" || ovs[0].Source.Kind != config.SourceLearned || ovs[0].Enabled() {
		t.Fatalf("o aprendido exato deveria ser substituído pelo generalizado: %+v", ovs[0])
	}
	if ovs[1].Name != "meu" {
		t.Fatalf("o override do usuário deveria continuar: %+v", ovs[1])
	}
	if rt := env.Live.Load().Route("cep"); rt.Override("get-cep-40415345-json") != nil || rt.Override("get-cep-id-json") == nil {
		t.Fatal("o snapshot deveria refletir a substituição")
	}
}

// Requisições simultâneas a registros distintos do mesmo endpoint geram um
// único override generalizado.
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
		t.Fatalf("esperado um único override generalizado: %+v", ovs[0].Match)
	}
}
