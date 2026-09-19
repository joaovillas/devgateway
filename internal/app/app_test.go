package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

var testWeb = fstest.MapFS{"index.html": {Data: []byte("<p>panel</p>")}}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// startWith brings the process up with a gateway.json on free ports and the
// given routes.
func startWith(t *testing.T, gatewayJSON string, routes map[string]string) *App {
	t.Helper()
	// The test's t.TempDir directories created after this point (such as the
	// file of a history backend) are only removed once the process has shut
	// down and closed those files.
	t.TempDir()
	dir := tempDir(t)
	writeFile(t, filepath.Join(dir, "gateway.json"), gatewayJSON)
	for name, doc := range routes {
		writeFile(t, filepath.Join(dir, "routes", name), doc)
	}
	a, err := Start(Options{
		Loader: loaderFor(filepath.Join(dir, "gateway.json")),
		Web:    testWeb,
		Log:    quietLog(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Shutdown(context.Background()) })
	return a
}

func countingUpstream(t *testing.T, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		io.WriteString(w, "upstream:"+r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

const freePorts = `{"ports":{"traffic":0,"admin":0}}`

// Requirement: Separation between the traffic port and the admin port

func TestAdminPathOnTrafficPortIsTraffic(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	a := startWith(t, freePorts, map[string]string{
		"all.yaml": "schemaVersion: 1\nname: all\nupstream: " + up.URL + "\nmatch:\n  path: /*\n",
	})
	status, body := getBody(t, "http://"+a.TrafficAddr()+"/api/routes")
	if status != 200 || body != "upstream:/api/routes" {
		t.Fatalf("/api/ on the traffic port should go to the upstream: %d %q", status, body)
	}
}

func TestAdminPathWithoutRouteIsNoRoute(t *testing.T) {
	a := startWith(t, freePorts, nil)
	status, body := getBody(t, "http://"+a.TrafficAddr()+"/api/routes")
	if status != http.StatusNotFound || !strings.Contains(body, "no_route") {
		t.Fatalf("with no route, /api/ on the traffic port is just a path with no route: %d %s", status, body)
	}
}

func TestTrafficNotForwardedOnAdminPort(t *testing.T) {
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	a := startWith(t, freePorts, map[string]string{
		"payments.yaml": "schemaVersion: 1\nname: payments\nupstream: " + up.URL + "\nmatch:\n  path: /payments/*\n",
	})
	// Control: the route works on the traffic port.
	if status, _ := getBody(t, "http://"+a.TrafficAddr()+"/payments/x"); status != 200 || hits.Load() != 1 {
		t.Fatalf("the route should serve on the traffic port")
	}
	status, body := getBody(t, "http://"+a.AdminAddr()+"/payments/x")
	if hits.Load() != 1 || strings.Contains(body, "upstream:") {
		t.Fatalf("the admin port forwarded to the upstream: %d %q", status, body)
	}
}

func TestEqualPortsRefuseToStart(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gateway.json"), `{"ports":{"traffic":18555,"admin":18555}}`)
	_, err := Start(Options{Loader: loaderFor(filepath.Join(dir, "gateway.json")), Web: testWeb, Log: quietLog()})
	if err == nil || !strings.Contains(err.Error(), "18555") {
		t.Fatalf("it should refuse to start and report the conflict, got %v", err)
	}
}

func TestStartsWithDefaultsWhenGatewayFileMissing(t *testing.T) {
	var logs strings.Builder
	t.Setenv("GATEWAY_TRAFFIC_PORT", "0")
	t.Setenv("GATEWAY_ADMIN_PORT", "0")
	path := filepath.Join(t.TempDir(), "gateway.json")
	a, err := Start(Options{
		Loader: loaderFor(path),
		Web:    testWeb,
		Log:    slog.New(slog.NewTextHandler(&logs, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Shutdown(context.Background())
	if !strings.Contains(logs.String(), "gateway.json not found") {
		t.Fatalf("it should log a warning with the path it looked at: %s", logs.String())
	}
	if status, _ := getBody(t, "http://"+a.AdminAddr()+"/"); status != 200 {
		t.Fatalf("the gateway should stay operational, admin answered %d", status)
	}
}
