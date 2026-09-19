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

// Decision is the result of step 4's draw for the selected override: which
// of its effects hold for this request.
type Decision struct {
	// Override is the selected override; nil when none matched.
	Override *config.CompiledOverride
	// Apply reports that at least one effect was drawn. When false, the
	// request proceeds as if the override did not exist, and the effects are
	// all zero.
	Apply bool
	// Drop reports that the connection must be dropped without a response.
	Drop bool
	// Respond reports that the declared response holds. When false, the
	// request goes to the upstream even though the override declares one.
	Respond bool
	// Delayed reports that the delay was drawn, which a delay of zero does
	// not tell apart on its own.
	Delayed bool
	// Delay is the delay to inject between the response being ready and the
	// write to the client.
	Delay time.Duration
}

// Applied returns the override whose effects hold for this request, or nil.
func (d Decision) Applied() *config.CompiledOverride {
	if !d.Apply {
		return nil
	}
	return d.Override
}

// Decide draws, in the fixed order drop, respond and delay, which of the
// override's effects hold for this request, each one against its own
// frequency and independently of the others. Four values are always consumed
// from the source, in this order — drop, respond, delay and the delay's value
// within the range — even when the configuration declares none of them: that
// way each draw's position in the sequence depends neither on the
// configuration nor on what happens later (upstream availability, for
// instance). With a nil override the decision is empty and nothing is drawn.
func Decide(o *config.CompiledOverride, rng *rand.Rand) Decision {
	if o == nil {
		return Decision{}
	}
	drop, respond := rng.Float64(), rng.Float64()
	delay, within := rng.Float64(), rng.Float64()

	// An effect that declares no frequency falls back to the override's
	// legacy probability, and to always when there is none either. An
	// undeclared effect has frequency zero and is never drawn.
	drawn := func(roll, chance float64) bool { return chance > 0 && roll < chance }

	d := Decision{Override: o}
	d.Drop = drawn(drop, o.Doc.DropChance())
	d.Respond = drawn(respond, o.Doc.RespondChance())
	d.Delayed = drawn(delay, o.Doc.LatencyChance())
	if l := o.Doc.Latency; d.Delayed && l != nil {
		switch {
		case l.Fixed != nil:
			d.Delay = time.Duration(*l.Fixed)
		case l.Min != nil && l.Max != nil:
			lo, hi := time.Duration(*l.Min), time.Duration(*l.Max)
			d.Delay = lo + time.Duration(within*float64(hi-lo))
		}
	}
	d.Apply = d.Drop || d.Respond || d.Delayed
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
