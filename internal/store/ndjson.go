package store

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/gamerjp64/gateway/internal/exchange"
)

// NDJSON grava uma troca por linha num arquivo, só por acréscimo. Um índice
// em memória guarda a posição de cada linha e os campos usados nos filtros,
// de modo que consultar não exige reler o arquivo e ler uma troca exige uma
// única leitura posicionada.
type NDJSON struct {
	mu    sync.RWMutex
	path  string
	f     *os.File
	size  int64
	index []ndEntry
	byID  map[string]int
	// base desloca as chaves de ordem a cada limpeza, para que um cursor
	// emitido antes dela não aponte para trocas novas. Ela é gravada na
	// primeira linha do arquivo limpo (baseLine) e sobrevive ao reinício.
	base uint64
}

// basePrefix marca a linha de controle que guarda a base das chaves de
// ordem. Uma troca serializada sempre começa por {"id":, então as duas nunca
// se confundem.
var basePrefix = []byte(`{"_base":`)

type baseLine struct {
	Base uint64 `json:"_base"`
}

type ndEntry struct {
	offset int64
	length int
	meta   exchange.Exchange // sem cabeçalhos nem corpos
}

// OpenNDJSON abre (ou cria) o arquivo e reconstrói o índice. Uma última
// linha incompleta, deixada por uma interrupção no meio da escrita, é
// descartada; linhas ilegíveis no meio do arquivo são ignoradas.
func OpenNDJSON(path string) (*NDJSON, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	s := &NDJSON{path: path, f: f, byID: map[string]int{}}
	if err := s.load(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func (s *NDJSON) load() error {
	r := bufio.NewReaderSize(io.NewSectionReader(s.f, 0, 1<<62), 1<<16)
	var offset int64
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			if len(line) > 0 {
				// Linha sem terminador: escrita interrompida. Descarta.
				if err := s.f.Truncate(offset); err != nil {
					return err
				}
			}
			break
		}
		if err != nil {
			return err
		}
		if bytes.HasPrefix(line, basePrefix) {
			var b baseLine
			if json.Unmarshal(line, &b) == nil {
				s.base = b.Base
			}
		} else {
			// Um identificador repetido (arquivo editado à mão) fica com a
			// primeira ocorrência, como se a segunda tivesse sido recusada.
			var e exchange.Exchange
			if json.Unmarshal(line, &e) == nil {
				if _, dup := s.byID[e.ID]; !dup {
					s.add(offset, len(line), &e)
				}
			}
		}
		offset += int64(len(line))
	}
	s.size = offset
	return nil
}

func (s *NDJSON) add(offset int64, length int, e *exchange.Exchange) {
	meta := e.Summary()
	meta.Request.Headers, meta.Response.Headers = nil, nil
	s.byID[e.ID] = len(s.index)
	s.index = append(s.index, ndEntry{offset: offset, length: length, meta: meta})
}

func (s *NDJSON) Record(_ context.Context, e *exchange.Exchange) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[e.ID]; ok {
		return ErrDuplicateID
	}
	if _, err := s.f.WriteAt(line, s.size); err != nil {
		return fmt.Errorf("gravando histórico em %s: %w", s.path, err)
	}
	s.add(s.size, len(line), e)
	s.size += int64(len(line))
	return nil
}

func (s *NDJSON) read(i int) (exchange.Exchange, error) {
	en := s.index[i]
	buf := make([]byte, en.length)
	if _, err := s.f.ReadAt(buf, en.offset); err != nil {
		return exchange.Exchange{}, fmt.Errorf("lendo histórico em %s: %w", s.path, err)
	}
	var e exchange.Exchange
	if err := json.Unmarshal(bytes.TrimSuffix(buf, []byte("\n")), &e); err != nil {
		return exchange.Exchange{}, err
	}
	return e, nil
}

func (s *NDJSON) List(_ context.Context, f exchange.Filter, p Page) (ListResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	limit := NormalizeLimit(p.Limit)
	start := len(s.index) - 1
	if p.Cursor != "" {
		c, err := strconv.ParseUint(p.Cursor, 10, 64)
		if err != nil {
			return ListResult{}, ErrBadCursor
		}
		if c < s.base {
			return ListResult{}, nil // cursor anterior à última limpeza
		}
		start = min(int(c-s.base), len(s.index)) - 1
	}
	var res ListResult
	last := -1
	for i := start; i >= 0; i-- {
		if !f.Match(&s.index[i].meta) {
			continue
		}
		if len(res.Items) == limit {
			res.Next = strconv.FormatUint(s.base+uint64(last), 10)
			break
		}
		e, err := s.read(i)
		if err != nil {
			return ListResult{}, err
		}
		res.Items = append(res.Items, e)
		last = i
	}
	return res, nil
}

func (s *NDJSON) Get(_ context.Context, id string) (exchange.Exchange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.byID[id]
	if !ok {
		return exchange.Exchange{}, ErrNotFound
	}
	return s.read(i)
}

func (s *NDJSON) Neighbor(_ context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	i, ok := s.byID[id]
	if !ok {
		return exchange.Exchange{}, ErrNotFound
	}
	step := -1
	if d == Newer {
		step = 1
	}
	for j := i + step; j >= 0 && j < len(s.index); j += step {
		if f.Match(&s.index[j].meta) {
			return s.read(j)
		}
	}
	return exchange.Exchange{}, ErrNoMore
}

func (s *NDJSON) Clear(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.f.Truncate(0); err != nil {
		return fmt.Errorf("limpando histórico em %s: %w", s.path, err)
	}
	s.base += uint64(len(s.index))
	s.size = 0
	s.index = nil
	s.byID = map[string]int{}
	// A nova base abre o arquivo limpo, para que o reinício a recupere.
	line, err := json.Marshal(baseLine{Base: s.base})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := s.f.WriteAt(line, 0); err != nil {
		return fmt.Errorf("limpando histórico em %s: %w", s.path, err)
	}
	s.size = int64(len(line))
	return nil
}

// Path é o arquivo do histórico.
func (s *NDJSON) Path() string { return s.path }

func (s *NDJSON) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}
