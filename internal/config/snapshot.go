package config

import (
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Snapshot é a configuração fundida e imutável: processo, rotas compiladas e
// os avisos da carga. Nunca é alterado depois de publicado; uma recarga
// constrói outro snapshot e troca o ponteiro.
type Snapshot struct {
	Settings Settings
	// Routes em ordem de precedência: a primeira que casa é a escolhida.
	Routes   []*CompiledRoute
	Warnings []string
	LoadedAt time.Time

	byName map[string]*CompiledRoute
}

// NewSnapshot monta o snapshot e seus índices a partir de rotas já compiladas.
func NewSnapshot(s Settings, routes []*CompiledRoute, warnings []string) *Snapshot {
	snap := &Snapshot{
		Settings: s,
		Routes:   routes,
		Warnings: warnings,
		LoadedAt: time.Now(),
		byName:   make(map[string]*CompiledRoute, len(routes)),
	}
	for _, r := range routes {
		snap.byName[r.Doc.Name] = r
	}
	return snap
}

// Route devolve a rota de nome dado, ou nil.
func (s *Snapshot) Route(name string) *CompiledRoute { return s.byName[name] }

// Loader lê gateway.json, o ambiente e o diretório de rotas.
type Loader struct {
	ConfigPath string
	Getenv     func(string) (string, bool)
}

// DefaultLoader usa GATEWAY_CONFIG (ou ./gateway.json) e o ambiente do processo.
func DefaultLoader() Loader {
	path, ok := os.LookupEnv(EnvConfigPath)
	if !ok || path == "" {
		path = "gateway.json"
	}
	return Loader{ConfigPath: path, Getenv: os.LookupEnv}
}

// Load constrói um snapshot completo, ou falha sem efeito colateral.
func (l Loader) Load() (*Snapshot, error) {
	s, warnings, err := LoadSettings(l.ConfigPath, l.Getenv)
	if err != nil {
		return nil, err
	}
	return l.withRoutes(s, warnings)
}

// LoadWith constrói um snapshot como Load, mas com data no lugar do conteúdo
// de gateway.json, sem gravá-lo. O diretório de rotas é lido do disco.
func (l Loader) LoadWith(data []byte) (*Snapshot, error) {
	s, warnings, err := SettingsFrom(l.ConfigPath, data, l.Getenv)
	if err != nil {
		return nil, err
	}
	return l.withRoutes(s, warnings)
}

func (l Loader) withRoutes(s Settings, warnings []string) (*Snapshot, error) {
	docs, w, err := ReadRoutesDir(s.RoutesDir)
	if err != nil {
		return nil, err
	}
	routes, err := BuildRoutes(docs)
	if err != nil {
		return nil, err
	}
	return NewSnapshot(s, routes, append(warnings, w...)), nil
}

// Live guarda o snapshot em vigor. Leituras não usam lock: cada requisição
// captura o ponteiro uma vez na entrada e segue com ele até o fim, de modo
// que uma troca concorrente nunca é observada pela metade.
type Live struct {
	p atomic.Pointer[Snapshot]

	mu   sync.Mutex
	subs []func(*Snapshot)
}

func NewLive(s *Snapshot) *Live {
	l := &Live{}
	l.p.Store(s)
	return l
}

func (l *Live) Load() *Snapshot { return l.p.Load() }

// Swap publica um snapshot novo e devolve o anterior. Os assinantes são
// avisados depois da troca, na goroutine de quem trocou.
func (l *Live) Swap(s *Snapshot) *Snapshot {
	old := l.p.Swap(s)
	l.mu.Lock()
	subs := slices.Clone(l.subs)
	l.mu.Unlock()
	for _, fn := range subs {
		fn(s)
	}
	return old
}

// Subscribe registra fn para ser chamada a cada troca de snapshot, com o
// snapshot publicado. Serve a quem guarda estado derivado da configuração
// fora do snapshot, como o estado vivo dos overrides. fn não deve trocar o
// snapshot nem bloquear.
func (l *Live) Subscribe(fn func(*Snapshot)) {
	l.mu.Lock()
	l.subs = append(l.subs, fn)
	l.mu.Unlock()
}
