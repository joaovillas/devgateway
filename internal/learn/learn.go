// Package learn implements learning mode: every method-and-path combination
// still unknown in a route, once answered by the upstream, is written to the
// route document as a disabled override, with the observed response
// pre-filled. Writing happens in a goroutine of its own, off the request
// path, through the same Writer the admin API uses.
package learn

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/config/writer"
	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/override"
)

// QueueSize is how many endpoints can wait to be written. With the queue
// full the endpoint is dropped with a warning in the log and becomes a
// candidate again on the next request: forwarding never waits for learning.
const QueueSize = 256

// Learner writes the learned endpoints.
type Learner struct {
	w   *writer.Writer
	log *slog.Logger
	now func() time.Time

	queue chan job

	// mu guards pending, unlearnable and closed. pending holds the endpoints
	// already in the queue, so that a burst of requests to the same new
	// endpoint does not pile up writes; the check that actually prevents
	// duplication is the one done under the write mutex, against the document
	// on disk. unlearnable holds the endpoints whose built override is not
	// valid, so that they do not come back to the queue (and to the log) on
	// every request.
	mu          sync.Mutex
	pending     map[key]bool
	unlearnable map[key]bool
	closed      bool

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// key identifies a candidate endpoint by its generalized path: requests for
// different records of the same endpoint ("/zip/1/json", "/zip/2/json")
// wait on a single write.
type key struct{ route, method, path string }

func learnKey(route string, e exchange.Exchange) key {
	return key{route, e.Method, override.Generalize(e.Path)}
}

type job struct {
	route   string
	ex      exchange.Exchange
	barrier chan struct{}
}

// New writes through the given writer. A nil log uses the default one.
func New(w *writer.Writer, log *slog.Logger) *Learner {
	if log == nil {
		log = slog.Default()
	}
	l := &Learner{
		w:           w,
		log:         log,
		now:         time.Now,
		queue:       make(chan job, QueueSize),
		pending:     map[key]bool{},
		unlearnable: map[key]bool{},
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	go l.run()
	return l
}

// Eligible reports whether the exchange can teach an endpoint: forwarded and
// answered by the upstream itself, with a complete response. Responses
// synthesized or dropped by an override, gateway errors (no route, 502, 504),
// interrupted transfers and protocol upgrades are left out. An injected delay
// does not change the upstream's response and does not prevent learning.
func Eligible(e exchange.Exchange) bool {
	return e.Outcome == exchange.OutcomeUpstream && e.Error == "" && e.Route != "" &&
		e.Status >= 200 && e.Status != http.StatusSwitchingProtocols
}

// Observe takes an exchange that finished on the given route and, if it
// teaches an unknown endpoint, queues the write without waiting for it.
func (l *Learner) Observe(route *config.CompiledRoute, e exchange.Exchange) {
	if route == nil || !Eligible(e) || override.Known(route.Doc, e.Method, e.Path) {
		return
	}
	k := learnKey(route.Name(), e)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.pending[k] || l.unlearnable[k] {
		return
	}
	select {
	case l.queue <- job{route: route.Name(), ex: e}:
		l.pending[k] = true
	default:
		l.log.Warn("endpoint not learned: the learning queue is full",
			"route", route.Name(), "method", e.Method, "path", e.Path)
	}
}

func (l *Learner) run() {
	defer close(l.done)
	for {
		select {
		case j := <-l.queue:
			l.handle(j)
		case <-l.stop:
			for {
				select {
				case j := <-l.queue:
					l.handle(j)
				default:
					return
				}
			}
		}
	}
}

func (l *Learner) handle(j job) {
	if j.barrier != nil {
		close(j.barrier)
		return
	}
	e := j.ex
	var invalid error
	_, err := l.w.UpdateRoute(writer.RouteUpdate{
		Route: j.route,
		Cause: writer.CauseLearning,
		Apply: func(r *config.Route) (bool, error) {
			// The document re-read from disk has the final say: an endpoint
			// written by an earlier request is not written again.
			if override.Known(*r, e.Method, e.Path) {
				return false, nil
			}
			o := override.FromExchange(e, config.SourceLearned, l.now())
			o.Match = override.LearnMatch(e.Method, e.Path)
			o.On = new(bool)
			// The generalized override replaces the learned exact ones it
			// covers. The name, derived from the path that gets written, is
			// chosen afterwards, among the ones left in the route.
			list, at := override.Absorb(r.Overrides, o)
			namePath := o.Match.Path
			if namePath == "" {
				namePath = e.Path
			}
			o.Name = override.Name(config.Route{Overrides: list}, e.Method, namePath)
			list[at].Name = o.Name
			// An override that would not be accepted as a route document is
			// not written, and the endpoint stops being a candidate.
			if invalid = config.ValidateOverride(j.route, o); invalid != nil {
				return false, invalid
			}
			r.Overrides = list
			return true, nil
		},
	})
	k := learnKey(j.route, e)
	switch {
	case invalid != nil:
		l.log.Warn("endpoint not learned: the override built from the exchange is invalid; it will not be tried again",
			"route", j.route, "method", e.Method, "path", e.Path, "error", invalid)
	case err != nil:
		l.log.Error("failed to write the learned endpoint; the route document was left unchanged",
			"route", j.route, "method", e.Method, "path", e.Path, "error", err)
	}
	l.mu.Lock()
	delete(l.pending, k)
	if invalid != nil {
		l.unlearnable[k] = true
	}
	l.mu.Unlock()
}

// Sync waits until every endpoint queued before the call has been written (or
// dropped after a failure).
func (l *Learner) Sync(ctx context.Context) error {
	b := make(chan struct{})
	select {
	case l.queue <- job{barrier: b}:
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-b:
		return nil
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close writes whatever is still in the queue and stops the writing goroutine.
func (l *Learner) Close() {
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		close(l.stop)
	})
	<-l.done
}
