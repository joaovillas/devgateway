// Package capture registra as trocas que atravessam a porta de tráfego: abre
// o registro na entrada da requisição, observa corpo e resposta sem alterar o
// que é repassado, decompõe a latência e grava a troca no histórico em uso,
// fora do caminho da requisição.
package capture

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gamerjp64/gateway/internal/exchange"
	"github.com/gamerjp64/gateway/internal/store"
)

// QueueSize é quantas trocas podem aguardar gravação. Com a fila cheia a
// troca é descartada com um aviso no log: o encaminhamento nunca espera pelo
// histórico.
const QueueSize = 1024

// Recorder abre os registros das requisições e grava as trocas fechadas no
// histórico, numa única goroutine, na ordem em que foram fechadas. Cada troca
// gravada é publicada no broker.
type Recorder struct {
	store  store.Store
	broker *Broker
	log    *slog.Logger
	now    func() time.Time

	// seq numera as requisições na ordem de chegada, registradas ou não.
	seq atomic.Uint64

	// open conta os registros abertos e ainda não fechados, para que o
	// encerramento possa esperar por eles.
	open atomic.Int64

	queue chan item
	// mu protege closed: com ele, nenhuma troca entra na fila depois que a
	// goroutine de gravação começa a esvaziá-la para encerrar.
	mu     sync.RWMutex
	closed bool
	stop   chan struct{}
	done   chan struct{}
	once   sync.Once
}

// item é uma troca a gravar ou uma barreira de sincronização.
type item struct {
	ex      *exchange.Exchange
	barrier chan struct{}
}

// NewRecorder grava no store dado, que normalmente é o histórico trocável do
// processo, de modo que a troca de backend a quente vale para a captura
// seguinte. broker pode ser nil; log nil usa o padrão.
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

// Broker devolve o broker em que as trocas gravadas são publicadas.
func (r *Recorder) Broker() *Broker { return r.broker }

// Options é o que a configuração em vigor diz sobre uma requisição.
type Options struct {
	// Record liga o registro (history.record).
	Record bool
	// Observe liga a observação da troca mesmo com o registro desligado, sem
	// gravá-la no histórico: o modo aprendizado precisa da resposta observada.
	Observe bool
	// MaxBodyBytes é o limite de captura de cada corpo (capture.maxBodyBytes).
	MaxBodyBytes int
}

// Begin abre o registro de uma requisição que acaba de chegar e inicia a
// cronometragem. O número de sequência é atribuído mesmo com o registro
// desligado, porque ele também ordena as decisões da requisição.
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

// enqueue entrega a troca à goroutine de gravação sem esperar. Depois de
// Close não há quem grave: a troca é descartada com um aviso no log, em vez
// de sumir na fila.
func (r *Recorder) enqueue(e *exchange.Exchange) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		r.log.Warn("troca descartada: o registro do histórico já foi encerrado",
			"id", e.ID, "metodo", e.Method, "path", e.Path)
		return
	}
	select {
	case r.queue <- item{ex: e}:
	default:
		r.log.Warn("troca descartada: fila de gravação do histórico cheia",
			"id", e.ID, "metodo", e.Method, "path", e.Path)
	}
}

// finished conta o fechamento de um registro aberto por Begin.
func (r *Recorder) finished() { r.open.Add(-1) }

// Open informa quantos registros foram abertos e ainda não fechados.
func (r *Recorder) Open() int64 { return r.open.Load() }

// Wait espera que todo registro aberto seja fechado, ou que ctx termine.
// Serve ao encerramento do processo: http.Server.Shutdown não espera as
// conexões sequestradas (upgrades de protocolo), cujos registros ainda
// fecharão depois dele.
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
			// Grava o que já estava na fila antes de encerrar.
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
	// A gravação não herda o contexto da requisição, que já terminou.
	if err := r.store.Record(context.Background(), it.ex); err != nil {
		r.log.Error("falha ao gravar a troca no histórico; o encaminhamento não foi afetado",
			"id", it.ex.ID, "metodo", it.ex.Method, "path", it.ex.Path, "erro", err)
		return
	}
	r.broker.Publish(*it.ex)
}

// Sync espera até que toda troca fechada antes da chamada tenha sido gravada
// (ou descartada por falha de gravação). Serve a quem lê o histórico logo
// depois de uma requisição e quer vê-la.
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

// Clear esvazia o histórico em uso. As trocas fechadas antes da chamada são
// gravadas primeiro, para que nenhuma delas reapareça depois da limpeza; as
// seguintes voltam a ser registradas normalmente.
func (r *Recorder) Clear(ctx context.Context) error {
	if err := r.Sync(ctx); err != nil {
		return err
	}
	return r.store.Clear(ctx)
}

// Close grava o que ainda estiver na fila e encerra a goroutine de gravação.
// Não fecha o store, que pertence a quem o criou.
func (r *Recorder) Close() {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		close(r.stop)
	})
	<-r.done
}
