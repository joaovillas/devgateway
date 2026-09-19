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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamerjp64/devgateway/internal/capture"
	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/store"
)

// echo is what the test upstream reports back about the request it received.
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

// recording is the settings these tests run with: recording on with the
// default capture limit, so that every forwarding test also exercises the
// capture.
func recording() config.Settings {
	return config.Settings{HistoryRecord: true, HistoryExpose: true, CaptureMaxBodyBytes: 64 << 10}
}

func liveOf(t *testing.T, routes ...config.Route) *config.Live {
	t.Helper()
	return liveWith(t, recording(), routes...)
}

func liveWith(t *testing.T, s config.Settings, routes ...config.Route) *config.Live {
	t.Helper()
	var docs []config.RouteDoc
	for _, r := range routes {
		docs = append(docs, config.NewRouteDoc(r.Name+".yaml", r))
	}
	compiled, err := config.BuildRoutes(docs)
	if err != nil {
		t.Fatal(err)
	}
	return config.NewLive(config.NewSnapshot(s, compiled, nil))
}

func gateway(t *testing.T, routes ...config.Route) *httptest.Server {
	t.Helper()
	rec := capture.NewRecorder(store.NewMemory(10), nil, nil)
	t.Cleanup(rec.Close)
	s := httptest.NewServer(NewHandler(liveOf(t, routes...), rec))
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

// Requirement: Path wildcard routing

func TestMostSpecificWildcardWins(t *testing.T) {
	api, pay := echoUpstream(t, "api"), echoUpstream(t, "payments")
	gw := gateway(t, route("api", api.URL, "/api/*"), route("payments", pay.URL, "/api/payments/*"))
	if e := echoOf(t)(get(t, gw, "/api/payments/123")); e.Upstream != "payments" {
		t.Fatalf("want payments, got %s", e.Upstream)
	}
	if e := echoOf(t)(get(t, gw, "/api/users")); e.Upstream != "api" {
		t.Fatalf("want api, got %s", e.Upstream)
	}
}

func TestExactPathBeatsWildcard(t *testing.T) {
	api, health := echoUpstream(t, "api"), echoUpstream(t, "health")
	gw := gateway(t, route("api", api.URL, "/api/*"), route("health", health.URL, "/api/health"))
	if e := echoOf(t)(get(t, gw, "/api/health")); e.Upstream != "health" {
		t.Fatalf("want health, got %s", e.Upstream)
	}
}

func TestStripPrefix(t *testing.T) {
	up := echoUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL, "/api/payments/*", func(r *config.Route) { r.StripPrefix = true }))
	e := echoOf(t)(get(t, gw, "/api/payments/123?x=1"))
	if e.Path != "/123" || e.RawQuery != "x=1" {
		t.Fatalf("the upstream should have received /123?x=1, it got %s?%s", e.Path, e.RawQuery)
	}
	if e := echoOf(t)(get(t, gw, "/api/payments")); e.Path != "/" {
		t.Fatalf("the prefix itself should become /, it became %s", e.Path)
	}
}

func TestPrefixPreservedByDefault(t *testing.T) {
	up := echoUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL, "/api/payments/*"))
	if e := echoOf(t)(get(t, gw, "/api/payments/123")); e.Path != "/api/payments/123" {
		t.Fatalf("the path should be preserved, got %s", e.Path)
	}
}

func TestUpstreamBasePathIsJoined(t *testing.T) {
	up := echoUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL+"/v2", "/pay/*", func(r *config.Route) { r.StripPrefix = true }))
	if e := echoOf(t)(get(t, gw, "/pay/charge")); e.Path != "/v2/charge" {
		t.Fatalf("want /v2/charge, got %s", e.Path)
	}
}

func TestNoRouteMatches(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/a/*"), route("b", up.URL, "/b", withHost("b.local")))
	res, body := get(t, gw, "/zzz")
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", res.StatusCode)
	}
	var d diag
	json.Unmarshal(body, &d)
	if d.Error != "no_route" || strings.Join(d.Patterns, ",") != "b.local/b,/a/*" {
		t.Fatalf("the body should report the missing route and list the patterns: %s", body)
	}
}

// Requirement: Host routing

func TestRouteByHostOnly(t *testing.T) {
	pay, other := echoUpstream(t, "payments"), echoUpstream(t, "other")
	gw := gateway(t, route("payments", pay.URL, "", withHost("payments.local")), route("other", other.URL, "/*"))
	if e := echoOf(t)(get(t, gw, "/anything", "Host", "payments.local")); e.Upstream != "payments" {
		t.Fatalf("want payments, got %s", e.Upstream)
	}
	if e := echoOf(t)(get(t, gw, "/anything", "Host", "payments.local:8080")); e.Upstream != "payments" {
		t.Fatalf("a host with a port should match, got %s", e.Upstream)
	}
	if e := echoOf(t)(get(t, gw, "/anything")); e.Upstream != "other" {
		t.Fatalf("another host should fall through to the route without a host, got %s", e.Upstream)
	}
}

func TestHostAndPathCombined(t *testing.T) {
	v2, fallback := echoUpstream(t, "v2"), echoUpstream(t, "fallback")
	gw := gateway(t, route("v2", v2.URL, "/v2/*", withHost("payments.local")), route("fallback", fallback.URL, "/*"))
	if e := echoOf(t)(get(t, gw, "/v1/charge", "Host", "payments.local")); e.Upstream != "fallback" {
		t.Fatalf("the host matches but the path does not; want fallback, got %s", e.Upstream)
	}
	if e := echoOf(t)(get(t, gw, "/v2/charge", "Host", "payments.local")); e.Upstream != "v2" {
		t.Fatalf("want v2, got %s", e.Upstream)
	}
}

func TestHostRouteBeatsPathRoute(t *testing.T) {
	byHost, byPath := echoUpstream(t, "host"), echoUpstream(t, "path")
	// The route without a host even has a more specific path; the host wins
	// anyway.
	gw := gateway(t, route("host", byHost.URL, "/*", withHost("payments.local")), route("path", byPath.URL, "/api/charge"))
	if e := echoOf(t)(get(t, gw, "/api/charge", "Host", "payments.local")); e.Upstream != "host" {
		t.Fatalf("want host, got %s", e.Upstream)
	}
}

// Requirement: Header forwarding

func TestForwardedHeadersAdded(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(get(t, gw, "/x", "Host", "front.local"))
	if got := e.Header.Get("X-Forwarded-For"); got != "127.0.0.1" {
		t.Errorf("X-Forwarded-For = %q, want the client address", got)
	}
	if got := e.Header.Get("X-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto = %q", got)
	}
	if got := e.Header.Get("X-Forwarded-Host"); got != "front.local" {
		t.Errorf("X-Forwarded-Host = %q", got)
	}
}

func TestOriginalHostByDefault(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	if e := echoOf(t)(get(t, gw, "/x", "Host", "payments.local")); e.Host != "payments.local" {
		t.Fatalf("the original Host should be passed through by default, got %q", e.Host)
	}
}

func TestHostRewrittenOnDemand(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*", func(r *config.Route) { r.RewriteHost = true }))
	e := echoOf(t)(get(t, gw, "/x", "Host", "payments.local"))
	if e.Host != strings.TrimPrefix(up.URL, "http://") {
		t.Fatalf("with rewriteHost the Host should be the upstream's, got %q", e.Host)
	}
}

func TestXForwardedForAccumulates(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(get(t, gw, "/x", "X-Forwarded-For", "203.0.113.9, 10.0.0.1"))
	if got := strings.Join(e.Header.Values("X-Forwarded-For"), ", "); got != "203.0.113.9, 10.0.0.1, 127.0.0.1" {
		t.Fatalf("the chain should accumulate, got %q", got)
	}
}

func TestForwardedHostAndProtoPreserved(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(get(t, gw, "/x", "Host", "internal.local",
		"X-Forwarded-Host", "public.example.com", "X-Forwarded-Proto", "https",
		"Forwarded", "for=203.0.113.9;proto=https"))
	if got := e.Header.Values("X-Forwarded-Host"); len(got) != 1 || got[0] != "public.example.com" {
		t.Errorf("X-Forwarded-Host should arrive as it was sent, got %q", got)
	}
	if got := e.Header.Values("X-Forwarded-Proto"); len(got) != 1 || got[0] != "https" {
		t.Errorf("X-Forwarded-Proto should arrive as it was sent, got %q", got)
	}
	if got := e.Header.Values("Forwarded"); len(got) != 1 || got[0] != "for=203.0.113.9;proto=https" {
		t.Errorf("Forwarded should arrive as it was sent, got %q", got)
	}
	if got := e.Header.Get("X-Forwarded-For"); got != "127.0.0.1" {
		t.Errorf("a missing X-Forwarded-For should be filled in, got %q", got)
	}
}

func TestForwardedHeaderDeclaredHopByHopNotRestored(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(get(t, gw, "/x", "Host", "front.local",
		"Connection", "X-Forwarded-Host", "X-Forwarded-Host", "this-connection-only.local"))
	if got := e.Header.Get("X-Forwarded-Host"); got != "front.local" {
		t.Fatalf("a header listed in Connection should not be passed through, got %q", got)
	}
}

func TestArbitraryHeadersRelayed(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(get(t, gw, "/x",
		"Authorization", "Bearer abc.def.ghi",
		"X-Custom", "value with  spaces",
		"X-Repeated", "one",
		"X-Repeated", "two",
		"X-Repeated", "one",
		"Accept", "application/json",
		"Accept", "text/plain;q=0.5",
		"Cookie", "a=1; b=2",
	))
	want := http.Header{
		"Authorization": {"Bearer abc.def.ghi"},
		"X-Custom":      {"value with  spaces"},
		"X-Repeated":    {"one", "two", "one"},
		"Accept":        {"application/json", "text/plain;q=0.5"},
		"Cookie":        {"a=1; b=2"},
	}
	for k, v := range want {
		if got := e.Header.Values(k); strings.Join(got, "\x00") != strings.Join(v, "\x00") {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// rawGet sends a hand-written HTTP/1.1 request, without the headers the Go
// client would add on its own.
func rawGet(t *testing.T, gw *httptest.Server, path string, hdr ...string) (*http.Response, []byte) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(gw.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: front.local\r\nConnection: close\r\n", path)
	for i := 0; i+1 < len(hdr); i += 2 {
		fmt.Fprintf(&b, "%s: %s\r\n", hdr[i], hdr[i+1])
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
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

func TestNoOtherHeadersAdded(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(rawGet(t, gw, "/x", "X-Custom", "1"))
	var got []string
	for k := range e.Header {
		got = append(got, k)
	}
	slices.Sort(got)
	want := []string{"X-Custom", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Gateway"}
	if !slices.Equal(got, want) {
		t.Fatalf("the upstream should receive only the headers sent plus the gateway's:\ngot  %q\nwant %q", got, want)
	}
}

// A forwarding header that arrives empty does not count as filled in: the
// gateway completes it as if it were missing. Forwarded, which the gateway
// does not add, is still passed through exactly as it arrived.
func TestEmptyForwardedHeadersAreFilled(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	e := echoOf(t)(rawGet(t, gw, "/x",
		"X-Forwarded-Host", "", "X-Forwarded-Proto", " ", "X-Forwarded-For", "", "Forwarded", ""))
	if got := e.Header.Values("X-Forwarded-Host"); len(got) != 1 || got[0] != "front.local" {
		t.Errorf("an empty X-Forwarded-Host should get the original host, got %q", got)
	}
	if got := e.Header.Values("X-Forwarded-Proto"); len(got) != 1 || got[0] != "http" {
		t.Errorf("an empty X-Forwarded-Proto should get the original scheme, got %q", got)
	}
	if got := e.Header.Values("X-Forwarded-For"); len(got) != 1 || got[0] != "127.0.0.1" {
		t.Errorf("an empty X-Forwarded-For should get just the client, got %q", got)
	}
	if got, ok := e.Header["Forwarded"]; !ok || len(got) != 1 || got[0] != "" {
		t.Errorf("Forwarded should be passed through as it arrived, got %q", got)
	}
}

// Requirement: Gateway identification header

func TestIdentWithoutIntervention(t *testing.T) {
	up := echoUpstream(t, "payments")
	gw := gateway(t, route("payments", up.URL, "/*"))
	res, body := get(t, gw, "/x", "X-Gateway", "forged-by-the-client")
	e := echoOf(t)(res, body)
	if got := e.Header.Values(HeaderGateway); len(got) != 1 || got[0] != "route=payments" {
		t.Errorf("the upstream should receive a single X-Gateway with the route, got %q", got)
	}
	if got := res.Header.Values(HeaderGateway); len(got) != 1 || got[0] != "route=payments" {
		t.Errorf("the client should receive a single X-Gateway with the route, got %q", got)
	}
}

func TestIdentReplacesUpstreamHeader(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(HeaderGateway, "route=another-gateway")
	}))
	t.Cleanup(up.Close)
	gw := gateway(t, route("payments", up.URL, "/*"))
	if res, _ := get(t, gw, "/x"); strings.Join(res.Header.Values(HeaderGateway), ",") != "route=payments" {
		t.Fatalf("the response's X-Gateway should be this gateway's, got %q", res.Header.Values(HeaderGateway))
	}
}

func TestIdentFormat(t *testing.T) {
	for _, c := range []struct {
		id   Ident
		want string
	}{
		{Ident{}, ""},
		{Ident{Route: "payments"}, "route=payments"},
		{Ident{Route: "payments", Override: "payments/flaky", Intervention: "synthesized"},
			"route=payments; override=payments/flaky; intervention=synthesized"},
	} {
		if got := c.id.String(); got != c.want {
			t.Errorf("%+v = %q, want %q", c.id, got, c.want)
		}
	}
}

func TestIdentOnGatewayErrors(t *testing.T) {
	gw := gateway(t,
		route("empty", "", "/empty/*"),
		route("dead", closedAddr(t), "/dead/*"),
	)
	for _, c := range []struct {
		path   string
		status int
		want   string
	}{
		{"/nothing", http.StatusNotFound, ""},
		{"/empty/x", http.StatusNotImplemented, "route=empty"},
		{"/dead/x", http.StatusBadGateway, "route=dead"},
	} {
		res, body := get(t, gw, c.path)
		if res.StatusCode != c.status {
			t.Fatalf("%s: want %d, got %d: %s", c.path, c.status, res.StatusCode, body)
		}
		if got, ok := res.Header[HeaderGateway]; !ok || len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: X-Gateway = %q (present: %v), want %q", c.path, got, ok, c.want)
		}
	}
}

// Requirement: Upstream failure handling

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
		t.Fatalf("want 502, got %d", res.StatusCode)
	}
	var d diag
	json.Unmarshal(body, &d)
	if d.Route != "payments" || d.Upstream != dead {
		t.Fatalf("the body should name the route and the upstream: %s", body)
	}
}

func TestUpstreamTimeout(t *testing.T) {
	released := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			close(released) // the gateway ended the request to the upstream
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	timeout := config.Duration(100 * time.Millisecond)
	gw := gateway(t, route("slow", slow.URL, "/*", func(r *config.Route) { r.Timeout = &timeout }))
	start := time.Now()
	res, body := get(t, gw, "/x")
	if res.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("want 504, got %d: %s", res.StatusCode, body)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the gateway should give up at the timeout")
	}
	var d diag
	json.Unmarshal(body, &d)
	if d.Route != "slow" || d.Upstream != slow.URL {
		t.Fatalf("the body should name the route and the upstream: %s", body)
	}
	if got := res.Header.Get(HeaderGateway); got != "route=slow" {
		t.Errorf("X-Gateway of the 504 = %q, want route=slow", got)
	}
	select {
	case <-released:
	case <-time.After(2 * time.Second):
		t.Fatal("the request to the upstream was not ended")
	}
}

func TestTimeoutDoesNotCutStreamingBody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, "end")
	}))
	t.Cleanup(up.Close)
	timeout := config.Duration(100 * time.Millisecond)
	gw := gateway(t, route("s", up.URL, "/*", func(r *config.Route) { r.Timeout = &timeout }))
	res, body := get(t, gw, "/x")
	if res.StatusCode != 200 || string(body) != "end" {
		t.Fatalf("the timeout only applies up to the headers; body was cut: %d %q", res.StatusCode, body)
	}
}

func TestFailingUpstreamDoesNotAffectOthers(t *testing.T) {
	ok := echoUpstream(t, "ok")
	gw := gateway(t, route("dead", closedAddr(t), "/dead/*"), route("ok", ok.URL, "/ok/*"))
	for range 20 {
		if res, _ := get(t, gw, "/dead/x"); res.StatusCode != http.StatusBadGateway {
			t.Fatalf("want 502, got %d", res.StatusCode)
		}
	}
	if e := echoOf(t)(get(t, gw, "/ok/x")); e.Upstream != "ok" {
		t.Fatalf("the healthy route should keep serving")
	}
}

// Requirement: Transparency of forwarded traffic

func TestMethodAndBodyPreserved(t *testing.T) {
	up := echoUpstream(t, "a")
	gw := gateway(t, route("a", up.URL, "/*"))
	payload := []byte{0, 1, 2, 0xff, 0xfe, '\n', '\r', 0x80}
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/upload", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/x-custom-binary")
	e := echoOf(t)(do(t, req))
	if e.Method != "POST" || !bytes.Equal(e.Body, payload) || e.Header.Get("Content-Type") != "application/x-custom-binary" {
		t.Fatalf("method, body or Content-Type changed: %s %v %q", e.Method, e.Body, e.Header.Get("Content-Type"))
	}
	for _, m := range []string{"PUT", "PATCH", "DELETE", "OPTIONS", "PROPFIND"} {
		req, _ := http.NewRequest(m, gw.URL+"/x", nil)
		if e := echoOf(t)(do(t, req)); e.Method != m {
			t.Fatalf("method %s turned into %s", m, e.Method)
		}
	}
}

func TestUpstreamStatusRelayed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Teapot", "yes")
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "i am a teapot")
	}))
	t.Cleanup(up.Close)
	gw := gateway(t, route("a", up.URL, "/*"))
	res, body := get(t, gw, "/x")
	if res.StatusCode != 418 || string(body) != "i am a teapot" || res.Header.Get("X-Teapot") != "yes" ||
		len(res.Header.Values("Set-Cookie")) != 2 {
		t.Fatalf("the response was changed: %d %q %v", res.StatusCode, body, res.Header)
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
			case <-next: // only emit the next event once the client has read this one
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
			t.Fatalf("event %d did not arrive before the response ended: %v", i, err)
		}
		if want := fmt.Sprintf("data: %d\n", i); line != want {
			t.Fatalf("event %d = %q", i, line)
		}
		rd.ReadString('\n')
		next <- struct{}{}
	}
}
