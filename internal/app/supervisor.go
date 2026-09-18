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

// port é uma porta atendida pelo processo — a de tráfego ou a de
// administração — sob o supervisor de listeners, fora do snapshot. A troca a
// quente abre o listener novo primeiro; só se ele abrir o servidor novo
// passa a atender e o antigo recebe Shutdown, que para de aceitar conexões e
// deixa as requisições em curso terminarem. Se o listener novo falha, nada
// muda.
type port struct {
	// name nomeia a porta nas mensagens ("tráfego", "administração").
	name string
	// key é a chave da configuração do processo ("ports.traffic").
	key     string
	handler http.Handler
	// baseContext, quando presente, monta o contexto base das requisições
	// do servidor a partir do sinal de encerramento dele, para que conexões
	// longas (o fluxo de eventos) terminem quando ele sai de serviço.
	baseContext func(stop <-chan struct{}) context.Context
	// fatal recebe o erro de um servidor que parou de atender por conta
	// própria.
	fatal func(error)

	mu  sync.Mutex
	cur *binding
	// retiring são os servidores já substituídos que ainda concluem
	// requisições em curso.
	retiring map[*binding]struct{}
	// closing marca que o encerramento da porta começou. Uma troca a quente
	// ainda em curso (um PATCH /api/settings que o encerramento aguarda) não
	// publica mais o binding novo, que ficaria escutando fora do alcance do
	// Shutdown.
	closing bool
}

// binding é um listener aberto e o servidor que o atende.
type binding struct {
	ln   net.Listener
	srv  *http.Server
	stop chan struct{}
	once sync.Once
	// done fecha quando o Shutdown do servidor conclui.
	done chan struct{}
}

// open abre o listener da porta dada, sem ainda atendê-lo.
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

// signal avisa as conexões longas de que o servidor sai de serviço.
func (b *binding) signal() { b.once.Do(func() { close(b.stop) }) }

// abort fecha o listener de uma troca que não será feita.
func (b *binding) abort() { b.ln.Close() }

// commit passa a atender pelo binding dado e tira de serviço o anterior, cujo
// Shutdown conclui as requisições em curso em segundo plano. Se o
// encerramento da porta já começou, o binding é descartado e commit devolve
// false: a porta segue na atual até sair de serviço.
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
		p.fatal(fmt.Errorf("porta de %s: %w", p.name, err))
	}
}

// addr é o endereço em que a porta atende agora.
func (p *port) addr() net.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur.ln.Addr()
}

// number é o número da porta em que ela atende agora.
func (p *port) number() int {
	if a, ok := p.addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// shutdown encerra o servidor em uso e os que ainda concluem requisições,
// aguardando as requisições em curso até o prazo de ctx. Passado o prazo, as
// conexões restantes são fechadas. Nenhuma troca a quente é feita depois de
// o encerramento começar.
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
				errs[i] = fmt.Errorf("porta de %s: %w", p.name, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}
