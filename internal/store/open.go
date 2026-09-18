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

// Reopens informa se a passagem da configuração old para next exige abrir
// outro backend do histórico: o backend mudou, a capacidade do backend em
// memória mudou, ou o arquivo de um backend persistente mudou.
func Reopens(old, next config.Settings) bool {
	if BackendName(old) != BackendName(next) {
		return true
	}
	if BackendName(next) == config.BackendMemory {
		return old.HistoryCapacity != next.HistoryCapacity
	}
	return old.HistoryPath != next.HistoryPath
}
