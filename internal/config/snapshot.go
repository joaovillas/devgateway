package config

import (
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Snapshot is the merged, immutable configuration: process settings, compiled
// routes and the warnings from the load. It is never changed once published;
// a reload builds another snapshot and swaps the pointer.
type Snapshot struct {
	Settings Settings
	// Routes in precedence order: the first one that matches wins.
	Routes   []*CompiledRoute
	Warnings []string
	LoadedAt time.Time

	byName map[string]*CompiledRoute
}

// NewSnapshot builds the snapshot and its indexes from already compiled routes.
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

// Route returns the route with the given name, or nil.
func (s *Snapshot) Route(name string) *CompiledRoute { return s.byName[name] }

// Loader reads gateway.json, the environment and the routes directory.
type Loader struct {
	ConfigPath string
	Getenv     func(string) (string, bool)
}

// DefaultLoader uses GATEWAY_CONFIG (or ./gateway.json) and the process
// environment.
func DefaultLoader() Loader {
	path, ok := os.LookupEnv(EnvConfigPath)
	if !ok || path == "" {
		path = "gateway.json"
	}
	return Loader{ConfigPath: path, Getenv: os.LookupEnv}
}

// Load builds a complete snapshot, or fails without any side effect.
func (l Loader) Load() (*Snapshot, error) {
	s, warnings, err := LoadSettings(l.ConfigPath, l.Getenv)
	if err != nil {
		return nil, err
	}
	return l.withRoutes(s, warnings)
}

// LoadWith builds a snapshot like Load, but with data standing in for the
// content of gateway.json, without writing it. The routes directory is still
// read from disk.
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

// Live holds the snapshot in force. Reads take no lock: each request grabs
// the pointer once on the way in and keeps it to the end, so a concurrent
// swap is never observed half applied.
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

// Swap publishes a new snapshot and returns the previous one. Subscribers are
// notified after the swap, on the goroutine that swapped.
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

// Subscribe registers fn to be called on every snapshot swap, with the
// snapshot just published. It serves whoever keeps state derived from the
// configuration outside the snapshot, such as the live override state. fn
// must not swap the snapshot and must not block.
func (l *Live) Subscribe(fn func(*Snapshot)) {
	l.mu.Lock()
	l.subs = append(l.subs, fn)
	l.mu.Unlock()
}
