package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gamerjp64/devgateway/internal/config"
	"github.com/gamerjp64/devgateway/internal/exchange"
	"github.com/gamerjp64/devgateway/internal/store"
)

// End-to-end capture: real requests on the traffic port, history read through
// the API on the admin port.

type listBody struct {
	Items     []exchange.Exchange `json:"items"`
	Next      string              `json:"next"`
	Recording bool                `json:"recording"`
	Backend   string              `json:"backend"`
}

type errBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Field   string `json:"field"`
	Env     string `json:"env"`
	File    string `json:"file"`
}

func apiCall(t *testing.T, method, url string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, url, err, b)
		}
	}
	return res.StatusCode
}

func statusRoutes(t *testing.T) map[string]string {
	t.Helper()
	var hits atomic.Int64
	up := countingUpstream(t, &hits)
	fail := httptest500(t)
	return map[string]string{
		"payments.yaml": "schemaVersion: 1\nname: payments\nupstream: " + up.URL + "\nmatch:\n  path: /payments/*\n",
		"broken.yaml":   "schemaVersion: 1\nname: broken\nupstream: " + fail + "\nmatch:\n  path: /broken/*\n",
	}
}

func httptest500(t *testing.T) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "broken", http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func TestHistoryThroughAdminAPI(t *testing.T) {
	a := startWith(t, freePorts, statusRoutes(t))
	traffic, api := "http://"+a.TrafficAddr(), "http://"+a.AdminAddr()+"/api/exchanges"
	for i := range 3 {
		getBody(t, fmt.Sprintf("%s/payments/%d", traffic, i))
		getBody(t, fmt.Sprintf("%s/broken/%d", traffic, i))
	}

	var all listBody
	if st := apiCall(t, "GET", api, &all); st != 200 || len(all.Items) != 6 || !all.Recording || all.Backend != config.BackendMemory {
		t.Fatalf("listing: status %d, %d items, recording %v, backend %q", st, len(all.Items), all.Recording, all.Backend)
	}
	if all.Items[0].Path != "/broken/2" || all.Items[5].Path != "/payments/0" {
		t.Fatalf("the listing should go from newest to oldest: %s ... %s", all.Items[0].Path, all.Items[5].Path)
	}
	if all.Items[0].Response.Body != nil {
		t.Fatal("the listing should come without bodies")
	}

	var byRoute listBody
	apiCall(t, "GET", api+"?route=payments", &byRoute)
	var by5xx listBody
	apiCall(t, "GET", api+"?statusMin=500&statusMax=599", &by5xx)
	if len(byRoute.Items) != 3 || len(by5xx.Items) != 3 || by5xx.Items[0].Route != "broken" {
		t.Fatalf("filters: %d from payments and %d 5xx", len(byRoute.Items), len(by5xx.Items))
	}

	var page listBody
	apiCall(t, "GET", api+"?limit=4", &page)
	var rest listBody
	apiCall(t, "GET", api+"?limit=4&cursor="+page.Next, &rest)
	if len(page.Items) != 4 || page.Next == "" || len(rest.Items) != 2 || rest.Next != "" {
		t.Fatalf("pagination: %d + %d items, next %q / %q", len(page.Items), len(rest.Items), page.Next, rest.Next)
	}

	var full exchange.Exchange
	if st := apiCall(t, "GET", api+"/"+all.Items[5].ID, &full); st != 200 || string(full.Response.Body) != "upstream:/payments/0" {
		t.Fatalf("read by id: status %d, body %q", st, full.Response.Body)
	}
	var e errBody
	if st := apiCall(t, "GET", api+"/"+exchange.NewID(full.Start), &e); st != 404 || e.Error != "not_found" {
		t.Fatalf("unknown id: status %d, error %q", st, e.Error)
	}

	var newer exchange.Exchange
	if st := apiCall(t, "GET", api+"/"+all.Items[5].ID+"/newer?route=payments", &newer); st != 200 || newer.Path != "/payments/1" {
		t.Fatalf("next one with a filter: status %d, path %s", st, newer.Path)
	}
	e = errBody{}
	if st := apiCall(t, "GET", api+"/"+all.Items[0].ID+"/newer", &e); st != 404 || e.Error != "no_more" {
		t.Fatalf("end of the navigation: status %d, error %q", st, e.Error)
	}
	var older exchange.Exchange
	if st := apiCall(t, "GET", api+"/"+all.Items[0].ID+"/older?statusMin=500", &older); st != 200 || older.Path != "/broken/1" {
		t.Fatalf("previous one with the 5xx filter: status %d, path %s", st, older.Path)
	}

	e = errBody{}
	if st := apiCall(t, "GET", api+"?statusMin=abc", &e); st != 400 || e.Error != "bad_request" || e.Field != "statusMin" {
		t.Fatalf("invalid parameter: status %d, %+v", st, e)
	}
	e = errBody{}
	if st := apiCall(t, "GET", api+"?cursor=garbage", &e); st != 400 || e.Error != "bad_cursor" {
		t.Fatalf("invalid cursor: status %d, %+v", st, e)
	}
	if st := apiCall(t, "POST", api, nil); st != http.StatusMethodNotAllowed {
		t.Fatalf("unsupported method: status %d", st)
	}
}

// Requirement: Configurable history exposure

func TestHistoryExposureDisabled(t *testing.T) {
	t.Setenv("GATEWAY_HISTORY_EXPOSE", "false")
	a := startWith(t, freePorts, statusRoutes(t))
	getBody(t, "http://"+a.TrafficAddr()+"/payments/x")
	api := "http://" + a.AdminAddr() + "/api/exchanges"

	var e errBody
	if st := apiCall(t, "GET", api, &e); st != http.StatusForbidden || e.Error != "history_disabled" ||
		e.Env != "GATEWAY_HISTORY_EXPOSE" || e.Field != "history.expose" || !strings.Contains(e.Message, "disabled") {
		t.Fatalf("listing with exposure off: status %d, %+v", st, e)
	}
	for _, p := range []string{"/" + exchange.NewID(a.Live.Load().LoadedAt), "/x/older", "/x/newer"} {
		e = errBody{}
		if st := apiCall(t, "GET", api+p, &e); st != http.StatusForbidden || e.Error != "history_disabled" {
			t.Fatalf("%s with exposure off: status %d, %+v", p, st, e)
		}
	}
	// Exposure is independent of recording: the exchange was recorded.
	a.Recorder.Sync(t.Context())
	res, err := a.History.List(t.Context(), exchange.Filter{}, store.Page{})
	if err != nil || len(res.Items) != 1 {
		t.Fatalf("with exposure off recording should carry on: %d exchanges, %v", len(res.Items), err)
	}
	// Clearing returns no data and works even without exposure.
	if st := apiCall(t, "DELETE", api, nil); st != http.StatusNoContent {
		t.Fatalf("clearing without exposure: status %d", st)
	}
}

func TestHistoryExposureDisabledByFileNamesFile(t *testing.T) {
	a := startWith(t, `{"ports":{"traffic":0,"admin":0},"history":{"expose":false}}`, nil)
	var e errBody
	apiCall(t, "GET", "http://"+a.AdminAddr()+"/api/exchanges", &e)
	if e.Error != "history_disabled" || !strings.HasSuffix(e.File, "gateway.json") || e.Env != "" {
		t.Fatalf("the refusal should name gateway.json: %+v", e)
	}
}

func TestHistoryRecordingDisabled(t *testing.T) {
	a := startWith(t, `{"ports":{"traffic":0,"admin":0},"history":{"record":false}}`, statusRoutes(t))
	for i := range 3 {
		if st, body := getBody(t, fmt.Sprintf("http://%s/payments/%d", a.TrafficAddr(), i)); st != 200 || body != fmt.Sprintf("upstream:/payments/%d", i) {
			t.Fatalf("with recording off, forwarding should carry on: %d %q", st, body)
		}
	}
	var l listBody
	if st := apiCall(t, "GET", "http://"+a.AdminAddr()+"/api/exchanges", &l); st != 200 || len(l.Items) != 0 || l.Recording {
		t.Fatalf("no exchange should be recorded: status %d, %d items, recording %v", st, len(l.Items), l.Recording)
	}
}

func TestHistoryClearOnDemand(t *testing.T) {
	a := startWith(t, freePorts, statusRoutes(t))
	traffic, api := "http://"+a.TrafficAddr(), "http://"+a.AdminAddr()+"/api/exchanges"
	for range 3 {
		getBody(t, traffic+"/payments/before")
	}
	if st := apiCall(t, "DELETE", api, nil); st != http.StatusNoContent {
		t.Fatalf("clearing: status %d", st)
	}
	var l listBody
	if apiCall(t, "GET", api, &l); len(l.Items) != 0 {
		t.Fatalf("after clearing, the history should be empty: %d", len(l.Items))
	}
	getBody(t, traffic+"/payments/after")
	if apiCall(t, "GET", api, &l); len(l.Items) != 1 || l.Items[0].Path != "/payments/after" {
		t.Fatalf("later exchanges should be recorded again: %v", l.Items)
	}
}

func TestCaptureFollowsHistoryBackendSwitch(t *testing.T) {
	a := startWith(t, freePorts, statusRoutes(t))
	traffic, api := "http://"+a.TrafficAddr(), "http://"+a.AdminAddr()+"/api/exchanges"
	getBody(t, traffic+"/payments/memory")
	a.Recorder.Sync(t.Context())

	path := filepath.Join(t.TempDir(), "history.ndjson")
	s := a.Live.Load().Settings
	s.HistoryBackend, s.HistoryPath = config.BackendNDJSON, path
	if err := a.History.Switch(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	getBody(t, traffic+"/payments/ndjson")
	var l listBody
	apiCall(t, "GET", api, &l)
	if l.Backend != config.BackendNDJSON || len(l.Items) != 1 || l.Items[0].Path != "/payments/ndjson" {
		t.Fatalf("the capture should write into the backend in use: backend %q, %v", l.Backend, l.Items)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "/payments/ndjson") {
		t.Fatalf("the exchange should be in the NDJSON file: %v", err)
	}
}
