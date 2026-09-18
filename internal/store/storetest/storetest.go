// Package storetest é a bateria de contrato que toda implementação de
// store.Store precisa passar.
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

	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
)

// TB é o subconjunto de testing.TB usado pela bateria, para que ela possa
// rodar contra um gravador e provar que detecta implementações erradas.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	TempDir() string
	Cleanup(func())
}

// Factory cria instâncias do backend sob teste.
type Factory struct {
	// New cria um store vazio.
	New func(t TB) store.Store
	// Reopen fecha o store e abre outro sobre os mesmos dados. Nil para
	// backends sem persistência.
	Reopen func(t TB, s store.Store) store.Store
}

// Case é um caso da bateria.
type Case struct {
	Name string
	Run  func(t TB, f Factory)
	// Persistent: o caso só se aplica a backends com Reopen.
	Persistent bool
}

// Cases lista a bateria completa.
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
	{Name: "SurvivesRestart", Run: survivesRestart, Persistent: true},
	{Name: "CursorBeforeClearSurvivesRestart", Run: cursorBeforeClearSurvivesRestart, Persistent: true},
}

// Run executa a bateria como subtestes de t.
func Run(t *testing.T, f Factory) {
	for _, c := range Cases {
		t.Run(c.Name, func(t *testing.T) {
			if c.Persistent && f.Reopen == nil {
				t.Skip("backend sem persistência")
			}
			c.Run(t, f)
		})
	}
}

var ctx = context.Background()

var base = time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

// sample cria a i-ésima troca de uma sequência com atributos variados.
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
	// Instantes comparados por valor; o fuso pode mudar na serialização.
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
		t.Fatalf("troca recuperada difere da registrada:\n%+v\n%+v", got, *want)
	}
	if !bytes.Equal(got.Request.Body, []byte{4, 0, 0xff, '\n'}) {
		t.Fatalf("corpo binário alterado: %v", got.Request.Body)
	}
}

func getMissing(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 3)
	if _, err := s.Get(ctx, "nao-existe"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("esperado ErrNotFound, recebido %v", err)
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
		t.Fatalf("primeira página deveria ter as 50 mais novas, da 149 à 100: %v", ids(res.Items))
	}
	if res.Next == "" {
		t.Fatalf("primeira página deveria indicar continuação")
	}
	var all []string
	all = append(all, ids(res.Items)...)
	for pages := 1; res.Next != ""; pages++ {
		if pages > 10 {
			t.Fatalf("paginação não termina")
		}
		res, err = s.List(ctx, exchange.Filter{}, store.Page{Limit: 50, Cursor: res.Next})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		all = append(all, ids(res.Items)...)
	}
	if len(all) != 150 {
		t.Fatalf("paginação deveria percorrer 150 trocas, percorreu %d", len(all))
	}
	for i, id := range all {
		if want := fmt.Sprintf("ID%06d", 149-i); id != want {
			t.Fatalf("posição %d: %s, esperado %s", i, id, want)
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
		t.Fatalf("sem limite, a página deveria ter %d itens e continuação: %d %q", store.DefaultLimit, len(res.Items), res.Next)
	}
	res, err = s.List(ctx, exchange.Filter{}, store.Page{Limit: 10})
	if err != nil || len(res.Items) != 10 {
		t.Fatalf("limite 10: %d itens, %v", len(res.Items), err)
	}
	res, _ = s.List(ctx, exchange.Filter{}, store.Page{Limit: store.DefaultLimit + 5})
	if len(res.Items) != store.DefaultLimit+5 || res.Next != "" {
		t.Fatalf("página que cobre tudo não deveria ter continuação: %d %q", len(res.Items), res.Next)
	}
}

// expectFilter compara o resultado do backend com a semântica de referência.
func expectFilter(t TB, s store.Store, all []*exchange.Exchange, name string, flt exchange.Filter) {
	t.Helper()
	var want []string
	for i := len(all) - 1; i >= 0; i-- {
		if flt.Match(all[i]) {
			want = append(want, all[i].ID)
		}
	}
	if len(want) == 0 {
		t.Fatalf("%s: filtro de teste não seleciona nada", name)
	}
	res, err := s.List(ctx, flt, store.Page{Limit: store.MaxLimit})
	if err != nil {
		t.Fatalf("%s: List: %v", name, err)
	}
	if got := ids(res.Items); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: recebido %v, esperado %v", name, got, want)
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
		{"rota", exchange.Filter{Route: "payments"}},
		{"upstream", exchange.Filter{Upstream: "http://users.local"}},
		{"override", exchange.Filter{Override: "orders/flaky"}},
		{"método", exchange.Filter{Method: "post"}},
		{"path", exchange.Filter{Path: "/orders/"}},
		{"faixa de status", exchange.Filter{StatusMin: 500, StatusMax: 599}},
		{"status mínimo", exchange.Filter{StatusMin: 404}},
		{"com intervenção", exchange.Filter{Intervened: boolPtr(true)}},
		{"sem intervenção", exchange.Filter{Intervened: boolPtr(false)}},
		{"janela de tempo", exchange.Filter{Since: base.Add(10 * time.Second), Until: base.Add(20 * time.Second)}},
	}
	for _, c := range cases {
		expectFilter(t, s, all, c.name, c.flt)
	}
}

func combinedFilters(t TB, f Factory) {
	s := f.New(t)
	all := recordN(t, s, 120)
	flt := exchange.Filter{Route: "payments", StatusMin: 500, StatusMax: 599, Intervened: boolPtr(false)}
	expectFilter(t, s, all, "combinado", flt)
	for _, e := range all {
		if flt.Match(e) && (e.Route != "payments" || e.Status < 500 || e.Intervened()) {
			t.Fatalf("semântica de referência não é conjuntiva")
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
		t.Fatalf("paginação filtrada: recebido %v, esperado %v", got, want)
	}
}

func neighborWalk(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 5)
	e, err := s.Neighbor(ctx, "ID000002", store.Newer, exchange.Filter{})
	if err != nil || e.ID != "ID000003" {
		t.Fatalf("seguinte de 2 deveria ser 3: %s %v", e.ID, err)
	}
	if e.Response.Body == nil {
		t.Fatalf("navegação deveria devolver a troca completa")
	}
	e, err = s.Neighbor(ctx, "ID000002", store.Older, exchange.Filter{})
	if err != nil || e.ID != "ID000001" {
		t.Fatalf("anterior de 2 deveria ser 1: %s %v", e.ID, err)
	}
	if _, err := s.Neighbor(ctx, "ID000004", store.Newer, exchange.Filter{}); !errors.Is(err, store.ErrNoMore) {
		t.Fatalf("depois da mais nova: esperado ErrNoMore, recebido %v", err)
	}
	if _, err := s.Neighbor(ctx, "ID000000", store.Older, exchange.Filter{}); !errors.Is(err, store.ErrNoMore) {
		t.Fatalf("antes da mais antiga: esperado ErrNoMore, recebido %v", err)
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
	// Parte de uma troca fora do filtro e percorre para frente.
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
		t.Fatalf("navegação filtrada: recebido %v, esperado %v", got, want)
	}
}

func neighborMissing(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 3)
	if _, err := s.Neighbor(ctx, "nao-existe", store.Newer, exchange.Filter{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("esperado ErrNotFound, recebido %v", err)
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
		t.Fatalf("histórico deveria estar vazio: %d %q %v", len(res.Items), res.Next, err)
	}
	if _, err := s.Get(ctx, "ID000003"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("troca limpa ainda encontrada: %v", err)
	}
	e := sample(77)
	if err := s.Record(ctx, e); err != nil {
		t.Fatalf("Record após Clear: %v", err)
	}
	res, err = s.List(ctx, exchange.Filter{}, store.Page{})
	if err != nil || len(res.Items) != 1 || res.Items[0].ID != e.ID {
		t.Fatalf("registro deveria voltar a funcionar após Clear: %v %v", ids(res.Items), err)
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
					t.Errorf("Record concorrente: %v", err)
				}
			}
		})
	}
	wg.Wait()
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || len(res.Items) != 200 {
		t.Fatalf("esperadas 200 trocas, recebidas %d (%v)", len(res.Items), err)
	}
	seen := map[string]bool{}
	for _, e := range res.Items {
		if seen[e.ID] {
			t.Fatalf("troca %s duplicada", e.ID)
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
		t.Fatalf("consulta após reinício: %v %v", ids(res.Items), err)
	}
	got, err := s.Get(ctx, "ID000004")
	if err != nil || !equalExchange(got, *sample(4)) {
		t.Fatalf("troca após reinício difere: %v", err)
	}
	if e, err := s.Neighbor(ctx, "ID000010", store.Newer, exchange.Filter{}); err != nil || e.ID != "ID000011" {
		t.Fatalf("navegação após reinício: %s %v", e.ID, err)
	}
	// Registros novos continuam depois dos antigos.
	if err := s.Record(ctx, sample(30)); err != nil {
		t.Fatalf("Record após reinício: %v", err)
	}
	res, _ = s.List(ctx, exchange.Filter{}, store.Page{Limit: 1})
	if len(res.Items) != 1 || res.Items[0].ID != "ID000030" {
		t.Fatalf("troca nova deveria ser a mais recente: %v", ids(res.Items))
	}
}

// Recorder é um TB que só registra falhas, para rodar a bateria contra uma
// implementação errada e confirmar que ela é detectada.
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

// RunCase executa um caso com um Recorder e informa se ele falhou.
func RunCase(c Case, f Factory, tempDir string) (failed bool, msg string) {
	r := &Recorder{dir: tempDir}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				r.Errorf("pânico: %v", p)
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

// duplicateIDRejected: registrar de novo um identificador que já consta do
// histórico é recusado com ErrDuplicateID, sem alterar o histórico.
func duplicateIDRejected(t TB, f Factory) {
	s := f.New(t)
	recordN(t, s, 3)
	again := sample(1)
	again.Status = 599
	if err := s.Record(ctx, again); !errors.Is(err, store.ErrDuplicateID) {
		t.Fatalf("identificador repetido: esperado ErrDuplicateID, recebido %v", err)
	}
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: store.MaxLimit})
	if err != nil || !reflect.DeepEqual(ids(res.Items), []string{"ID000002", "ID000001", "ID000000"}) {
		t.Fatalf("o histórico não deveria mudar com a recusa: %v %v", ids(res.Items), err)
	}
	got, err := s.Get(ctx, "ID000001")
	if err != nil || !equalExchange(got, *sample(1)) {
		t.Fatalf("a troca original deveria seguir intacta: %+v %v", got, err)
	}
}

// staleCursor pede a página seguinte a um cursor emitido antes da limpeza e
// exige que ela venha vazia, sem alcançar as trocas novas.
func staleCursor(t TB, s store.Store, cursor string) {
	t.Helper()
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Cursor: cursor})
	if err != nil || len(res.Items) != 0 || res.Next != "" {
		t.Fatalf("cursor anterior à limpeza não deveria alcançar trocas novas: %v %q %v", ids(res.Items), res.Next, err)
	}
}

// firstPageCursor registra quatro trocas e devolve o cursor da página de uma.
func firstPageCursor(t TB, s store.Store) string {
	t.Helper()
	recordN(t, s, 4)
	res, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: 1})
	if err != nil || res.Next == "" {
		t.Fatalf("primeira página deveria indicar continuação: %q %v", res.Next, err)
	}
	return res.Next
}

// recordFresh registra trocas novas, com identificadores fora de sample(0..3).
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
	// Uma segunda limpeza, também seguida de reinício, continua valendo.
	cursor2, err := s.List(ctx, exchange.Filter{}, store.Page{Limit: 1})
	if err != nil || cursor2.Next == "" {
		t.Fatalf("página após o reinício deveria indicar continuação: %q %v", cursor2.Next, err)
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
		t.Fatalf("o histórico após limpeza e reinício deveria ter só as 8 novas: %v %v", ids(res.Items), err)
	}
}
