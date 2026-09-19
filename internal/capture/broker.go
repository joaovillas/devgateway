package capture

import (
	"sync"
	"sync/atomic"

	"github.com/joaovillas/devgateway/internal/exchange"
)

// Broker hands the recorded exchanges to whoever is subscribed, such as the
// API's real-time stream. Publishing never blocks: a subscriber that does not
// keep up loses the exchanges that do not fit in its buffer, and the loss is
// counted on the subscription.
type Broker struct {
	mu   sync.Mutex
	subs map[*Subscription]struct{}
}

// NewBroker creates a broker with no subscribers.
func NewBroker() *Broker { return &Broker{subs: map[*Subscription]struct{}{}} }

// Subscription is a subscription to the recorded exchanges. C delivers the
// exchanges in recording order and is closed by Close.
type Subscription struct {
	C       <-chan exchange.Exchange
	c       chan exchange.Exchange
	b       *Broker
	dropped atomic.Uint64
	closed  bool // guarded by b.mu
}

// Subscribe creates a subscription buffered for up to buffer pending
// exchanges.
func (b *Broker) Subscribe(buffer int) *Subscription {
	if buffer < 1 {
		buffer = 1
	}
	c := make(chan exchange.Exchange, buffer)
	s := &Subscription{C: c, c: c, b: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Publish delivers the exchange to every subscriber that has room in its
// buffer, without waiting for any of them.
func (b *Broker) Publish(e exchange.Exchange) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		select {
		case s.c <- e:
		default:
			s.dropped.Add(1)
		}
	}
}

// Subscribers reports how many subscriptions are open.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Dropped reports how many exchanges this subscription lost to a full buffer.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// TakeDropped returns the losses piled up since the last call and zeroes the
// count, for whoever reports losses per interval.
func (s *Subscription) TakeDropped() uint64 { return s.dropped.Swap(0) }

// Close ends the subscription and closes C. It can be called more than once.
func (s *Subscription) Close() {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	delete(s.b.subs, s)
	close(s.c)
}
