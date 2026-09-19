// Package storetest is the contract suite that every store.Store
// implementation has to pass.
package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gamerjp64/devgateway/internal/exchange"
	"github.com/gamerjp64/devgateway/internal/store"
)

// TB is the subset of testing.TB the suite uses, so that it can run against
// a recorder and prove that it catches wrong implementations.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	TempDir() string
	Cleanup(func())
}

// Factory creates instances of the backend under test.
type Factory struct {
	// New creates an empty store.
	New func(t TB) store.Store
	// Reopen closes the store and opens another one over the same data. Nil
	// for backends without persistence.
	Reopen func(t TB, s store.Store) store.Store
}

// Case is one case of the suite.
type Case struct {
	Name string
	Run  func(t TB, f Factory)
	// Persistent: the case only applies to backends with Reopen.
	Persistent bool
}

// Cases lists the full suite.
var Cases = []Case{
	{Name: "RecordAndGet", Run: recordAndGet},
	{Name: "GetMissing", Run: getMissing},
	{Name: "NewestFirstWithPagination", Run: newestFirstWithPagination},
	{Name: "PageLimitDefaultAndCap", Run: pageLimitDefaultAndCap},
	{Name: "Filters", Run: filters},
	{Name: "CombinedFiltersAreConjunctive", Run: combinedFilters},
	{Name: "FilteredPagination", Run: filteredPagination},
	{Name: "NeighborWalk", Run: neighborWalk},
	{Name: "NeighborRespectsFilter", Run: neighborRespectsFilter},
	{Name: "NeighborMissing", Run: neighborMissing},
	{Name: "ClearThenRecord", Run: clearThenRecord},
	{Name: "CursorBeforeClearIsStale", Run: cursorBeforeClear},
	{Name: "DuplicateIDRejected", Run: duplicateIDRejected},
	{Name: "ConcurrentRecord", Run: concurrentRecord},
	{Name: "ChronologicalOrderNotCompletionOrder", Run: chronologicalOrder},
	{Name: "ChronologicalOrderSurvivesRestart", Run: chronologicalOrderSurvivesRestart, Persistent: true},
	{Name: "SurvivesRestart", Run: survivesRestart, Persistent: true},
	{Name: "CursorBeforeClearSurvivesRestart", Run: cursorBeforeClearSurvivesRestart, Persistent: true},
}

// Run executes the suite as subtests of t.
func Run(t *testing.T, f Factory) {
	for _, c := range Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Persistent && f.Reopen == nil {
				t.Skip("backend without persistence")
			}
			c.Run(t, f)
		})
	}
}

var ctx = context.Background()

var base = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

// sample builds the i-th exchange of a sequence with varied attributes.
func sample(i int) *exchange.Exchange {
	routes := []string{"payments", "users", "orders"}
	methods := []string{"GET", "POST", "DELETE"}
	statuses := []int{200, 404, 500, 503, 201}
	e := &exchange.Exchange{
		ID:         fmt.Sprintf("ID%06d", i),
		Seq:        uint64(i),
		Start:      base.Add(time.Duration(i) * time.Second),
		Method:     methods[i%len(methods)],
		Host:       "gw.local",
		Path:       fmt.Sprintf("/api/%s/%d", routes[i%len(routes)], i),
		Query:      "a=1",
		ClientAddr: "127.0.0.1:5000",
		Route:      routes[i%len(routes)],
		Upstream:   "http://" + routes[i%len(routes)] + ".local",
		Outcome:    exchange.OutcomeUpstream,
		Status:     statuses[i%len(statuses)],
		Request: exchange.Message{
			Headers: http.Header{"X-N": {fmt.Sprint(i)}},
			Body:    []byte{byte(i), 0, 0xff, '\n'},
			Size:    4,
		},
		Response: exchange.Message{
			Headers: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"a=1", "b=2"}},
			Body:    []byte(fmt.Sprintf(`{"n":%d}`, i)),
			Size:    100,
		},
		Timing: exchange.Timing{TotalMs: 12.5, UpstreamMs: 10, InjectedMs: 0, GatewayMs: 2.5},
	}
	if i%4 == 0 {
		e.Override = e.Route + "/flaky"
		e.Interventions = []string{"synthesized"}
		e.Outcome = exchange.OutcomeSynthesized
		e.Response.Truncated = true
	}
	return e
}

func recordN(t TB, s store.Store, n int) []*exchange.Exchange {
	t.Helper()
	out := make([]*exchange.Exchange, n)
	for i := range n {
		out[i] = sample(i)
		if err := s.Record(ctx, out[i]); err != nil {
			t.Fatalf("Record(%d): %v", i, err)
		}
	}
	return out
}

func equalExchange(a, b exchange.Exchange) bool {
	// Instants compared by value; the zone can change in serialization.
	if !a.Start.Equal(b.Start) {
		return false
	}
	a.Start, b.Start = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func ids(items []exchange.Exchange) []string {
	out := make([]string, len(items))
	for i, e := range items {
		out[i] = e.ID
	}
	return out
}

func recordAndGet(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 5)
	want := sample(4)
	got, err := s.Get(ctx, want.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !equalExchange(got, *want) {
		t.Fatalf("the exchange read back differs from the one recorded:\n%+v\n%+v", got, *want)
	}
	if !bytes.Equal(got.Request.Body, []byte{4, 0, 0xff, '\n'}) {
		t.Fatalf("binary body altered: %v", got.Request.Body)
	}
}

func getMissing(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 3)
	if _, err := s.Get(ctx, "does-not-exist"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func newestFirstWithPagination(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 150)
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: 50})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Items) != 50 || res.Items[0].ID != "ID000149" || res.Items[49].ID != "ID000100" {
		t.Fatalf("the first page should hold the 50 newest, from 149 down to 100: %v", ids(res.Items))
	}
	if res.Next == "" {
		t.Fatalf("the first page should signal a continuation")
	}
	var all []string
	all = append(all, ids(res.Items)...)
	for pages := 1; res.Next != ""; pages++ {
		if pages > 10 {
			t.Fatalf("pagination does not terminate")
		}
		res, err = s.List(ctx, exchange.Filter{}, store.Page{Limit: 50, Cursor: res.Next})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		all = append(all, ids(res.Items)...)
	}
	if len(all) != 150 {
		t.Fatalf("pagination should walk 150 exchanges, it walked %d", len(all))
	}
	for i, id := range all {
		if want := fmt.Sprintf("ID%06d", 149-i); id != want {
			t.Fatalf("position %d: %s, want %s", i, id, want)
		}
	}
}

func pageLimitDefaultAndCap(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, store.DefaultLimit+5)
	res, err := s.List(ctx, exchange.Filter{}, store.Page{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(res.Items) != store.DefaultLimit || res.Next == "" {
		t.Fatalf("with no limit, the page should hold %d items and a continuation: %d %q", store.DefaultLimit, len(res.Items), res.Next)
	}
	res, err = s.List(ctx, exchange.Filter{}, store.Page{Limit: 10})
	if err != nil || len(res.Items) != 10 {
		t.Fatalf("limit 10: %d items, %v", len(res.Items), err)
	}
	res, _ = s.List(ctx, exchange.Filter{}, store.Page{Limit: store.DefaultLimit + 5})
	if len(res.Items) != store.DefaultLimit+5 || res.Next != "" {
		t.Fatalf("a page that covers everything should have no continuation: %d %q", len(res.Items), res.Next)
	}
}

// expectFilter compares the backend's result against the reference semantics.
func expectFilter(t TB, s store.Store, all []*exchange.Exchange, name string, flt exchange.Filter) {
	t.Helper()
	var want []string
	for i := len(all) - 1; i >= 0; i-- {
		if flt.Match(all[i]) {
			want = append(want, all[i].ID)
		}
	}
	if len(want) == 0 {
		t.Fatalf("%s: the test filter selects nothing", name)
	}
	res, err := s.List(ctx, flt, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatalf("%s: List: %v", name, err)
	}
	if got := ids(res.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", name, got, want)
	}
}

func boolPtr(b bool) *bool { return &b }

func filters(t TB, f Factory) {
	s := f.New(t)
	all := recordN(t, s, 60)
	cases := []struct {
		name string
		flt  exchange.Filter
	}{
		{"route", exchange.Filter{Route: "payments"}},
		{"upstream", exchange.Filter{Upstream: "http://users.local"}},
		{"override", exchange.Filter{Override: "orders/flaky"}},
		{"method", exchange.Filter{Method: "post"}},
		{"path", exchange.Filter{Path: "/orders/"}},
		{"status range", exchange.Filter{StatusMin: 500, StatusMax: 599}},
		{"minimum status", exchange.Filter{StatusMin: 404}},
		{"intervened", exchange.Filter{Intervened: boolPtr(true)}},
		{"not intervened", exchange.Filter{Intervened: boolPtr(false)}},
		{"time window", exchange.Filter{Since: base.Add(10 * time.Second), Until: base.Add(20 * time.Second)}},
	}
	for _, c := range cases {
		expectFilter(t, s, all, c.name, c.flt)
	}
}

func combinedFilters(t TB, f Factory) {
	s := f.New(t)
	all := recordN(t, s, 120)
	flt := exchange.Filter{Route: "payments", StatusMin: 500, StatusMax: 599, Intervened: boolPtr(false)}
	expectFilter(t, s, all, "combined", flt)
	for _, e := range all {
		if flt.Match(e) && (e.Route != "payments" || e.Status < 500 || e.Intervened()) {
			t.Fatalf("the reference semantics are not conjunctive")
		}
	}
}

func filteredPagination(t TB, f Factory) {
	s := f.New(t)
	all := recordN(t, s, 150)
	flt := exchange.Filter{Route: "users"}
	var want, got []string
	for i := len(all) - 1; i >= 0; i-- {
		if flt.Match(all[i]) {
			want = append(want, all[i].ID)
		}
	}
	cursor := ""
	for range 20 {
		res, err := s.List(ctx, flt, store.Page{Limit: 7, Cursor: cursor})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		got = append(got, ids(res.Items)...)
		if cursor = res.Next; cursor == "" {
			break
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered pagination: got %v, want %v", got, want)
	}
}

func neighborWalk(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 5)
	e, err := s.Neighbor(ctx, "ID000002", store.Newer, exchange.Filter{})
	if err != nil || e.ID != "ID000003" {
		t.Fatalf("the one after 2 should be 3: %s %v", e.ID, err)
	}
	if e.Response.Body == nil {
		t.Fatalf("navigation should return the full exchange")
	}
	e, err = s.Neighbor(ctx, "ID000002", store.Older, exchange.Filter{})
	if err != nil || e.ID != "ID000001" {
		t.Fatalf("the one before 2 should be 1: %s %v", e.ID, err)
	}
	if _, err := s.Neighbor(ctx, "ID000004", store.Newer, exchange.Filter{}); !errors.Is(err, store.ErrNoMore) {
		t.Fatalf("past the newest one: want ErrNoMore, got %v", err)
	}
	if _, err := s.Neighbor(ctx, "ID000000", store.Older, exchange.Filter{}); !errors.Is(err, store.ErrNoMore) {
		t.Fatalf("before the oldest one: want ErrNoMore, got %v", err)
	}
}

func neighborRespectsFilter(t TB, f Factory) {
	s := f.New(t)
	all := recordN(t, s, 40)
	flt := exchange.Filter{StatusMin: 500, StatusMax: 599}
	var want []string
	for _, e := range all {
		if flt.Match(e) {
			want = append(want, e.ID)
		}
	}
	// Starts from an exchange outside the filter and walks forward.
	var got []string
	id := "ID000000"
	for range 100 {
		e, err := s.Neighbor(ctx, id, store.Newer, flt)
		if errors.Is(err, store.ErrNoMore) {
			break
		}
		if err != nil {
			t.Fatalf("Neighbor: %v", err)
		}
		got = append(got, e.ID)
		id = e.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filtered navigation: got %v, want %v", got, want)
	}
}

func neighborMissing(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 3)
	if _, err := s.Neighbor(ctx, "does-not-exist", store.Newer, exchange.Filter{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func clearThenRecord(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 10)
	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	res, err := s.List(ctx, exchange.Filter{}, store.Page{})
	if err != nil || len(res.Items) != 0 || res.Next != "" {
		t.Fatalf("the history should be empty: %d %q %v", len(res.Items), res.Next, err)
	}
	if _, err := s.Get(ctx, "ID000003"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a cleared exchange is still found: %v", err)
	}
	e := sample(77)
	if err := s.Record(ctx, e); err != nil {
		t.Fatalf("Record after Clear: %v", err)
	}
	res, err = s.List(ctx, exchange.Filter{}, store.Page{})
	if err != nil || len(res.Items) != 1 || res.Items[0].ID != e.ID {
		t.Fatalf("recording should work again after Clear: %v %v", ids(res.Items), err)
	}
}

func concurrentRecord(t TB, f Factory) {
	s := f.New(t)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 25 {
				e := sample(g*100 + i)
				if err := s.Record(ctx, e); err != nil {
					t.Errorf("concurrent Record: %v", err)
				}
			}
		})
	}
	wg.Wait()
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || len(res.Items) != 200 {
		t.Fatalf("want 200 exchanges, got %d (%v)", len(res.Items), err)
	}
	seen := map[string]bool{}
	for _, e := range res.Items {
		if seen[e.ID] {
			t.Fatalf("exchange %s duplicated", e.ID)
		}
		seen[e.ID] = true
	}
}

func survivesRestart(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 30)
	s = f.Reopen(t, s)
	res, err := s.List(ctx, exchange.Filter{Route: "payments"}, store.Page{Limit: 5})
	if err != nil || len(res.Items) != 5 || res.Items[0].ID != "ID000027" {
		t.Fatalf("query after restart: %v %v", ids(res.Items), err)
	}
	got, err := s.Get(ctx, "ID000004")
	if err != nil || !equalExchange(got, *sample(4)) {
		t.Fatalf("the exchange differs after the restart: %v", err)
	}
	if e, err := s.Neighbor(ctx, "ID000010", store.Newer, exchange.Filter{}); err != nil || e.ID != "ID000011" {
		t.Fatalf("navigation after restart: %s %v", e.ID, err)
	}
	// New records keep going after the old ones.
	if err := s.Record(ctx, sample(30)); err != nil {
		t.Fatalf("Record after restart: %v", err)
	}
	res, _ = s.List(ctx, exchange.Filter{}, store.Page{Limit: 1})
	if len(res.Items) != 1 || res.Items[0].ID != "ID000030" {
		t.Fatalf("the new exchange should be the most recent one: %v", ids(res.Items))
	}
}

// Recorder is a TB that only records failures, so the suite can be run
// against a wrong implementation to confirm that it gets caught.
type Recorder struct {
	dir      string
	mu       sync.Mutex
	failed   bool
	msgs     []string
	cleanups []func()
}

func (r *Recorder) Helper() {}
func (r *Recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failed = true
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}
func (r *Recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}
func (r *Recorder) TempDir() string   { return r.dir }
func (r *Recorder) Cleanup(fn func()) { r.cleanups = append(r.cleanups, fn) }

// RunCase runs one case with a Recorder and reports whether it failed.
func RunCase(c Case, f Factory, tempDir string) (failed bool, msg string) {
	r := &Recorder{dir: tempDir}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				r.Errorf("panic: %v", p)
			}
		}()
		c.Run(r, f)
	}()
	<-done
	for i := len(r.cleanups) - 1; i >= 0; i-- {
		r.cleanups[i]()
	}
	return r.failed, strings.Join(r.msgs, "; ")
}

// duplicateIDRejected: recording an ID that is already in the history again
// is rejected with ErrDuplicateID, leaving the history unchanged.
func duplicateIDRejected(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 3)
	again := sample(1)
	again.Status = 599
	if err := s.Record(ctx, again); !errors.Is(err, store.ErrDuplicateID) {
		t.Fatalf("repeated ID: want ErrDuplicateID, got %v", err)
	}
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || !reflect.DeepEqual(ids(res.Items), []string{"ID000002", "ID000001", "ID000000"}) {
		t.Fatalf("the history should not change on a rejection: %v %v", ids(res.Items), err)
	}
	got, err := s.Get(ctx, "ID000001")
	if err != nil || !equalExchange(got, *sample(1)) {
		t.Fatalf("the original exchange should stay intact: %+v %v", got, err)
	}
}

// staleCursor asks for the page after a cursor issued before the clear and
// requires it to come back empty, without reaching the new exchanges.
func staleCursor(t TB, s store.Store, cursor string) {
	t.Helper()
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Cursor: cursor})
	if err != nil || len(res.Items) != 0 || res.Next != "" {
		t.Fatalf("a cursor from before the clear should not reach new exchanges: %v %q %v", ids(res.Items), res.Next, err)
	}
}

// firstPageCursor records four exchanges and returns the cursor of a
// one-item page.
func firstPageCursor(t TB, s store.Store) string {
	t.Helper()
	recordN(t, s, 4)
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: 1})
	if err != nil || res.Next == "" {
		t.Fatalf("the first page should signal a continuation: %q %v", res.Next, err)
	}
	return res.Next
}

// recordFresh records new exchanges, with IDs outside sample(0..3).
func recordFresh(t TB, s store.Store) {
	t.Helper()
	for i := 100; i < 105; i++ {
		if err := s.Record(ctx, sample(i)); err != nil {
			t.Fatalf("Record(%d): %v", i, err)
		}
	}
}

func cursorBeforeClear(t TB, f Factory) {
	s := f.New(t)
	cursor := firstPageCursor(t, s)
	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	recordFresh(t, s)
	staleCursor(t, s, cursor)
}

func cursorBeforeClearSurvivesRestart(t TB, f Factory) {
	s := f.New(t)
	cursor := firstPageCursor(t, s)
	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	s = f.Reopen(t, s)
	recordFresh(t, s)
	staleCursor(t, s, cursor)
	// A second clear, also followed by a restart, still holds.
	cursor2, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: 1})
	if err != nil || cursor2.Next == "" {
		t.Fatalf("the page after the restart should signal a continuation: %q %v", cursor2.Next, err)
	}
	if err := s.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	s = f.Reopen(t, s)
	recordN(t, s, 8)
	staleCursor(t, s, cursor2.Next)
	staleCursor(t, s, cursor)
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || len(res.Items) != 8 || res.Items[0].ID != "ID000007" {
		t.Fatalf("after a clear and a restart the history should hold only the 8 new ones: %v %v", ids(res.Items), err)
	}
}

// outOfOrder records exchanges in the order they would finish, which is not
// the order they arrived in: the slow one arrived first and finished last;
// two arrived at the same instant and only the sequence separates them; two
// tie on both instant and sequence (different processes) and fall back to
// record order. It returns the IDs in the expected chronological order,
// newest to oldest.
func outOfOrder(t TB, s store.Store) []string {
	t.Helper()
	at := func(ms int, seq uint64, id string) *exchange.Exchange {
		e := sample(int(seq))
		e.ID, e.Seq, e.Start = id, seq, base.Add(time.Duration(ms)*time.Millisecond)
		return e
	}
	finished := []*exchange.Exchange{
		at(50, 2, "fast"),
		at(80, 4, "seq-tie-b"),
		at(80, 3, "seq-tie-a"),
		at(0, 1, "slow"),
		at(90, 5, "full-tie-1"),
		at(90, 5, "full-tie-2"),
		at(120, 6, "last"),
	}
	for _, e := range finished {
		if err := s.Record(ctx, e); err != nil {
			t.Fatalf("Record(%s): %v", e.ID, err)
		}
	}
	return []string{"last", "full-tie-2", "full-tie-1", "seq-tie-b", "seq-tie-a", "fast", "slow"}
}

// checkOrder checks listing, pagination and navigation against want, newest
// to oldest.
func checkOrder(t TB, s store.Store, want []string) {
	t.Helper()
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || !reflect.DeepEqual(ids(res.Items), want) {
		t.Fatalf("the listing should follow arrival order: got %v, want %v (%v)", ids(res.Items), want, err)
	}
	var paged []string
	cursor := ""
	for range len(want) + 1 {
		res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		paged = append(paged, ids(res.Items)...)
		if cursor = res.Next; cursor == "" {
			break
		}
	}
	if !reflect.DeepEqual(paged, want) {
		t.Fatalf("pagination should follow arrival order: got %v, want %v", paged, want)
	}
	for i, id := range want {
		older, err := s.Neighbor(ctx, id, store.Older, exchange.Filter{})
		if i == len(want)-1 {
			if !errors.Is(err, store.ErrNoMore) {
				t.Fatalf("there should be no exchange before %s: %s %v", id, older.ID, err)
			}
		} else if err != nil || older.ID != want[i+1] {
			t.Fatalf("the one before %s should be %s: %s %v", id, want[i+1], older.ID, err)
		}
		newer, err := s.Neighbor(ctx, id, store.Newer, exchange.Filter{})
		if i == 0 {
			if !errors.Is(err, store.ErrNoMore) {
				t.Fatalf("there should be no exchange after %s: %s %v", id, newer.ID, err)
			}
		} else if err != nil || newer.ID != want[i-1] {
			t.Fatalf("the one after %s should be %s: %s %v", id, want[i-1], newer.ID, err)
		}
	}
}

// chronologicalOrder: history order is arrival order (start, then sequence),
// not record order, which follows when the exchanges finish.
func chronologicalOrder(t TB, f Factory) {
	s := f.New(t)
	checkOrder(t, s, outOfOrder(t, s))
}

func chronologicalOrderSurvivesRestart(t TB, f Factory) {
	s := f.New(t)
	want := outOfOrder(t, s)
	// A cursor issued before the restart still holds after it.
	first, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: 3})
	if err != nil || first.Next == "" {
		t.Fatalf("the first page should signal a continuation: %q %v", first.Next, err)
	}
	s = f.Reopen(t, s)
	checkOrder(t, s, want)
	rest, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit, Cursor: first.Next})
	if err != nil || !reflect.DeepEqual(ids(rest.Items), want[3:]) {
		t.Fatalf("cursor from before the restart: got %v, want %v (%v)", ids(rest.Items), want[3:], err)
	}
}
