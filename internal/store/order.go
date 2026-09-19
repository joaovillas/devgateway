package store

import (
	"cmp"
	"encoding/base64"
	"encoding/json"

	"github.com/gamerjp64/devgateway/internal/exchange"
)

// orderKey places an exchange in the history. The order is chronological by
// arrival: start instant, then the arrival sequence number and, last, the
// order in which the backend recorded it, which only breaks ties between
// exchanges with the same start and sequence (such as ones from different
// processes at the same instant). Capture writes an exchange once it
// finishes; without this key, a slow exchange that arrived earlier would
// show up as newer than the fast ones that arrived after it.
//
// The start is kept as separate seconds and nanoseconds, not as nanoseconds
// since the epoch, because UnixNano cannot represent instants outside
// 1678-2262 (the zero instant among them).
type orderKey struct {
	Sec  int64  `json:"s"`
	Nsec int32  `json:"n"`
	Seq  uint64 `json:"q"`
	Ins  uint64 `json:"i"`
}

// keyOf builds the key for e, recorded at record position ins.
func keyOf(e *exchange.Exchange, ins uint64) orderKey {
	return orderKey{Sec: e.Start.Unix(), Nsec: int32(e.Start.Nanosecond()), Seq: e.Seq, Ins: ins}
}

// compare returns -1, 0 or +1 depending on whether a comes before, at the
// same place as, or after b.
func (a orderKey) compare(b orderKey) int {
	if c := cmp.Compare(a.Sec, b.Sec); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Nsec, b.Nsec); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Seq, b.Seq); c != 0 {
		return c
	}
	return cmp.Compare(a.Ins, b.Ins)
}

// cursor continues a listing: the key of the last exchange delivered and the
// history epoch it was issued in. Every clear bumps the epoch, and a cursor
// from an earlier epoch cannot reach the new exchanges.
type cursor struct {
	Epoch uint64   `json:"e"`
	Key   orderKey `json:"k"`
}

// encodeCursor serializes the cursor into an opaque, URL-safe string.
func encodeCursor(epoch uint64, k orderKey) string {
	b, _ := json.Marshal(cursor{Epoch: epoch, Key: k})
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor reads a cursor issued by encodeCursor, or returns
// ErrBadCursor.
func decodeCursor(s string) (cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, ErrBadCursor
	}
	var c cursor
	if err := json.Unmarshal(b, &c); err != nil {
		return cursor{}, ErrBadCursor
	}
	return c, nil
}
