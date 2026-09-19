// Package learn implementa o modo aprendizado: cada combinação de método e
// path ainda desconhecida numa rota, respondida pelo upstream, é gravada no
// documento da rota como um override desligado, com a resposta observada
// pré-preenchida. A gravação acontece numa goroutine própria, fora do caminho
// da requisição, pelo mesmo Writer da API de administração.
package learn

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/config/writer"
	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/override"
)

// QueueSize é quantos endpoints podem aguardar gravação. Com a fila cheia o
// endpoint é descartado com um aviso no log e volta a ser candidato na
// próxima requisição: o encaminhamento nunca espera pelo aprendizado.
const QueueSize = 256

// Learner grava os endpoints aprendidos.
type Learner struct {
	w   *writer.Writer
	log *slog.Logger
	now func() time.Time

	queue chan job

	// mu protege pending, unlearnable e closed. pending guarda os endpoints
	// já na fila, para que uma rajada de requisições ao mesmo endpoint novo
	// não empilhe gravações; a verificação que impede a duplicação é a feita
	// sob o mutex de escrita, contra o documento em disco. unlearnable guarda
	// os endpoints cujo override montado não é válido, para que não voltem à
	// fila (e ao log) a cada requisição.
	mu          sync.Mutex
	pending     map[key]bool
	unlearnable map[key]bool
	closed      bool

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

// key identifica um endpoint candidato pelo path generalizado: requisições a
// registros distintos do mesmo endpoint ("/viacep/1/json", "/viacep/2/json")
// aguardam uma única gravação.
type key struct{ route, method, path string }

func learnKey(route string, e exchange.Exchange) key {
	return key{route, e.Method, override.Generalize(e.Path)}
}

type job struct {
	route   string
	ex      exchange.Exchange
	barrier chan struct{}
}

// New grava pelo writer dado. log nil usa o padrão.
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

// Eligible informa se a troca pode ensinar um endpoint: encaminhada e
// respondida pelo próprio upstream, com a resposta completa. Respostas
// sintetizadas ou derrubadas por override, erros do gateway (sem rota, 502,
// 504), transferências interrompidas e upgrades de protocolo ficam de fora.
// Um atraso injetado não altera a resposta do upstream e não impede o
// aprendizado.
func Eligible(e exchange.Exchange) bool {
	return e.Outcome == exchange.OutcomeUpstream && e.Error == "" && e.Route != "" &&
		e.Status >= 200 && e.Status != http.StatusSwitchingProtocols
}

// Observe recebe uma troca encerrada na rota dada e, se ela ensina um
// endpoint desconhecido, enfileira a gravação sem esperar por ela.
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
		l.log.Warn("endpoint não aprendido: fila do aprendizado cheia",
			"rota", route.Name(), "metodo", e.Method, "path", e.Path)
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
			// O documento relido do disco é a palavra final: um endpoint
			// gravado por uma requisição anterior não é gravado de novo.
			if override.Known(*r, e.Method, e.Path) {
				return false, nil
			}
			o := override.FromExchange(e, config.SourceLearned, l.now())
			o.Match = override.LearnMatch(e.Method, e.Path)
			o.On = new(bool)
			// O generalizado substitui os aprendidos exatos que cobre. O nome,
			// derivado do path gravado, é escolhido depois, entre os que
			// restam na rota.
			list, at := override.Absorb(r.Overrides, o)
			namePath := o.Match.Path
			if namePath == "" {
				namePath = e.Path
			}
			o.Name = override.Name(config.Route{Overrides: list}, e.Method, namePath)
			list[at].Name = o.Name
			// Um override que não seria aceito como documento de rota não é
			// gravado, e o endpoint deixa de ser candidato.
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
		l.log.Warn("endpoint não aprendido: o override montado a partir da troca é inválido; ele não será tentado de novo",
			"rota", j.route, "metodo", e.Method, "path", e.Path, "erro", invalid)
	case err != nil:
		l.log.Error("falha ao gravar o endpoint aprendido; o documento da rota não foi alterado",
			"rota", j.route, "metodo", e.Method, "path", e.Path, "erro", err)
	}
	l.mu.Lock()
	delete(l.pending, k)
	if invalid != nil {
		l.unlearnable[k] = true
	}
	l.mu.Unlock()
}

// Sync espera até que todo endpoint enfileirado antes da chamada tenha sido
// gravado (ou descartado por falha).
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

// Close grava o que ainda estiver na fila e encerra a goroutine de gravação.
func (l *Learner) Close() {
	l.once.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		close(l.stop)
	})
	<-l.done
}
