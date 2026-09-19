package store

import (
	"fmt"

	"github.com/gamerjp64/devgateway/internal/config"
)

// Open initializes the history backend picked by the configuration, with
// memory as the default. A failure never falls back to another backend: the
// error names the backend and the cause, so that the process refuses to
// start (or the hot swap is rejected) instead of carrying on without
// persisting anything.
func Open(s config.Settings) (Store, error) {
	switch s.HistoryBackend {
	case "", config.BackendMemory:
		return NewMemory(s.HistoryCapacity), nil
	case config.BackendNDJSON:
		st, err := OpenNDJSON(s.HistoryPath)
		if err != nil {
			return nil, fmt.Errorf("history backend %s at %s: %w", s.HistoryBackend, s.HistoryPath, err)
		}
		return st, nil
	case config.BackendSQLite:
		st, err := OpenSQLite(s.HistoryPath)
		if err != nil {
			return nil, fmt.Errorf("history backend %s at %s: %w", s.HistoryBackend, s.HistoryPath, err)
		}
		return st, nil
	}
	return nil, fmt.Errorf("unknown history backend %q; use %s, %s or %s",
		s.HistoryBackend, config.BackendMemory, config.BackendNDJSON, config.BackendSQLite)
}

// BackendName returns the effective backend name, with memory standing in
// for the empty value.
func BackendName(s config.Settings) string {
	if s.HistoryBackend == "" {
		return config.BackendMemory
	}
	return s.HistoryBackend
}

// Reopens reports whether moving from configuration old to next requires
// opening another history backend: the backend changed, the in-memory
// backend's capacity changed, or a persistent backend's file changed.
func Reopens(old, next config.Settings) bool {
	if BackendName(old) != BackendName(next) {
		return true
	}
	if BackendName(next) == config.BackendMemory {
		return old.HistoryCapacity != next.HistoryCapacity
	}
	return old.HistoryPath != next.HistoryPath
}
