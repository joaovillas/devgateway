// Package exchange defines the HTTP exchange recorded by the gateway and the
// filter used to query it, shared by capture, storage and the API.
package exchange

import (
	"crypto/rand"
	"net/http"
	"strings"
	"time"
)

// Outcome says what produced the result of the exchange.
type Outcome string

const (
	OutcomeUpstream    Outcome = "upstream"    // response from the upstream
	OutcomeSynthesized Outcome = "synthesized" // response declared by an override
	OutcomeDropped     Outcome = "dropped"     // connection dropped by an override
	OutcomeGateway     Outcome = "gateway"     // error response from the gateway itself
)

// How a connection drop happened.
const (
	// DropHijack: HTTP/1.x, the socket was hijacked and closed without a response.
	DropHijack = "hijack"
	// DropStreamReset: HTTP/2, where there is no socket to close, the stream
	// was cancelled abruptly, without a response.
	DropStreamReset = "stream_reset"
	// DropAbort: HTTP/1.x whose ResponseWriter does not allow hijacking; the
	// handler was aborted and the server closed the connection without a
	// response.
	DropAbort = "abort"
)

// Exchange is an HTTP exchange that went through the traffic port.
type Exchange struct {
	ID    string    `json:"id"`
	Seq   uint64    `json:"seq"` // arrival sequence number within the process
	Start time.Time `json:"start"`

	Method     string `json:"method"`
	Host       string `json:"host"`
	Path       string `json:"path"`
	Query      string `json:"query,omitempty"`
	ClientAddr string `json:"clientAddr,omitempty"`

	Route    string `json:"route,omitempty"`
	Upstream string `json:"upstream,omitempty"`
	// Override is "route/override" when some override stepped in.
	Override string `json:"override,omitempty"`
	// Interventions lists what the override did: synthesized, delayed, dropped.
	Interventions []string `json:"interventions,omitempty"`
	Outcome       Outcome  `json:"outcome"`
	// DropMode records how the drop happened: hijack (HTTP/1.1),
	// stream_reset (HTTP/2, where there is no socket to close) or abort.
	DropMode string `json:"dropMode,omitempty"`
	// Error describes gateway or upstream failures (502, 504...).
	Error string `json:"error,omitempty"`

	Status   int     `json:"status,omitempty"` // zero when there was no response
	Request  Message `json:"request"`
	Response Message `json:"response"`

	Timing Timing `json:"timing"`
}

// Intervened reports whether an override changed the result of this exchange.
func (e *Exchange) Intervened() bool { return len(e.Interventions) > 0 }

// Message is one side of the exchange: headers and the captured body.
type Message struct {
	Headers http.Header `json:"headers,omitempty"`
	Body    []byte      `json:"body,omitempty"`
	// Size is the real body size in bytes, even when truncated.
	Size      int64 `json:"size"`
	Truncated bool  `json:"truncated,omitempty"`
}

// Timing breaks the exchange latency down, in milliseconds.
//
// Upstream time runs from sending the request to the upstream until the end
// of the response body, minus the delay injected inside that window. The body
// is streamed to the client rather than buffered: when the client reads more
// slowly than the upstream writes, the copy waits on the client, and that
// wait counts as upstream time. Telling them apart would mean buffering the
// whole response before writing it, which would break streaming.
type Timing struct {
	TotalMs    float64 `json:"totalMs"`
	UpstreamMs float64 `json:"upstreamMs"`
	InjectedMs float64 `json:"injectedMs"`
	GatewayMs  float64 `json:"gatewayMs"`
}

// Ms converts a duration to milliseconds.
func Ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// Filter narrows a query. Empty fields do not filter; the ones that are set
// apply together.
type Filter struct {
	Route    string
	Upstream string
	Override string
	Method   string
	// Path matches by content: "/charge" finds "/api/payments/charge/1".
	Path      string
	StatusMin int
	StatusMax int
	// Intervened, when set, separates exchanges with and without intervention.
	Intervened *bool
	Since      time.Time
	Until      time.Time
}

// Match applies the filter to an exchange. Backends that filter outside Go
// have to reproduce exactly these semantics.
func (f Filter) Match(e *Exchange) bool {
	switch {
	case f.Route != "" && e.Route != f.Route:
		return false
	case f.Upstream != "" && e.Upstream != f.Upstream:
		return false
	case f.Override != "" && e.Override != f.Override:
		return false
	case f.Method != "" && !strings.EqualFold(e.Method, f.Method):
		return false
	case f.Path != "" && !strings.Contains(e.Path, f.Path):
		return false
	case f.StatusMin != 0 && e.Status < f.StatusMin:
		return false
	case f.StatusMax != 0 && e.Status > f.StatusMax:
		return false
	case f.Intervened != nil && e.Intervened() != *f.Intervened:
		return false
	case !f.Since.IsZero() && e.Start.Before(f.Since):
		return false
	case !f.Until.IsZero() && !e.Start.Before(f.Until):
		return false
	}
	return true
}

// Summary is the exchange without the bodies, for listings.
func (e Exchange) Summary() Exchange {
	e.Request.Body = nil
	e.Response.Body = nil
	return e
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID generates an identifier in ULID format: 26 characters, sortable by
// creation time and unique across processes.
func NewID(t time.Time) string {
	var b [16]byte
	ms := uint64(t.UnixMilli())
	for i := 5; i >= 0; i-- {
		b[i] = byte(ms)
		ms >>= 8
	}
	rand.Read(b[6:])
	// 128 bits in 26 base32 digits, most significant first.
	var out [26]byte
	var acc uint64
	bits := 0
	j := 25
	for i := 15; i >= 0; i-- {
		acc |= uint64(b[i]) << bits
		bits += 8
		for bits >= 5 && j >= 0 {
			out[j] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			j--
		}
	}
	for j >= 0 {
		out[j] = crockford[acc&31]
		acc >>= 5
		j--
	}
	return string(out[:])
}
