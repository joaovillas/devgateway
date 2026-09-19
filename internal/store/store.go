// Package store defines the storage for the exchange history and its
// implementations: memory, NDJSON file and SQLite.
package store

import (
	"context"
	"errors"

	"github.com/gamerjp64/devgateway/internal/exchange"
)

var (
	// ErrNotFound: no exchange with the requested ID.
	ErrNotFound = errors.New("exchange not found")
	// ErrNoMore: navigation reached the end of the history in that direction.
	ErrNoMore = errors.New("no more exchanges in that direction")
	// ErrBadCursor: the pagination cursor was not issued by this store.
	ErrBadCursor = errors.New("invalid pagination cursor")
	// ErrDuplicateID: the history already holds an exchange with the ID that
	// was being recorded; the record is rejected and nothing changes.
	ErrDuplicateID = errors.New("an exchange with that ID already exists")
)

// Direction points item-by-item navigation.
type Direction int

const (
	// Older walks back in time.
	Older Direction = iota
	// Newer walks forward in time.
	Newer
)

// Page requests one page of the listing. An empty Cursor starts at the
// newest exchange.
type Page struct {
	Limit  int
	Cursor string
}

// ListResult is one page, newest exchange first. An empty Next means there
// is nothing more to fetch.
type ListResult struct {
	Items []exchange.Exchange
	Next  string
}

// Store holds the history. History order is chronological by request
// arrival: start instant, then sequence number and, only to break ties, the
// order in which the exchange was recorded. Since capture records an
// exchange once it finishes, a slow exchange enters the history after others
// that arrived later, but it takes the position of its arrival. Every
// implementation has to pass the same contract suite (package storetest).
type Store interface {
	// Record appends the exchange to the history, or returns ErrDuplicateID
	// if the ID is already in it.
	Record(ctx context.Context, e *exchange.Exchange) error
	List(ctx context.Context, f exchange.Filter, p Page) (ListResult, error)
	Get(ctx context.Context, id string) (exchange.Exchange, error)
	// Neighbor returns the exchange next to id in the requested direction,
	// among those that satisfy the filter. Exchange id itself need not
	// satisfy it.
	Neighbor(ctx context.Context, id string, d Direction, f exchange.Filter) (exchange.Exchange, error)
	Clear(ctx context.Context) error
	Close() error
}

// DefaultLimit and MaxLimit bound the page size.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// NormalizeLimit applies the default and the page size cap.
func NormalizeLimit(n int) int {
	switch {
	case n <= 0:
		return DefaultLimit
	case n > MaxLimit:
		return MaxLimit
	}
	return n
}
