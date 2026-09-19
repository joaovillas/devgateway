// Package capture records the exchanges that go through the traffic port: it
// opens the record when the request comes in, observes body and response
// without changing what is passed along, breaks the latency down and writes
// the exchange to the history in use, off the request path.
package capture

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/joaovillas/devgateway/internal/exchange"
	"github.com/joaovillas/devgateway/internal/store"
)

// QueueSize is how many exchanges can wait to be written. With the queue full
// the exchange is dropped with a warning in the log: forwarding never waits
// for the history.
const QueueSize = 1024

// Recorder opens the records of the requests and writes the finished
// exchanges to the history, in a single goroutine, in the order they were
// finished. Every exchange written is published on the broker.
type Recorder struct {
	store  store.Store
	broker *Broker
	log    *slog.Logger
	now    func() time.Time

	// seq numbers the requests in arrival order, recorded or not.
	seq atomic.Uint64

	// open counts the records opened and not yet closed, so that shutdown can
	// wait for them.
	open atomic.Int64

	queue chan item
	// mu guards closed: with it, no exchange enters the queue after the
	// writing goroutine starts draining it to shut down.
	mu     sync.RWMutex
	closed bool
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

// item is an exchange to write or a synchronization barrier.
type item struct {
	ex      *exchange.Exchange
	barrier chan struct{}
}

// NewRecorder writes to the given store, which is normally the process's
// switchable history, so that swapping the backend at runtime takes effect
// from the next capture on. broker may be nil; a nil log uses the default
// one.
func NewRecorder(st store.Store, broker *Broker, log *slog.Logger) *Recorder {
	if log == nil {
		log = slog.Default()
	}
	if broker == nil {
		broker = NewBroker()
	}
	r := &Recorder{
		store:  st,
		broker: broker,
		log:    log,
		now:    time.Now,
		queue:  make(chan item, QueueSize),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	go r.run()
	return r
}

// Broker returns the broker the recorded exchanges are published on.
func (r *Recorder) Broker() *Broker { return r.broker }

// Options is what the configuration in effect says about a request.
type Options struct {
	// Record turns recording on (history.record).
	Record bool
	// Observe turns on observing the exchange even with recording off,
	// without writing it to the history: learning mode needs the observed
	// response.
	Observe bool
	// MaxBodyBytes is the capture limit for each body (capture.maxBodyBytes).
	MaxBodyBytes int
}

// Begin opens the record of a request that has just arrived and starts the
// clock. The sequence number is assigned even with recording off, because it
// also orders the request's decisions.
func (r *Recorder) Begin(w http.ResponseWriter, req *http.Request, o Options) *Record {
	start := r.now()
	r.open.Add(1)
	rec := &Record{
		rec:     r,
		enabled: o.Record || o.Observe,
		store:   o.Record,
		start:   start,
		seq:     r.seq.Add(1),
		w:       w,
		req:     req,
	}
	if !rec.enabled {
		return rec
	}
	limit := max(o.MaxBodyBytes, 0)
	rec.ex = exchange.Exchange{
		ID:         exchange.NewID(start),
		Seq:        rec.seq,
		Start:      start.UTC(),
		Method:     req.Method,
		Host:       req.Host,
		Path:       req.URL.Path,
		Query:      req.URL.RawQuery,
		ClientAddr: req.RemoteAddr,
		Outcome:    exchange.OutcomeUpstream,
	}
	rec.ex.Request.Headers = req.Header.Clone()
	if req.Body != nil && req.Body != http.NoBody {
		rec.body = &bodyTap{rc: req.Body, tap: tap{limit: limit}}
		r2 := *req
		r2.Body = rec.body
		rec.req = &r2
	}
	rec.cw = &writer{ResponseWriter: w, tap: tap{limit: limit}}
	rec.w = rec.cw
	return rec
}

// enqueue hands the exchange to the writing goroutine without waiting. After
// Close there is no one left to write it: the exchange is dropped with a
// warning in the log, instead of vanishing into the queue.
func (r *Recorder) enqueue(e *exchange.Exchange) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		r.log.Warn("exchange dropped: history recording has already been shut down",
			"id", e.ID, "method", e.Method, "path", e.Path)
		return
	}
	select {
	case r.queue <- item{ex: e}:
	default:
		r.log.Warn("exchange dropped: the history write queue is full",
			"id", e.ID, "method", e.Method, "path", e.Path)
	}
}

// finished counts the closing of a record opened by Begin.
func (r *Recorder) finished() { r.open.Add(-1) }

// Open reports how many records have been opened and not yet closed.
func (r *Recorder) Open() int64 { return r.open.Load() }

// Wait waits for every open record to be closed, or for ctx to end. It serves
// process shutdown: http.Server.Shutdown does not wait for hijacked
// connections (protocol upgrades), whose records are still closed after it.
func (r *Recorder) Wait(ctx context.Context) error {
	t := time.NewTicker(5 * time.Millisecond)
	defer t.Stop()
	for r.open.Load() > 0 {
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (r *Recorder) run() {
	defer close(r.done)
	for {
		select {
		case it := <-r.queue:
			r.handle(it)
		case <-r.stop:
			// Write whatever was already in the queue before shutting down.
			for {
				select {
				case it := <-r.queue:
					r.handle(it)
				default:
					return
				}
			}
		}
	}
}

func (r *Recorder) handle(it item) {
	if it.barrier != nil {
		close(it.barrier)
		return
	}
	// Writing does not inherit the request's context, which has already
	// ended.
	if err := r.store.Record(context.Background(), it.ex); err != nil {
		r.log.Error("failed to write the exchange to the history; forwarding was not affected",
			"id", it.ex.ID, "method", it.ex.Method, "path", it.ex.Path, "error", err)
		return
	}
	r.broker.Publish(*it.ex)
}

// Sync waits until every exchange finished before the call has been written
// (or dropped after a write failure). It serves whoever reads the history
// right after a request and wants to see it there.
func (r *Recorder) Sync(ctx context.Context) error {
	b := make(chan struct{})
	select {
	case r.queue <- item{barrier: b}:
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-b:
		return nil
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Clear empties the history in use. The exchanges finished before the call
// are written first, so that none of them shows up again after the cleanup;
// the ones after it go on being recorded as usual.
func (r *Recorder) Clear(ctx context.Context) error {
	if err := r.Sync(ctx); err != nil {
		return err
	}
	return r.store.Clear(ctx)
}

// Close writes whatever is still in the queue and stops the writing
// goroutine. It does not close the store, which belongs to whoever created
// it.
func (r *Recorder) Close() {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		close(r.stop)
	})
	<-r.done
}
