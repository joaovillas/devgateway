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
	"slices"
	"sync"

	"github.com/gamerjp64/devgateway/internal/exchange"
)

// NDJSON writes one exchange per line to an append-only file, in the order
// the exchanges finish. An in-memory index, in ascending key (orderKey)
// order, holds the position of each line and the fields used by the filters,
// so that querying never has to reread the file and reading one exchange
// takes a single positioned read.
type NDJSON struct {
	mu    sync.RWMutex
	path  string
	f     *os.File
	size  int64
	index []ndEntry
	byID  map[string]orderKey
	// lines counts the exchanges read or written since the file was opened:
	// it is the record position of the next one, the same one it will get
	// when the file is reopened.
	lines uint64
	// base is the history epoch: it is bumped on every clear, so that a
	// cursor issued before it cannot reach the new exchanges. It is written
	// on the first line of the cleared file (baseLine) and survives restarts.
	base uint64
}

// basePrefix marks the control line that holds the base of the order keys. A
// serialized exchange always starts with {"id":, so the two can never be
// confused.
var basePrefix = []byte(`{"_base":`)

type baseLine struct {
	Base uint64 `json:"_base"`
}

type ndEntry struct {
	key    orderKey
	offset int64
	length int
	meta   exchange.Exchange // no headers, no bodies
}

// OpenNDJSON opens (or creates) the file and rebuilds the index. An
// incomplete last line, left behind by an interrupted write, is dropped;
// unreadable lines in the middle of the file are skipped.
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
	s := &NDJSON{path: path, f: f, byID: map[string]orderKey{}}
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
				// Line without a terminator: interrupted write. Drop it.
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
			// A repeated ID (hand-edited file) keeps the first occurrence,
			// as if the second one had been rejected.
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

// search returns the position of the first exchange whose key is not before k.
func (s *NDJSON) search(k orderKey) int {
	i, _ := slices.BinarySearchFunc(s.index, k, func(e ndEntry, k orderKey) int { return e.key.compare(k) })
	return i
}

func (s *NDJSON) add(offset int64, length int, e *exchange.Exchange) {
	meta := e.Summary()
	meta.Request.Headers, meta.Response.Headers = nil, nil
	k := keyOf(e, s.lines)
	s.lines++
	s.byID[e.ID] = k
	s.index = slices.Insert(s.index, s.search(k), ndEntry{key: k, offset: offset, length: length, meta: meta})
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
		return fmt.Errorf("writing history to %s: %w", s.path, err)
	}
	s.add(s.size, len(line), e)
	s.size += int64(len(line))
	return nil
}

func (s *NDJSON) read(i int) (exchange.Exchange, error) {
	en := s.index[i]
	buf := make([]byte, en.length)
	if _, err := s.f.ReadAt(buf, en.offset); err != nil {
		return exchange.Exchange{}, fmt.Errorf("reading history from %s: %w", s.path, err)
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
		c, err := decodeCursor(p.Cursor)
		if err != nil {
			return ListResult{}, err
		}
		if c.Epoch != s.base {
			return ListResult{}, nil // cursor predates the last clear
		}
		// The cursor is the key of the last exchange delivered; the listing
		// resumes from the one before it.
		start = s.search(c.Key) - 1
	}
	var res ListResult
	var last orderKey
	for i := start; i >= 0; i-- {
		if !f.Match(&s.index[i].meta) {
			continue
		}
		if len(res.Items) == limit {
			// One more exchange matches: the page continues after the last
			// one delivered.
			res.Next = encodeCursor(s.base, last)
			break
		}
		e, err := s.read(i)
		if err != nil {
			return ListResult{}, err
		}
		res.Items = append(res.Items, e)
		last = s.index[i].key
	}
	return res, nil
}

func (s *NDJSON) Get(_ context.Context, id string) (exchange.Exchange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	if !ok {
		return exchange.Exchange{}, ErrNotFound
	}
	return s.read(s.search(k))
}

func (s *NDJSON) Neighbor(_ context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.byID[id]
	if !ok {
		return exchange.Exchange{}, ErrNotFound
	}
	i := s.search(k)
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
		return fmt.Errorf("clearing history at %s: %w", s.path, err)
	}
	s.base++
	s.lines = 0
	s.size = 0
	s.index = nil
	s.byID = map[string]orderKey{}
	// The new base opens the cleared file, so that a restart picks it up.
	line, err := json.Marshal(baseLine{Base: s.base})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := s.f.WriteAt(line, 0); err != nil {
		return fmt.Errorf("clearing history at %s: %w", s.path, err)
	}
	s.size = int64(len(line))
	return nil
}

// Path is the history file.
func (s *NDJSON) Path() string { return s.path }

func (s *NDJSON) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}
