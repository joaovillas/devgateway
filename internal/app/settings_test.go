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

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
)

// Configuração do processo pela API e troca a quente das portas.

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

// gatewayFile lê e interpreta o gateway.json do processo.
func (e *adminEnv) gatewayFile(t *testing.T) (config.GatewayFile, string) {
	t.Helper()
	path := filepath.Join(e.dir, "gateway.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := config.ParseGatewayFile(path, data)
	if err != nil {
		t.Fatalf("gateway.json gravado não é válido: %v\n%s", err, data)
	}
	return g, string(data)
}

// fileState guarda conteúdo e instante de modificação de um arquivo, para
// verificar que ele não foi tocado.
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

// occupy ocupa uma porta livre até o fim do teste e devolve o número dela.
func occupy(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// waitRefused espera até que o endereço deixe de aceitar conexões.
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
			t.Fatalf("a porta %s deveria ter deixado de aceitar conexões", port)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Scenario: Configuração do processo alterada pela API

func TestSettingsSeedChangedByAPI(t *testing.T) {
	flaky := map[string]string{"flaky.yaml": "schemaVersion: 1\nname: flaky\nmatch:\n  path: /flaky/*\n" +
		"overrides:\n  - name: half\n    match:\n      path: /flaky/*\n    respond:\n      status: 503\n    probability: 0.5\n"}
	// Sem upstream: o que o override não intercepta responde 501.
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
		t.Fatalf("alteração do seed: %d %s", r.status, r.body)
	}
	if s := e.Live.Load().Settings; s.Seed == nil || *s.Seed != 42 {
		t.Fatalf("o seed novo deveria valer sem reinício: %v", s.Seed)
	}
	g, raw := e.gatewayFile(t)
	if g.Seed == nil || *g.Seed != 42 || g.Ports == nil || g.Ports.Traffic == nil || *g.Ports.Traffic != 0 {
		t.Fatalf("gateway.json deveria declarar o seed e manter as portas:\n%s", raw)
	}
	for _, v := range res.Settings.Values {
		if v.Key == "seed" && (v.Source.Origin != config.OriginFile || v.Value != float64(42)) {
			t.Fatalf("a resposta deveria trazer o seed vindo do arquivo: %+v", v)
		}
	}
	got := pattern(e.traffic)

	// Um processo que já sobe com o mesmo seed decide igual: o seed alterado
	// pela API vale como se tivesse sido lido na carga.
	fresh := startAdmin(t, `{"ports":{"traffic":0,"admin":0},"seed":42}`, flaky)
	if want := pattern(fresh.traffic); got != want {
		t.Fatalf("as decisões deveriam seguir o seed 42:\napi:   %s\ncarga: %s", got, want)
	}
	if !strings.Contains(got, "503") || !strings.Contains(got, "501") {
		t.Fatalf("a probabilidade de 50%% deveria alternar as respostas: %s", got)
	}

	// null remove a chave do arquivo e o valor volta ao padrão.
	r = e.patchSettings(t, `{"seed":null}`)
	if g, raw := e.gatewayFile(t); r.status != 200 || g.Seed != nil || e.Live.Load().Settings.Seed != nil {
		t.Fatalf("remoção do seed: %d %s\n%s", r.status, r.body, raw)
	}
}

// Scenario: Valor do ambiente é travado

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
	// O documento bruto que altera a porta travada também é recusado.
	r := e.call(t, "PUT", "/settings/document", "application/json", `{"ports":{"traffic":9090,"admin":0},"seed":1}`)
	if ae := r.err(t); r.status != 409 || ae.Env != "GATEWAY_TRAFFIC_PORT" {
		t.Fatalf("documento que altera a porta travada: %d %s", r.status, r.body)
	}
	if after := stateOf(t, path); after != before {
		t.Fatalf("gateway.json não deveria ser modificado:\n%s", after.data)
	}
	if e.Live.Load() != snap {
		t.Fatal("a configuração em vigor não deveria mudar")
	}

	// Chaves não travadas continuam alteráveis, e um documento que não mexe
	// na chave travada é aceito.
	if r := e.patchSettings(t, `{"seed":2}`); r.status != 200 {
		t.Fatalf("seed com a porta travada: %d %s", r.status, r.body)
	}
	if r := e.call(t, "PUT", "/settings/document", "application/json", `{"ports":{"admin":0},"seed":3}`); r.status != 200 {
		t.Fatalf("documento que mantém a porta travada: %d %s", r.status, r.body)
	}
	if s := e.Live.Load().Settings; *s.Seed != 3 || s.Sources["ports.traffic"].Origin != config.OriginEnv {
		t.Fatalf("configuração depois do documento: %+v", s)
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
		{`{"desconhecido":1}`, "invalid", "desconhecido", 422},
		{`{"seed":`, "bad_request", "", 400},
		{`[1]`, "bad_request", "", 400},
	} {
		r := e.patchSettings(t, c.patch)
		ae := r.err(t)
		if r.status != c.status || ae.Error != c.code || ae.Field != c.field {
			t.Fatalf("%s: %d %s", c.patch, r.status, r.body)
		}
	}
	// If-Match divergente não grava.
	if r := e.patchSettings(t, `{"seed":1}`, "If-Match", `"outra"`); r.status != 412 || r.err(t).Error != "stale" {
		t.Fatalf("If-Match divergente: %d %s", r.status, r.body)
	}
	if after := stateOf(t, path); after != before {
		t.Fatalf("gateway.json não deveria ser modificado:\n%s", after.data)
	}
	// If-Match com a versão atual grava.
	etag := e.call(t, "GET", "/settings/document", "", "").header.Get("ETag")
	if r := e.patchSettings(t, `{"seed":1}`, "If-Match", etag); r.status != 200 {
		t.Fatalf("If-Match atual: %d %s", r.status, r.body)
	}
}

func TestSettingsDocument(t *testing.T) {
	t.Setenv("GATEWAY_TRAFFIC_PORT", "0")
	t.Setenv("GATEWAY_ADMIN_PORT", "0")
	e := startAdminWith(t, "", nil, Options{})
	path := filepath.Join(e.dir, "gateway.json")

	r := e.call(t, "GET", "/settings/document", "", "")
	if r.status != 200 || strings.TrimSpace(string(r.body)) != "{}" || r.header.Get("X-Gateway-File-Exists") != "false" {
		t.Fatalf("documento ausente: %d %v %s", r.status, r.header, r.body)
	}
	// A primeira alteração cria o arquivo, declarando a versão de schema.
	if r := e.patchSettings(t, `{"seed":9}`); r.status != 200 {
		t.Fatalf("criação por PATCH: %d %s", r.status, r.body)
	}
	if g, raw := e.gatewayFile(t); g.SchemaVersion == nil || *g.SchemaVersion != config.SchemaVersion || *g.Seed != 9 {
		t.Fatalf("gateway.json criado:\n%s", raw)
	}

	// O documento bruto é gravado como enviado e aplicado a quente.
	doc := "{\n  \"schemaVersion\": 1,\n  \"seed\": 11,\n  \"history\": { \"record\": false }\n}\n"
	r = e.call(t, "PUT", "/settings/document", "application/json", doc)
	var res settingsRes
	r.decode(t, &res)
	if r.status != 200 || !slices.Contains(res.Applied, "seed") || !slices.Contains(res.Applied, "history.record") {
		t.Fatalf("gravação do documento: %d %s", r.status, r.body)
	}
	if data, _ := os.ReadFile(path); string(data) != doc {
		t.Fatalf("o documento deveria ser gravado como enviado:\n%s", data)
	}
	if s := e.Live.Load().Settings; *s.Seed != 11 || s.HistoryRecord {
		t.Fatalf("o documento deveria valer sem reinício: %+v", s)
	}
	r = e.call(t, "GET", "/settings/document", "", "")
	if string(r.body) != doc || r.header.Get("X-Gateway-File-Exists") != "true" || r.header.Get("ETag") == "" {
		t.Fatalf("leitura do documento: %v %s", r.header, r.body)
	}

	before := stateOf(t, path)
	r = e.call(t, "PUT", "/settings/document", "application/json", "{\n  \"seed\": 1,\n}\n")
	if ae := r.err(t); r.status != 422 || ae.Line != 3 || ae.Column == 0 {
		t.Fatalf("JSON inválido: %d %s", r.status, r.body)
	}
	r = e.call(t, "PUT", "/settings/document", "application/json", `{"ports":{"traffic":1,"admin":1}}`)
	if r.status != 409 || r.err(t).Error != "locked" {
		t.Fatalf("portas vindas do ambiente: %d %s", r.status, r.body)
	}
	if r := e.call(t, "PUT", "/settings/document", "application/yaml", "seed: 1\n"); r.status != 415 {
		t.Fatalf("tipo de conteúdo errado: %d %s", r.status, r.body)
	}
	if after := stateOf(t, path); after != before {
		t.Fatal("gateway.json não deveria ser modificado pelas escritas recusadas")
	}
}

// Liga e desliga do modo aprendizado pela API.

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
		t.Fatalf("estado inicial do aprendizado: %d %s", r.status, r.body)
	}

	r = e.call(t, "PUT", "/learning", "", `{"enabled":true}`)
	l = learning{}
	r.decode(t, &l)
	if r.status != 200 || !l.Enabled || l.Source.Origin != config.OriginFile {
		t.Fatalf("ligar o aprendizado: %d %s", r.status, r.body)
	}
	if g, raw := e.gatewayFile(t); g.Learning == nil || g.Learning.Enabled == nil || !*g.Learning.Enabled {
		t.Fatalf("gateway.json deveria declarar o aprendizado ligado:\n%s", raw)
	}
	getBody(t, e.traffic+"/api/novo")
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
			t.Fatalf("o endpoint novo deveria ser aprendido: %+v", l)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if r := e.call(t, "PUT", "/learning", "", `{"enabled":false}`); r.status != 200 {
		t.Fatalf("desligar o aprendizado: %d %s", r.status, r.body)
	}
	getBody(t, e.traffic+"/api/outro")
	e.Recorder.Wait(t.Context())
	e.Learner.Sync(t.Context())
	l = learning{}
	e.call(t, "GET", "/learning", "", "").decode(t, &l)
	if l.Enabled || l.Learned["api"] != 1 {
		t.Fatalf("com o aprendizado desligado nada deveria ser aprendido: %+v", l)
	}
	if r := e.call(t, "PUT", "/learning", "", `{}`); r.status != 422 || r.err(t).Field != "enabled" {
		t.Fatalf("corpo sem enabled: %d %s", r.status, r.body)
	}
}

func TestLearningLockedByEnv(t *testing.T) {
	t.Setenv("GATEWAY_LEARNING", "false")
	e := startAdmin(t, freePorts, nil)
	before := stateOf(t, filepath.Join(e.dir, "gateway.json"))
	r := e.call(t, "PUT", "/learning", "", `{"enabled":true}`)
	if ae := r.err(t); r.status != 409 || ae.Error != "locked" || ae.Env != "GATEWAY_LEARNING" {
		t.Fatalf("aprendizado travado pelo ambiente: %d %s", r.status, r.body)
	}
	if stateOf(t, filepath.Join(e.dir, "gateway.json")) != before || e.Live.Load().Settings.LearningEnabled {
		t.Fatal("nada deveria mudar")
	}
	var l struct{ Locked bool }
	e.call(t, "GET", "/learning", "", "").decode(t, &l)
	if !l.Locked {
		t.Fatal("a leitura deveria informar a trava")
	}
}

// Scenarios: Backend do histórico trocado a quente, Backend novo indisponível
// preserva o atual — pela API.

func TestSettingsSwitchesHistoryBackend(t *testing.T) {
	e := startAdmin(t, freePorts, statusRoutes(t))
	getBody(t, e.traffic+"/payments/memoria")
	e.Recorder.Sync(t.Context())
	old := e.History.Backend()

	dbPath := filepath.Join(e.dir, "data", "history.db")
	r := e.patchSettings(t, `{"history":{"backend":"sqlite","path":"data/history.db"}}`)
	var res settingsRes
	r.decode(t, &res)
	if r.status != 200 || !slices.Contains(res.Applied, "history.backend") || len(res.Notes) != 1 ||
		!strings.Contains(res.Notes[0], "não foram migradas") || !strings.Contains(res.Notes[0], old) {
		t.Fatalf("troca de backend: %d %s", r.status, r.body)
	}
	if e.History.Backend() != config.BackendSQLite {
		t.Fatalf("o backend em uso deveria ser sqlite: %s", e.History.Backend())
	}
	getBody(t, e.traffic+"/payments/sqlite")
	var l listBody
	e.call(t, "GET", "/exchanges", "", "").decode(t, &l)
	if l.Backend != config.BackendSQLite || len(l.Items) != 1 || l.Items[0].Path != "/payments/sqlite" {
		t.Fatalf("as trocas seguintes deveriam ir para o sqlite, sem migração: %s %v", l.Backend, l.Items)
	}
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("o banco deveria estar ao lado do gateway.json: %v", err)
	}

	// Backend que não inicializa: recusado, o atual segue em uso.
	writeFile(t, filepath.Join(e.dir, "arquivo-comum"), "x")
	before := stateOf(t, filepath.Join(e.dir, "gateway.json"))
	r = e.patchSettings(t, `{"history":{"backend":"ndjson","path":"arquivo-comum/history.ndjson"}}`)
	if ae := r.err(t); r.status != 409 || ae.Error != "backend_unavailable" || !strings.Contains(ae.Message, "ndjson") || !strings.Contains(ae.Message, "sqlite") {
		t.Fatalf("backend indisponível: %d %s", r.status, r.body)
	}
	if e.History.Backend() != config.BackendSQLite || stateOf(t, filepath.Join(e.dir, "gateway.json")) != before {
		t.Fatal("o backend atual e o gateway.json deveriam ficar como estavam")
	}
	getBody(t, e.traffic+"/payments/depois")
	e.Recorder.Sync(t.Context())
	if res, err := e.History.List(t.Context(), exchange.Filter{}, store.Page{}); err != nil || len(res.Items) != 2 {
		t.Fatalf("o sqlite deveria seguir registrando: %d %v", len(res.Items), err)
	}
}

// Scenario: Porta trocada a quente

func TestTrafficPortSwitchedHot(t *testing.T) {
	arrived, release := make(chan struct{}), make(chan struct{})
	slow := httpServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-release
		io.WriteString(w, "lento")
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
		t.Fatalf("troca da porta de tráfego: %d %s", r.status, r.body)
	}
	newBase := fmt.Sprintf("http://127.0.0.1:%d", port)
	if st, body := getBody(t, newBase+"/fast/y"); st != 200 || body != "upstream:/fast/y" {
		t.Fatalf("a porta nova deveria atender: %d %q", st, body)
	}
	if !strings.HasSuffix(e.TrafficAddr(), ":"+strconv.Itoa(port)) {
		t.Fatalf("o endereço de tráfego deveria ser o novo: %s", e.TrafficAddr())
	}
	waitRefused(t, oldAddr)
	if g, raw := e.gatewayFile(t); g.Ports == nil || g.Ports.Traffic == nil || *g.Ports.Traffic != port {
		t.Fatalf("gateway.json deveria declarar a porta nova:\n%s", raw)
	}

	// A requisição em curso na porta antiga conclui normalmente.
	close(release)
	select {
	case res := <-done:
		if res.err != nil || res.status != 200 || res.body != "lento" {
			t.Fatalf("a requisição em curso deveria concluir: %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a requisição em curso não concluiu")
	}
}

// Scenario: Porta nova indisponível preserva a atual

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
		t.Fatalf("porta ocupada: %d %s", r.status, r.body)
	}
	if !strings.Contains(ae.Message, "segue na") {
		t.Fatalf("a recusa deveria dizer em que porta o tráfego segue: %s", ae.Message)
	}
	if st, body := getBody(t, e.traffic+"/payments/x"); st != 200 || body != "upstream:/payments/x" {
		t.Fatalf("a porta atual deveria seguir atendendo: %d %q", st, body)
	}
	if stateOf(t, path) != before || e.Live.Load() != snap {
		t.Fatal("nada deveria mudar: nem gateway.json, nem a configuração em vigor (seed incluído)")
	}
}

func TestAdminPortSwitchedHot(t *testing.T) {
	e := startAdminWith(t, freePorts, nil, Options{Heartbeat: time.Hour})
	oldAddr := e.AdminAddr()

	// Um fluxo de eventos aberto na porta antiga não segura a troca: ele
	// termina quando a porta sai de serviço, e o cliente reconecta.
	stream := openEvents(t, "http://"+oldAddr+"/api/events")
	stream.next(t, "hello")

	port := freePort(t)
	r := e.patchSettings(t, fmt.Sprintf(`{"ports":{"admin":%d}}`, port))
	var res settingsRes
	r.decode(t, &res)
	if r.status != 200 || !slices.Equal(res.Applied, []string{"ports.admin"}) {
		t.Fatalf("a resposta deveria sair pela porta antiga: %d %s", r.status, r.body)
	}
	for _, v := range res.Settings.Values {
		if v.Key == "ports.admin" && v.Value != float64(port) {
			t.Fatalf("a resposta deveria trazer a porta nova: %+v", v)
		}
	}
	e.api = fmt.Sprintf("http://127.0.0.1:%d/api", port)
	if r := e.call(t, "GET", "/status", "", ""); r.status != 200 {
		t.Fatalf("a API deveria atender na porta nova: %d", r.status)
	}
	waitRefused(t, oldAddr)
	stream.closed(t)
}
