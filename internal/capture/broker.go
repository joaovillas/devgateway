package capture

import (
	"sync"
	"sync/atomic"

	"github.com/gamerjp64/gateway/internal/exchange"
)

// Broker distribui as trocas registradas a quem estiver assinando, como o
// fluxo em tempo real da API. A publicação nunca bloqueia: um assinante que
// não consome a tempo perde as trocas que não couberem no seu buffer, e a
// perda fica contada na assinatura.
type Broker struct {
	mu   sync.Mutex
	subs map[*Subscription]struct{}
}

// NewBroker cria um broker sem assinantes.
func NewBroker() *Broker { return &Broker{subs: map[*Subscription]struct{}{}} }

// Subscription é uma assinatura das trocas registradas. C entrega as trocas
// na ordem de registro e é fechado por Close.
type Subscription struct {
	C       <-chan exchange.Exchange
	c       chan exchange.Exchange
	b       *Broker
	dropped atomic.Uint64
	closed  bool // protegido por b.mu
}

// Subscribe cria uma assinatura com buffer para até buffer trocas pendentes.
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

// Publish entrega a troca a cada assinante que tiver espaço no buffer, sem
// esperar por nenhum deles.
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

// Subscribers informa quantas assinaturas estão abertas.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Dropped informa quantas trocas esta assinatura perdeu por buffer cheio.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// TakeDropped devolve as perdas acumuladas desde a última chamada e zera a
// contagem, para quem informa as perdas por intervalo.
func (s *Subscription) TakeDropped() uint64 { return s.dropped.Swap(0) }

// Close encerra a assinatura e fecha C. Pode ser chamado mais de uma vez.
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
