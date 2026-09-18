package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
)

// echo é o que o upstream de teste devolve sobre a requisição recebida.
type echo struct {
	Upstream string      `json:"upstream"`
	Method   string      `json:"method"`
	Path     string      `json:"path"`
	RawQuery string      `json:"rawQuery"`
	Host     string      `json:"host"`
	Header   http.Header `json:"header"`
	Body     []byte      `json:"body"`
}

func echoUpstream(t *testing.T, name string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(echo{name, r.Method, r.URL.Path, r.URL.RawQuery, r.Host, r.Header, body})
	}))
	t.Cleanup(s.Close)
	return s
}

func route(name, upstream, path string, mods ...func(*config.Route)) config.Route {
	r := config.Route{SchemaVersion: 1, Name: name, Upstream: upstream, Match: config.RouteMatch{Path: path}}
	for _, m := range mods {
		m(&r)
	}
	return r
}

func withHost(h string) func(*config.Route) { return func(r *config.Route) { r.Match.Host = h } }

func liveOf(t *testing.T, routes ...config.Route) *config.Live {
	t.Helper()
	var docs []config.RouteDoc
	for _, r := range routes {
		docs = append(docs, config.NewRouteDoc(r.Name+".yaml", r))
	}
	compiled, err := config.BuildRoutes(docs)
	if err != nil {
		t.Fatal(err)
	}
	return config.NewLive(config.NewSnapshot(config.Settings{}, compiled, nil))
}

func gateway(t *testing.T, routes ...config.Route) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(NewHandler(liveOf(t, routes...)))
	t.Cleanup(s.Close)
	return s
}

func do(t *testing.T, req *http.Request) (*http.Response, []byte) {
	t.Helper()
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res, body
}

func get(t *testing.T, gw *httptest.Server, path string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, gw.URL+path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1]
		} else {
			req.Header.Add(hdr[i], hdr[i+1])
		}
	}
	return do(t, req)
}

func echoOf(t *testing.T) func(*http.Response, []byte) echo {
	return func(res *http.Response, body []byte) echo {
		t.Helper()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d: %s", res.StatusCode, body)
		}
		var e echo
		if err := json.Unmarshal(body, &e); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		return e
	}
}

// Requirement: Roteamento por curinga de path

func TestMostSpecificWildcardWins(t *testing.T) {
	api, pay := echoUpstream(t, "api"), echoUpstream(t, "payments")
	gw := gateway(t, route("api", api.URL, "/api/*"), route("payments", pay.URL, "/api/payments/*"))
	if e := echoOf(t)(get(t, gw, "/api/payments/123")); e.Upstream != "payments" {
		t.Fatalf("esperado payments, recebido %s", e.Upstream)
	}
	if e := echoOf(t)(get(t, gw, "/api/users")); e.Upstream != "api" {
		t.Fatalf("esperado api, recebido %s", e.Upstream)
	}
}

func TestExactPathBeatsWildcard(t *testing.T) {
	api, health := echoUpstream(t, "api"), echoUpstream(t, "health")
	gw := gateway(t, route("api", api.URL, "/api/*"), route("health", health.URL, "/api/health"))
	if e := echoOf(t)(get(t, gw, "/api/health")); e.Upstream != "health" {
		t.Fatalf("esperado health, recebido %s", e.Upstream)
	}
}

func TestStripPrefix(t *testing.T) {
	up := echoUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL, "/api/payments/*", func(r *config.Route) { r.StripPrefix = true }))
	e := echoOf(t)(get(t, gw, "/api/payments/123?x=1"))
	if e.Path != "/123" || e.RawQuery != "x=1" {
		t.Fatalf("upstream deveria receber /123?x=1, recebeu %s?%s", e.Path, e.RawQuery)
	}
	if e := echoOf(t)(get(t, gw, "/api/payments")); e.Path != "/" {
		t.Fatalf("o próprio prefixo deveria virar /, virou %s", e.Path)
	}
}

func TestPrefixPreservedByDefault(t *testing.T) {
	up := echoUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL, "/api/payments/*"))
	if e := echoOf(t)(get(t, gw, "/api/payments/123")); e.Path != "/api/payments/123" {
		t.Fatalf("path deveria ser preservado, recebido %s", e.Path)
	}
}

func TestUpstreamBasePathIsJoined(t *testing.T) {
	up := echoUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL+"/v2", "/pay/*", func(r *config.Route) { r.StripPrefix = true }))
	if e := echoOf(t)(get(t, gw, "/pay/charge")); e.Path != "/v2/charge" {
		t.Fatalf("esperado /v2/charge, recebido %s", e.Path)
	}
}

func TestNoRouteMatches(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/a/*"), route("b", up.URL, "/b", withHost("b.local")))
	res, body := get(t, gw, "/zzz")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("esperado 404, recebido %d", res.StatusCode)
	}
	var d diag
	json.Unmarshal(body, &d)
	if d.Error != "no_route" || strings.Join(d.Patterns, ",") != "b.local/b,/a/*" {
		t.Fatalf("corpo deveria informar a falta de rota e listar os padrões: %s", body)
	}
}

// Requirement: Roteamento por host

func TestRouteByHostOnly(t *testing.T) {
	pay, other := echoUpstream(t, "payments"), echoUpstream(t, "other")
	gw := gateway(t, route("payments", pay.URL, "", withHost("payments.local")), route("other", other.URL, "/*"))
	if e := echoOf(t)(get(t, gw, "/qualquer", "Host", "payments.local")); e.Upstream != "payments" {
		t.Fatalf("esperado payments, recebido %s", e.Upstream)
	}
	if e := echoOf(t)(get(t, gw, "/qualquer", "Host", "payments.local:8080")); e.Upstream != "payments" {
		t.Fatalf("host com porta deveria casar, recebido %s", e.Upstream)
	}
	if e := echoOf(t)(get(t, gw, "/qualquer")); e.Upstream != "other" {
		t.Fatalf("outro host deveria cair na rota sem host, recebido %s", e.Upstream)
	}
}

func TestHostAndPathCombined(t *testing.T) {
	v2, fallback := echoUpstream(t, "v2"), echoUpstream(t, "fallback")
	gw := gateway(t, route("v2", v2.URL, "/v2/*", withHost("payments.local")), route("fallback", fallback.URL, "/*"))
	if e := echoOf(t)(get(t, gw, "/v1/charge", "Host", "payments.local")); e.Upstream != "fallback" {
		t.Fatalf("host casa mas path não; esperado fallback, recebido %s", e.Upstream)
	}
	if e := echoOf(t)(get(t, gw, "/v2/charge", "Host", "payments.local")); e.Upstream != "v2" {
		t.Fatalf("esperado v2, recebido %s", e.Upstream)
	}
}

func TestHostRouteBeatsPathRoute(t *testing.T) {
	byHost, byPath := echoUpstream(t, "host"), echoUpstream(t, "path")
	// A rota sem host tem até um path mais específico; o host vence mesmo assim.
	gw := gateway(t, route("host", byHost.URL, "/*", withHost("payments.local")), route("path", byPath.URL, "/api/charge"))
	if e := echoOf(t)(get(t, gw, "/api/charge", "Host", "payments.local")); e.Upstream != "host" {
		t.Fatalf("esperado host, recebido %s", e.Upstream)
	}
}

// Requirement: Encaminhamento de cabeçalhos

func TestForwardedHeadersAdded(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(get(t, gw, "/x", "Host", "front.local"))
	if got := e.Header.Get("X-Forwarded-For"); got != "127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, esperado o endereço do cliente", got)
	}
	if got := e.Header.Get("X-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto = %q", got)
	}
	if got := e.Header.Get("X-Forwarded-Host"); got != "front.local" {
		t.Errorf("X-Forwarded-Host = %q", got)
	}
	if e.Host != strings.TrimPrefix(up.URL, "http://") {
		t.Errorf("Host deveria ser o do upstream por padrão, recebido %q", e.Host)
	}
}

func TestHostPreservedOnDemand(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*", func(r *config.Route) { r.PreserveHost = true }))
	if e := echoOf(t)(get(t, gw, "/x", "Host", "payments.local")); e.Host != "payments.local" {
		t.Fatalf("Host deveria ser preservado, recebido %q", e.Host)
	}
}

func TestXForwardedForAccumulates(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(get(t, gw, "/x", "X-Forwarded-For", "203.0.113.9, 10.0.0.1"))
	if got := strings.Join(e.Header.Values("X-Forwarded-For"), ", "); got != "203.0.113.9, 10.0.0.1, 127.0.0.1" {
		t.Fatalf("cadeia deveria acumular, recebido %q", got)
	}
}

// Requirement: Tratamento de falha do upstream

func closedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return "http://" + addr
}

func TestUpstreamRefusesConnection(t *testing.T) {
	dead := closedAddr(t)
	gw := gateway(t, route("payments", dead, "/*"))
	res, body := get(t, gw, "/x")
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("esperado 502, recebido %d", res.StatusCode)
	}
	var d diag
	json.Unmarshal(body, &d)
	if d.Route != "payments" || d.Upstream != dead {
		t.Fatalf("corpo deveria nomear a rota e o upstream: %s", body)
	}
}

func TestUpstreamTimeout(t *testing.T) {
	released := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			close(released) // o gateway encerrou a requisição ao upstream
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	timeout := config.Duration(100 * time.Millisecond)
	gw := gateway(t, route("slow", slow.URL, "/*", func(r *config.Route) { r.Timeout = &timeout }))
	start := time.Now()
	res, body := get(t, gw, "/x")
	if res.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("esperado 504, recebido %d: %s", res.StatusCode, body)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("o gateway deveria desistir no tempo limite")
	}
	var d diag
	json.Unmarshal(body, &d)
	if d.Route != "slow" || d.Upstream != slow.URL {
		t.Fatalf("corpo deveria nomear a rota e o upstream: %s", body)
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("a requisição ao upstream não foi encerrada")
	}
}

func TestTimeoutDoesNotCutStreamingBody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, "fim")
	}))
	t.Cleanup(up.Close)
	timeout := config.Duration(100 * time.Millisecond)
	gw := gateway(t, route("s", up.URL, "/*", func(r *config.Route) { r.Timeout = &timeout }))
	res, body := get(t, gw, "/x")
	if res.StatusCode != 200 || string(body) != "fim" {
		t.Fatalf("o tempo limite vale até os cabeçalhos; corpo cortado: %d %q", res.StatusCode, body)
	}
}

func TestFailingUpstreamDoesNotAffectOthers(t *testing.T) {
	ok := echoUpstream(t, "ok")
	gw := gateway(t, route("dead", closedAddr(t), "/dead/*"), route("ok", ok.URL, "/ok/*"))
	for range 20 {
		if res, _ := get(t, gw, "/dead/x"); res.StatusCode != http.StatusBadGateway {
			t.Fatalf("esperado 502, recebido %d", res.StatusCode)
		}
	}
	if e := echoOf(t)(get(t, gw, "/ok/x")); e.Upstream != "ok" {
		t.Fatalf("rota saudável deveria seguir atendendo")
	}
}

// Requirement: Transparência do tráfego encaminhado

func TestMethodAndBodyPreserved(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	payload := []byte{0, 1, 2, 0xff, 0xfe, '\n', '\r', 0x80}
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/upload", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/x-custom-binary")
	e := echoOf(t)(do(t, req))
	if e.Method != "POST" || !bytes.Equal(e.Body, payload) || e.Header.Get("Content-Type") != "application/x-custom-binary" {
		t.Fatalf("método, corpo ou Content-Type alterados: %s %v %q", e.Method, e.Body, e.Header.Get("Content-Type"))
	}
	for _, m := range []string{"PUT", "PATCH", "DELETE", "OPTIONS", "PROPFIND"} {
		req, _ := http.NewRequest(m, gw.URL+"/x", nil)
		if e := echoOf(t)(do(t, req)); e.Method != m {
			t.Fatalf("método %s virou %s", m, e.Method)
		}
	}
}

func TestUpstreamStatusRelayed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Teapot", "sim")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "sou um bule")
	}))
	t.Cleanup(up.Close)
	gw := gateway(t, route("a", up.URL, "/*"))
	res, body := get(t, gw, "/x")
	if res.StatusCode != 418 || string(body) != "sou um bule" || res.Header.Get("X-Teapot") != "sim" ||
		len(res.Header.Values("Set-Cookie")) != 2 {
		t.Fatalf("resposta alterada: %d %q %v", res.StatusCode, body, res.Header)
	}
}

func TestStreamingRelayedIncrementally(t *testing.T) {
	next := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range 3 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			select {
			case <-next: // só emite o próximo evento depois que o cliente leu este
			case <-time.After(5 * time.Second):
				return
			}
		}
	}))
	t.Cleanup(up.Close)
	gw := gateway(t, route("sse", up.URL, "/*"))
	res, err := http.Get(gw.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	rd := bufio.NewReader(res.Body)
	for i := range 3 {
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatalf("evento %d não chegou antes do fim da resposta: %v", i, err)
		}
		if want := fmt.Sprintf("data: %d\n", i); line != want {
			t.Fatalf("evento %d = %q", i, line)
		}
		rd.ReadString('\n')
		next <- struct{}{}
	}
}
