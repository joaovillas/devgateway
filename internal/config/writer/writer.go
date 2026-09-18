// Package writer grava documentos de rota em disco e aplica o resultado a
// quente. Toda escrita — da API de administração ou do modo aprendizado —
// passa por um único Writer: sob um mutex de escrita, relê o documento,
// aplica a alteração, valida o conjunto de rotas, grava o documento de forma
// atômica (arquivo temporário e rename) e troca o snapshot em vigor. Os
// demais documentos não são tocados.
package writer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"

	"github.com/gamerjp64/gateway/internal/config"
)

var (
	// ErrNotFound informa que a rota não existe na configuração em vigor.
	ErrNotFound = errors.New("rota não encontrada")
	// ErrStale informa que o documento em disco não está na versão esperada.
	ErrStale = errors.New("o documento mudou desde a versão informada")
)

// Causas de uma alteração aplicada, como o fluxo de eventos as nomeia.
const (
	CauseAPI      = "api"
	CauseReload   = "reload"
	CauseLearning = "learning"
)

// Change descreve uma alteração aplicada.
type Change struct {
	Cause string
	// Routes são as rotas criadas, alteradas ou removidas, pelo nome.
	Routes []string
	// Settings são as chaves da configuração do processo que mudaram.
	Settings []string
}

// Result descreve o documento depois de uma escrita.
type Result struct {
	// Changed informa se o documento foi gravado (ou removido).
	Changed bool
	// Created informa que o documento não existia e foi criado.
	Created bool
	// Snapshot é o snapshot publicado pela escrita, ou o em vigor quando nada
	// mudou.
	Snapshot *config.Snapshot
	// Route é o nome da rota depois da escrita, que difere do pedido numa
	// renomeação.
	Route string
	// File é o documento da rota.
	File string
	// Version é a versão do documento depois da escrita (ver Version); vazia
	// numa remoção.
	Version string
}

// Writer serializa as escritas de configuração.
type Writer struct {
	live *config.Live

	mu sync.Mutex
	// files exclui as leituras de documento feitas fora do mutex de escrita
	// (pela API) durante a substituição do arquivo: no Windows um arquivo
	// aberto para leitura não pode ser substituído nem removido.
	files sync.RWMutex

	hooksMu sync.Mutex
	hooks   []func(Change)
}

// New grava sobre a configuração em vigor em live.
func New(live *config.Live) *Writer { return &Writer{live: live} }

// OnChange registra fn para ser chamada depois de cada alteração aplicada,
// fora do mutex de escrita.
func (w *Writer) OnChange(fn func(Change)) {
	w.hooksMu.Lock()
	w.hooks = append(w.hooks, fn)
	w.hooksMu.Unlock()
}

func (w *Writer) notify(c Change) {
	w.hooksMu.Lock()
	hooks := slices.Clone(w.hooks)
	w.hooksMu.Unlock()
	for _, fn := range hooks {
		fn(c)
	}
}

// Exclusive executa fn sob o mutex de escrita, para operações que precisam
// excluir as escritas de documento, como a recarga do disco.
func (w *Writer) Exclusive(fn func() error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return fn()
}

// ReadDocument lê um documento de rota sem disputar o arquivo com a
// substituição feita por uma escrita em curso.
func (w *Writer) ReadDocument(file string) ([]byte, error) {
	w.files.RLock()
	defer w.files.RUnlock()
	return os.ReadFile(file)
}

// RouteUpdate é uma alteração num documento de rota.
type RouteUpdate struct {
	// Route é o nome da rota na configuração em vigor.
	Route string
	// IfMatch, quando informado, exige que o documento em disco esteja nessa
	// versão (ver Version); senão a escrita falha com ErrStale.
	IfMatch string
	// Cause é a causa informada aos observadores (CauseAPI, CauseLearning).
	Cause string
	// Apply altera o documento lido do disco e informa se houve mudança. Sem
	// mudança, nada é gravado. Apply pode mudar o nome da rota, o que a
	// renomeia; o documento continua no mesmo arquivo.
	Apply func(*config.Route) (bool, error)
}

// UpdateRoute aplica u ao documento da rota e informa se ele foi gravado.
// Ver Update.
func (w *Writer) UpdateRoute(u RouteUpdate) (bool, error) {
	res, err := w.Update(u)
	return res.Changed, err
}

// Update aplica u ao documento da rota. O documento é relido do disco sob o
// mutex, para que a alteração parta da versão gravada e não apague o que
// outra escrita acabou de gravar. A configuração resultante é validada por
// inteiro antes de qualquer gravação: se é inválida, nada é gravado e a
// configuração em vigor continua.
func (w *Writer) Update(u RouteUpdate) (Result, error) {
	w.mu.Lock()
	res, err := w.update(u)
	w.mu.Unlock()
	if res.Changed {
		w.notify(Change{Cause: u.Cause, Routes: names(u.Route, res.Route)})
	}
	return res, err
}

func (w *Writer) update(u RouteUpdate) (Result, error) {
	snap := w.live.Load()
	cur, data, err := current(snap, u.Route, u.IfMatch)
	if err != nil {
		return Result{}, err
	}
	doc, err := config.ParseRoute(cur.File, data)
	if err != nil {
		return Result{}, err
	}
	changed, err := u.Apply(&doc)
	if err != nil {
		return Result{}, err
	}
	if !changed {
		return Result{Snapshot: snap, Route: u.Route, File: cur.File, Version: Version(data)}, nil
	}
	out, err := config.MarshalRoute(doc)
	if err != nil {
		return Result{}, err
	}
	rd := config.NewRouteDoc(cur.File, doc)
	next, err := w.commit(snap, cur, &rd, out)
	if err != nil {
		return Result{}, err
	}
	return Result{Changed: true, Snapshot: next, Route: doc.Name, File: cur.File, Version: Version(out)}, nil
}

// current devolve a rota em vigor e o texto do seu documento em disco,
// conferindo a versão esperada.
func current(snap *config.Snapshot, route, ifMatch string) (*config.CompiledRoute, []byte, error) {
	cur := snap.Route(route)
	if cur == nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, route)
	}
	data, err := os.ReadFile(cur.File)
	if err != nil {
		return nil, nil, fmt.Errorf("lendo o documento da rota %s: %w", route, err)
	}
	if ifMatch != "" && Version(data) != ifMatch {
		return nil, nil, ErrStale
	}
	return cur, data, nil
}

// commit valida o conjunto de rotas com doc no lugar de replace (acrescentado,
// quando replace é nil; sem replace, quando doc é nil), grava out no arquivo
// de doc (ou remove o de replace) e publica o snapshot novo. Nada é gravado
// se a validação falha.
func (w *Writer) commit(snap *config.Snapshot, replace *config.CompiledRoute, doc *config.RouteDoc, out []byte) (*config.Snapshot, error) {
	docs := make([]config.RouteDoc, 0, len(snap.Routes)+1)
	for _, r := range snap.Routes {
		if r == replace {
			if doc != nil {
				docs = append(docs, *doc)
			}
			continue
		}
		docs = append(docs, config.NewRouteDoc(r.File, r.Doc))
	}
	if replace == nil && doc != nil {
		docs = append(docs, *doc)
	}
	routes, err := config.BuildRoutes(docs)
	if err != nil {
		return nil, err
	}
	w.files.Lock()
	defer w.files.Unlock()
	if doc == nil {
		if err := remove(replace.File); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("removendo o documento da rota %s: %w", replace.Name(), err)
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(doc.File), 0o755); err != nil {
			return nil, fmt.Errorf("criando o diretório de rotas: %w", err)
		}
		if err := WriteFileAtomic(doc.File, out); err != nil {
			return nil, fmt.Errorf("gravando o documento da rota %s: %w", doc.Route.Name, err)
		}
	}
	next := config.NewSnapshot(snap.Settings, routes, snap.Warnings)
	w.live.Swap(next)
	return next, nil
}

// CreateRoute cria a rota r num documento novo, {nome}.yaml no diretório de
// rotas em vigor. Um nome já usado por outra rota é recusado como conflito.
// Se {nome}.yaml é o documento de outra rota (que o manteve ao ser
// renomeada), a rota vai para o primeiro {nome}-N.yaml livre; se o arquivo
// existe mas não é de rota nenhuma, a criação também é recusada.
func (w *Writer) CreateRoute(r config.Route, cause string) (Result, error) {
	w.mu.Lock()
	res, err := w.create(r.Name, func(file string) (config.RouteDoc, []byte, error) {
		out, err := config.MarshalRoute(r)
		if err != nil {
			return config.RouteDoc{}, nil, err
		}
		return config.NewRouteDoc(file, r), out, nil
	})
	w.mu.Unlock()
	if res.Changed {
		w.notify(Change{Cause: cause, Routes: []string{res.Route}})
	}
	return res, err
}

// fileOwner é a rota em vigor cujo documento é o arquivo dado, ou nil.
func fileOwner(snap *config.Snapshot, st os.FileInfo) *config.CompiledRoute {
	for _, r := range snap.Routes {
		if rs, err := os.Stat(r.File); err == nil && os.SameFile(rs, st) {
			return r
		}
	}
	return nil
}

// freeFile é o primeiro {nome}-N.yaml, a partir de N = 2, que ainda não
// existe no diretório de rotas. Um arquivo existente nunca é sobrescrito,
// seja ou não o documento de uma rota.
func freeFile(dir, name string) string {
	for n := 2; ; n++ {
		file := filepath.Join(dir, fmt.Sprintf("%s-%d.yaml", name, n))
		if _, err := os.Stat(file); err != nil {
			return file
		}
	}
}

func (w *Writer) create(name string, build func(file string) (config.RouteDoc, []byte, error)) (Result, error) {
	snap := w.live.Load()
	file := filepath.Join(snap.Settings.RoutesDir, name+".yaml")
	if cur := snap.Route(name); cur != nil {
		return Result{}, config.Errors{{File: cur.File, Field: "name", Conflict: true,
			Msg: fmt.Sprintf("rota %q já declarada em %s", name, cur.File)}}
	}
	doc, out, err := build(file)
	if err != nil {
		return Result{}, err
	}
	if doc.Route.Name != name {
		return Result{}, config.Errors{doc.Locate("name",
			fmt.Sprintf("o nome declarado %q difere do nome da rota %q", doc.Route.Name, name))}
	}
	// A validação vem antes de usar o nome como caminho de arquivo.
	if _, err := config.BuildRoutes([]config.RouteDoc{doc}); err != nil {
		return Result{}, err
	}
	if st, err := os.Stat(file); err == nil {
		if fileOwner(snap, st) == nil {
			return Result{}, config.Errors{{File: file, Field: "name", Conflict: true,
				Msg: fmt.Sprintf("o arquivo %s já existe e não corresponde a nenhuma rota em vigor; recarregue a configuração ou escolha outro nome", file)}}
		}
		// O arquivo é o documento de outra rota, que o manteve ao ser
		// renomeada: a rota nova vai para o primeiro {nome}-N.yaml livre.
		file = freeFile(snap.Settings.RoutesDir, name)
		if doc, out, err = build(file); err != nil {
			return Result{}, err
		}
	}
	next, err := w.commit(snap, nil, &doc, out)
	if err != nil {
		return Result{}, err
	}
	return Result{Changed: true, Created: true, Snapshot: next, Route: name, File: file, Version: Version(out)}, nil
}

// DocumentWrite grava o texto de um documento de rota como enviado,
// comentários incluídos.
type DocumentWrite struct {
	// Route é o nome da rota. O documento precisa declarar esse mesmo nome.
	// Se a rota não existe, é criada.
	Route string
	Data  []byte
	// IfMatch, quando informado, exige que o documento em disco esteja nessa
	// versão; uma rota inexistente não confere com nenhuma versão.
	IfMatch string
	Cause   string
}

// WriteDocument valida o texto como na carga do disco, com erros apontando
// linha e coluna, e o grava sem reserializar.
func (w *Writer) WriteDocument(d DocumentWrite) (Result, error) {
	w.mu.Lock()
	res, err := w.writeDocument(d)
	w.mu.Unlock()
	if res.Changed {
		w.notify(Change{Cause: d.Cause, Routes: []string{d.Route}})
	}
	return res, err
}

func (w *Writer) writeDocument(d DocumentWrite) (Result, error) {
	snap := w.live.Load()
	if snap.Route(d.Route) == nil {
		if d.IfMatch != "" {
			return Result{}, ErrStale
		}
		return w.create(d.Route, func(file string) (config.RouteDoc, []byte, error) {
			doc, err := config.ParseRouteDoc(file, d.Data)
			return doc, d.Data, err
		})
	}
	cur, data, err := current(snap, d.Route, d.IfMatch)
	if err != nil {
		return Result{}, err
	}
	doc, err := config.ParseRouteDoc(cur.File, d.Data)
	if err != nil {
		return Result{}, err
	}
	if doc.Route.Name != d.Route {
		return Result{}, config.Errors{doc.Locate("name",
			fmt.Sprintf("o nome declarado %q difere do nome da rota %q", doc.Route.Name, d.Route))}
	}
	if bytes.Equal(data, d.Data) {
		return Result{Snapshot: snap, Route: d.Route, File: cur.File, Version: Version(data)}, nil
	}
	next, err := w.commit(snap, cur, &doc, d.Data)
	if err != nil {
		return Result{}, err
	}
	return Result{Changed: true, Snapshot: next, Route: d.Route, File: cur.File, Version: Version(d.Data)}, nil
}

// DeleteRoute remove a rota e apaga o seu documento.
func (w *Writer) DeleteRoute(route, ifMatch, cause string) (Result, error) {
	w.mu.Lock()
	res, err := w.deleteRoute(route, ifMatch)
	w.mu.Unlock()
	if res.Changed {
		w.notify(Change{Cause: cause, Routes: []string{route}})
	}
	return res, err
}

func (w *Writer) deleteRoute(route, ifMatch string) (Result, error) {
	snap := w.live.Load()
	cur, _, err := current(snap, route, ifMatch)
	if err != nil {
		return Result{}, err
	}
	next, err := w.commit(snap, cur, nil, nil)
	if err != nil {
		return Result{}, err
	}
	return Result{Changed: true, Snapshot: next, Route: route, File: cur.File}, nil
}

// Reload constrói uma configuração nova com load e, se ela é válida, chama
// apply com a anterior e a nova, para aplicar o que vive fora do snapshot
// (portas, backend do histórico). Só se apply conclui o snapshot novo é
// publicado; qualquer falha preserva a configuração em vigor. Requisições em
// curso seguem com o snapshot que capturaram. Roda sob o mutex de escrita,
// para não intercalar com a gravação de um documento.
func (w *Writer) Reload(load func() (*config.Snapshot, error), apply func(old, next *config.Snapshot) error) (Change, *config.Snapshot, error) {
	return w.Replace(CauseReload, load, apply)
}

// Replace é Reload com a causa dada, para quem troca a configuração inteira
// por outro motivo, como a alteração de gateway.json pela API. load roda sob
// o mutex de escrita, de modo que pode ler e alterar arquivos sem intercalar
// com outra escrita. Fora da recarga, uma troca que não mudou nada não é
// informada aos observadores.
func (w *Writer) Replace(cause string, load func() (*config.Snapshot, error), apply func(old, next *config.Snapshot) error) (Change, *config.Snapshot, error) {
	w.mu.Lock()
	old := w.live.Load()
	next, err := load()
	if err == nil && apply != nil {
		err = apply(old, next)
	}
	if err != nil {
		w.mu.Unlock()
		return Change{}, nil, err
	}
	w.live.Swap(next)
	w.mu.Unlock()
	c := Change{Cause: cause, Routes: diffRoutes(old, next), Settings: DiffSettings(old.Settings, next.Settings)}
	if cause == CauseReload || len(c.Routes) > 0 || len(c.Settings) > 0 {
		w.notify(c)
	}
	return c, next, nil
}

// diffRoutes nomeia as rotas criadas, removidas ou alteradas entre os dois
// snapshots, em ordem alfabética.
func diffRoutes(old, next *config.Snapshot) []string {
	out := []string{}
	for _, r := range next.Routes {
		prev := old.Route(r.Name())
		if prev == nil || prev.File != r.File || !reflect.DeepEqual(prev.Doc, r.Doc) {
			out = append(out, r.Name())
		}
	}
	for _, r := range old.Routes {
		if next.Route(r.Name()) == nil {
			out = append(out, r.Name())
		}
	}
	slices.Sort(out)
	return out
}

// DiffSettings nomeia as chaves da configuração do processo cujo valor ou
// origem mudou, na ordem de Settings.Effective.
func DiffSettings(old, next config.Settings) []string {
	out := []string{}
	a, b := old.Effective(), next.Effective()
	for i := range a {
		if !reflect.DeepEqual(a[i].Value, b[i].Value) || a[i].Source != b[i].Source {
			out = append(out, a[i].Key)
		}
	}
	return out
}

func names(before, after string) []string {
	if after == "" || after == before {
		return []string{before}
	}
	return []string{before, after}
}

// Version é a versão opaca de um documento, usada como ETag.
func Version(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// WriteFileAtomic grava data em path por meio de um arquivo temporário no
// mesmo diretório, renomeado sobre o destino: quem lê o arquivo vê o
// conteúdo anterior ou o novo, nunca uma gravação pela metade. As permissões
// de um arquivo existente são preservadas.
func WriteFileAtomic(path string, data []byte) error {
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
