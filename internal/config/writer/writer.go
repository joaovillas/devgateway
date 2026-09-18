// Package writer grava documentos de rota em disco e aplica o resultado a
// quente. Toda escrita — da API de administração ou do modo aprendizado —
// passa por um único Writer: sob um mutex de escrita, relê o documento,
// aplica a alteração, valida o conjunto de rotas, grava o documento de forma
// atômica (arquivo temporário e rename) e troca o snapshot em vigor. Os
// demais documentos não são tocados.
package writer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	Cause  string
	Routes []string
}

// Writer serializa as escritas de configuração.
type Writer struct {
	live *config.Live

	mu sync.Mutex

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
	// mudança, nada é gravado.
	Apply func(*config.Route) (bool, error)
}

// UpdateRoute aplica u ao documento da rota. O documento é relido do disco
// sob o mutex, para que a alteração parta da versão gravada e não apague o
// que outra escrita acabou de gravar. A configuração resultante é validada
// por inteiro antes de qualquer gravação: se é inválida, nada é gravado e a
// configuração em vigor continua. Informa se o documento foi gravado.
func (w *Writer) UpdateRoute(u RouteUpdate) (bool, error) {
	w.mu.Lock()
	changed, err := w.updateRoute(u)
	w.mu.Unlock()
	if changed {
		w.notify(Change{Cause: u.Cause, Routes: []string{u.Route}})
	}
	return changed, err
}

func (w *Writer) updateRoute(u RouteUpdate) (bool, error) {
	snap := w.live.Load()
	cur := snap.Route(u.Route)
	if cur == nil {
		return false, fmt.Errorf("%w: %s", ErrNotFound, u.Route)
	}
	data, err := os.ReadFile(cur.File)
	if err != nil {
		return false, fmt.Errorf("lendo o documento da rota %s: %w", u.Route, err)
	}
	if u.IfMatch != "" && Version(data) != u.IfMatch {
		return false, ErrStale
	}
	doc, err := config.ParseRoute(cur.File, data)
	if err != nil {
		return false, err
	}
	changed, err := u.Apply(&doc)
	if err != nil || !changed {
		return false, err
	}
	docs := make([]config.RouteDoc, 0, len(snap.Routes))
	for _, r := range snap.Routes {
		if r == cur {
			docs = append(docs, config.NewRouteDoc(r.File, doc))
		} else {
			docs = append(docs, config.NewRouteDoc(r.File, r.Doc))
		}
	}
	routes, err := config.BuildRoutes(docs)
	if err != nil {
		return false, err
	}
	out, err := config.MarshalRoute(doc)
	if err != nil {
		return false, err
	}
	if err := WriteFileAtomic(cur.File, out); err != nil {
		return false, fmt.Errorf("gravando o documento da rota %s: %w", u.Route, err)
	}
	w.live.Swap(config.NewSnapshot(snap.Settings, routes, snap.Warnings))
	return true, nil
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
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
