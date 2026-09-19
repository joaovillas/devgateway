package override

import (
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
)

// Source returns the randomness source for the request with sequence number
// seq. With a seed, it is derived only from (seed, seq): a request's
// decisions depend on no other request and on no goroutine scheduling order,
// and the same seed with the same arrival order reproduces the same
// decisions. Without a seed, the source is seeded at random and does not
// reproduce.
func Source(seed *uint64, seq uint64) *rand.Rand {
	if seed == nil {
		return rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}
	// Neighboring seeds and sequence numbers differ by only a few bits;
	// mixing keeps the PCG's first values from coming out correlated.
	return rand.New(rand.NewPCG(mix(*seed), mix(seq)))
}

// mix is the splitmix64 finalizer: a bijection that spreads every input bit
// across the whole output.
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// Decision is the result of step 4's draw for the selected override.
type Decision struct {
	// Override is the selected override; nil when none matched.
	Override *config.CompiledOverride
	// Apply reports that the override holds for this request. When false, the
	// request proceeds as if the override did not exist, and Drop and Delay
	// are zero.
	Apply bool
	// Drop reports that the connection must be dropped without a response.
	Drop bool
	// Delay is the delay to inject between the response being ready and the
	// write to the client.
	Delay time.Duration
}

// Applied returns the override that holds for this request, or nil.
func (d Decision) Applied() *config.CompiledOverride {
	if !d.Apply {
		return nil
	}
	return d.Override
}

// Decide draws, in a fixed order and from the given source, the override's
// application, drop and delay. All three values are always consumed, in this
// order, even when the configuration does not need one of them: that way each
// draw's position in the sequence depends neither on the configuration nor on
// what happens later (upstream availability, for instance). With a nil
// override the decision is empty and nothing is drawn.
func Decide(o *config.CompiledOverride, rng *rand.Rand) Decision {
	if o == nil {
		return Decision{}
	}
	apply := rng.Float64()
	// The drop is declared, not drawn; its slot stays reserved so that the
	// delay always takes the third value.
	rng.Float64()
	delay := rng.Float64()
	p := 1.0
	if o.Doc.Probability != nil {
		p = *o.Doc.Probability
	}
	d := Decision{Override: o, Apply: apply < p}
	if !d.Apply {
		return d
	}
	d.Drop = o.Doc.Drop
	if l := o.Doc.Latency; l != nil {
		switch {
		case l.Fixed != nil:
			d.Delay = time.Duration(*l.Fixed)
		case l.Min != nil && l.Max != nil:
			lo, hi := time.Duration(*l.Min), time.Duration(*l.Max)
			d.Delay = lo + time.Duration(delay*float64(hi-lo))
		}
	}
	return d
}

// Status is the declared response's status: 200 when not given.
func Status(o *config.CompiledOverride) int {
	if r := o.Doc.Respond; r != nil && r.Status != 0 {
		return r.Status
	}
	return http.StatusOK
}

// Body is the declared response's body, already serialized.
func Body(o *config.CompiledOverride) []byte { return o.RespondBody }

// SetHeaders adds the declared response's headers to h. A body declared as a
// structure gets the JSON content type, unless the override declares another
// one. With no content type declared or inferred, the server does not guess
// one from the body: the client gets only what the override declared.
func SetHeaders(h http.Header, o *config.CompiledOverride) {
	declared := false
	if r := o.Doc.Respond; r != nil {
		// A header with several values is repeated in the response, once per
		// value, in the declared order.
		for k, vs := range r.Headers {
			for _, v := range vs {
				h.Add(k, v)
			}
			if strings.EqualFold(k, "Content-Type") {
				declared = true
			}
		}
	}
	switch {
	case declared:
	case o.RespondContentType != "":
		h.Set("Content-Type", o.RespondContentType)
	default:
		h["Content-Type"] = nil
	}
}
