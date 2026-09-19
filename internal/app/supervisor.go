package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// port is a port the process serves — the traffic one or the admin one —
// under the listener supervisor, outside the snapshot. A live swap opens the
// new listener first; only if it opens does the new server start serving and
// the old one get a Shutdown, which stops accepting connections and lets the
// requests in flight finish. If the new listener fails, nothing changes.
type port struct {
	// name names the port in messages ("traffic", "admin").
	name string
	// key is the process configuration key ("ports.traffic").
	key     string
	handler http.Handler
	// baseContext, when present, builds the base context of the server's
	// requests from its shutdown signal, so that long-lived connections (the
	// event stream) end when it goes out of service.
	baseContext func(stop <-chan struct{}) context.Context
	// fatal receives the error of a server that stopped serving on its own.
	fatal func(error)

	mu  sync.Mutex
	cur *binding
	// retiring are the servers already replaced that are still finishing
	// requests in flight.
	retiring map[*binding]struct{}
	// closing marks that the port's shutdown has begun. A live swap still in
	// progress (a PATCH /api/settings that the shutdown is waiting on) no
	// longer publishes the new binding, which would end up listening beyond
	// Shutdown's reach.
	closing bool
}

// binding is an open listener and the server that serves it.
type binding struct {
	ln   net.Listener
	srv  *http.Server
	stop chan struct{}
	once sync.Once
	// done closes when the server's Shutdown completes.
	done chan struct{}
}

// open opens the listener for the given port, without serving it yet.
func (p *port) open(n int) (*binding, error) {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(n))
	if err != nil {
		return nil, err
	}
	b := &binding{ln: ln, stop: make(chan struct{}), done: make(chan struct{})}
	b.srv = &http.Server{Handler: p.handler, ReadHeaderTimeout: 10 * time.Second}
	if p.baseContext != nil {
		ctx := p.baseContext(b.stop)
		b.srv.BaseContext = func(net.Listener) context.Context { return ctx }
	}
	b.srv.RegisterOnShutdown(b.signal)
	return b, nil
}

// signal tells the long-lived connections that the server is going out of
// service.
func (b *binding) signal() { b.once.Do(func() { close(b.stop) }) }

// abort closes the listener of a swap that will not happen.
func (b *binding) abort() { b.ln.Close() }

// commit starts serving through the given binding and takes the previous one
// out of service; its Shutdown finishes the requests in flight in the
// background. If the port's shutdown has already begun, the binding is
// discarded and commit returns false: the port stays on the current one
// until it goes out of service.
func (p *port) commit(b *binding) bool {
	p.mu.Lock()
	if p.closing {
		p.mu.Unlock()
		b.abort()
		return false
	}
	old := p.cur
	p.cur = b
	if old != nil {
		if p.retiring == nil {
			p.retiring = map[*binding]struct{}{}
		}
		p.retiring[old] = struct{}{}
	}
	p.mu.Unlock()
	go p.serve(b)
	if old != nil {
		go func() {
			old.signal()
			old.srv.Shutdown(context.Background())
			close(old.done)
			p.mu.Lock()
			delete(p.retiring, old)
			p.mu.Unlock()
		}()
	}
	return true
}

func (p *port) serve(b *binding) {
	if err := b.srv.Serve(b.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		p.fatal(fmt.Errorf("%s port: %w", p.name, err))
	}
}

// addr is the address the port is serving on right now.
func (p *port) addr() net.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur.ln.Addr()
}

// number is the number of the port it is serving on right now.
func (p *port) number() int {
	if a, ok := p.addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// shutdown closes the server in use and the ones still finishing requests,
// waiting for the requests in flight up to ctx's deadline. Past the
// deadline, the remaining connections are closed. No live swap happens once
// the shutdown has begun.
func (p *port) shutdown(ctx context.Context) error {
	p.mu.Lock()
	p.closing = true
	all := []*binding{p.cur}
	for b := range p.retiring {
		all = append(all, b)
	}
	p.mu.Unlock()
	errs := make([]error, len(all))
	var wg sync.WaitGroup
	for i, b := range all {
		wg.Go(func() {
			b.signal()
			if err := b.srv.Shutdown(ctx); err != nil {
				b.srv.Close()
				errs[i] = fmt.Errorf("%s port: %w", p.name, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
