package store

import (
	"fmt"

	"github.com/gamerjp64/gateway/internal/config"
)

// Open inicializa o backend do histórico escolhido pela configuração, com
// memória como padrão. Uma falha nunca cai para outro backend: o erro nomeia
// o backend e a causa, para que o processo recuse iniciar (ou a troca a
// quente seja recusada) em vez de seguir sem persistir nada.
func Open(s config.Settings) (Store, error) {
	switch s.HistoryBackend {
	case "", config.BackendMemory:
		return NewMemory(s.HistoryCapacity), nil
	case config.BackendNDJSON:
		st, err := OpenNDJSON(s.HistoryPath)
		if err != nil {
			return nil, fmt.Errorf("backend do histórico %s em %s: %w", s.HistoryBackend, s.HistoryPath, err)
		}
		return st, nil
	case config.BackendSQLite:
		st, err := OpenSQLite(s.HistoryPath)
		if err != nil {
			return nil, fmt.Errorf("backend do histórico %s em %s: %w", s.HistoryBackend, s.HistoryPath, err)
		}
		return st, nil
	}
	return nil, fmt.Errorf("backend do histórico %q desconhecido; use %s, %s ou %s",
		s.HistoryBackend, config.BackendMemory, config.BackendNDJSON, config.BackendSQLite)
}

// BackendName devolve o nome efetivo do backend, com memória no lugar do vazio.
func BackendName(s config.Settings) string {
	if s.HistoryBackend == "" {
		return config.BackendMemory
	}
	return s.HistoryBackend
}
