// Package app monta o processo: carrega a configuração e sobe as portas de
// tráfego e de administração.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/gamerjp64/gateway/internal/admin"
	"github.com/gamerjp64/gateway/internal/capture"
	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/config/writer"
	"github.com/gamerjp64/gateway/internal/learn"
	"github.com/gamerjp64/gateway/internal/override"
	"github.com/gamerjp64/gateway/internal/proxy"
	"github.com/gamerjp64/gateway/internal/store"
)

// App é o processo em execução.
type App struct {
	Live *config.Live
	// History é o histórico de trocas, com backend trocável a quente.
	History *store.Switchable
	// Recorder registra as trocas da porta de tráfego no histórico em uso e
	// as publica no broker de tempo real.
	Recorder *capture.Recorder
	// Writer serializa as escritas de documento de rota e aplica o resultado
	// a quente; é compartilhado pelo aprendizado e pela API.
	Writer *writer.Writer
	// Overrides guarda o estado vivo dos overrides: tempo de vida restante e
	// contagem de aplicações.
	Overrides *override.Tracker
	// Learner grava os endpoints aprendidos com o modo aprendizado ligado.
	Learner *learn.Learner
	Log     *slog.Logger

	// traffic e admin são as duas portas, sob o supervisor de listeners.
	traffic, admin *port
	serveErr       chan error
}

// Options reúne o que o processo recebe de fora.
type Options struct {
	Loader config.Loader
	Web    fs.FS
	Log    *slog.Logger
	// Heartbeat é o intervalo do heartbeat do fluxo de eventos da API; zero
	// usa o padrão de 15 s.
	Heartbeat time.Duration
}

// Start carrega a configuração e abre as duas portas. Falha sem deixar
// nenhuma porta aberta quando a configuração é inválida ou uma porta está
// ocupada.
func Start(opts Options) (*App, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	snap, err := opts.Loader.Load()
	if err != nil {
		return nil, err
	}
	for _, w := range snap.Warnings {
		log.Warn(w)
	}
	s := snap.Settings

	// O backend do histórico abre antes das portas: se falhar, o processo recusa
	// iniciar em vez de cair silenciosamente para outro backend.
	hist, err := store.Open(s)
	if err != nil {
		return nil, err
	}
	a := &App{
		Live:     config.NewLive(snap),
		History:  store.NewSwitchable(store.BackendName(s), hist),
		Log:      log,
		serveErr: make(chan error, 1),
	}
	a.Recorder = capture.NewRecorder(a.History, capture.NewBroker(), log)
	a.Writer = writer.New(a.Live)
	a.Overrides = override.NewTracker(a.Live)
	a.Learner = learn.New(a.Writer, log)
	a.traffic = &port{
		name: "tráfego", key: "ports.traffic", fatal: a.fatal,
		handler: proxy.NewHandlerWith(a.Live, a.Recorder, proxy.Options{
			Tracker: a.Overrides,
			Learner: a.Learner,
		}),
	}
	a.admin = &port{
		name: "administração", key: "ports.admin", fatal: a.fatal,
		handler: admin.New(admin.Deps{
			Live:      a.Live,
			Web:       opts.Web,
			History:   a.History,
			Recorder:  a.Recorder,
			Writer:    a.Writer,
			Overrides: a.Overrides,
			Loader:    opts.Loader,
			Apply:     a.applySettings,
			Ports:     a.ports,
			StartedAt: time.Now(),
			Heartbeat: opts.Heartbeat,
		}),
		baseContext: func(stop <-chan struct{}) context.Context {
			return admin.StreamContext(context.Background(), stop)
		},
	}
	tb, err := a.traffic.open(s.TrafficPort)
	if err != nil {
		a.closeWorkers()
		return nil, fmt.Errorf("abrindo a porta de tráfego %d: %w", s.TrafficPort, err)
	}
	ab, err := a.admin.open(s.AdminPort)
	if err != nil {
		tb.abort()
		a.closeWorkers()
		return nil, fmt.Errorf("abrindo a porta de administração %d: %w", s.AdminPort, err)
	}
	a.traffic.commit(tb)
	a.admin.commit(ab)
	log.Info("gateway no ar",
		"trafego", a.TrafficAddr(), "administracao", a.AdminAddr(), "rotas", len(snap.Routes), "historico", a.History.Backend())
	return a, nil
}

func (a *App) closeWorkers() {
	a.Learner.Close()
	a.Recorder.Close()
	a.History.Close()
}

// ports informa as portas em que o processo atende agora.
func (a *App) ports() (traffic, adminPort int) { return a.traffic.number(), a.admin.number() }

// applySettings aplica a quente o que a configuração do processo controla
// fora do snapshot, antes de o snapshot novo ser publicado. Os demais
// valores (seed, registro e exposição do histórico, limite de captura,
// aprendizado, diretório de rotas) são lidos do snapshot a cada requisição e
// passam a valer com a troca dele.
//
// Tudo é preparado antes de qualquer troca: os listeners das portas
// alteradas são abertos e o backend novo do histórico é inicializado. Se algo
// falha, o que foi preparado é descartado e nada muda. Depois persist grava
// a configuração (gateway.json, numa alteração pela API); se falhar, também
// nada muda. Só então o backend do histórico é trocado — sem migrar as
// trocas anteriores — e as portas passam a atender nos listeners novos,
// enquanto os servidores antigos concluem as requisições em curso.
func (a *App) applySettings(old, next config.Settings, persist func() error) error {
	type opened struct {
		p *port
		b *binding
	}
	var ready []opened
	undo := func() {
		for _, o := range ready {
			o.b.abort()
		}
	}
	for _, p := range []struct {
		p        *port
		from, to int
	}{
		{a.traffic, old.TrafficPort, next.TrafficPort},
		{a.admin, old.AdminPort, next.AdminPort},
	} {
		if p.from == p.to {
			continue
		}
		b, err := p.p.open(p.to)
		if err != nil {
			undo()
			return &admin.ApplyError{
				Code:  "port_unavailable",
				Field: p.p.key,
				Message: fmt.Sprintf("porta %d indisponível: %v; a porta de %s segue na %d",
					p.to, cause(err), p.p.name, p.p.number()),
			}
		}
		ready = append(ready, opened{p.p, b})
	}
	var hist store.Store
	if store.Reopens(old, next) {
		st, err := store.Open(next)
		if err != nil {
			undo()
			return &admin.ApplyError{
				Code:    "backend_unavailable",
				Field:   "history.backend",
				Message: fmt.Sprintf("%v; o histórico segue em %s", err, a.History.Backend()),
			}
		}
		hist = st
	}
	if persist != nil {
		if err := persist(); err != nil {
			undo()
			if hist != nil {
				hist.Close()
			}
			return err
		}
	}
	if hist != nil {
		if err := a.History.Swap(context.Background(), store.BackendName(next), hist); err != nil {
			a.Log.Warn("backend do histórico trocado, mas o anterior não fechou", "erro", err)
		}
		a.Log.Info("backend do histórico trocado; as trocas anteriores não foram migradas",
			"de", store.BackendName(old), "para", store.BackendName(next))
	}
	for _, o := range ready {
		from := o.p.number()
		if !o.p.commit(o.b) {
			a.Log.Warn("troca de porta descartada: o processo está encerrando",
				"porta", o.p.name, "segue na", from)
			continue
		}
		a.Log.Info("porta trocada a quente; as requisições em curso terminam na anterior",
			"porta", o.p.name, "de", from, "para", o.p.number())
	}
	return nil
}

// fatal entrega o erro de um servidor que parou de atender por conta própria.
// Só o primeiro interessa a quem espera em Err.
func (a *App) fatal(err error) {
	select {
	case a.serveErr <- err:
	default:
	}
}

// Err entrega o primeiro erro fatal de um dos servidores.
func (a *App) Err() <-chan error { return a.serveErr }

// TrafficAddr e AdminAddr são os endereços em que as portas atendem agora,
// que mudam com a troca a quente.
func (a *App) TrafficAddr() string { return a.traffic.addr().String() }
func (a *App) AdminAddr() string   { return a.admin.addr().String() }

// Shutdown encerra as duas portas, aguardando as requisições em curso —
// inclusive as que ainda terminam numa porta substituída —, e depois fecha
// o histórico.
func (a *App) Shutdown(ctx context.Context) error {
	err := errors.Join(a.traffic.shutdown(ctx), a.admin.shutdown(ctx))
	// Com as portas fechadas nenhuma troca nova chega. O Shutdown do
	// servidor não espera as conexões sequestradas (upgrades de protocolo):
	// os registros delas são esperados aqui, até o prazo de ctx. Um que
	// feche depois disso é descartado com aviso no log. O que está na fila é
	// gravado antes de o histórico fechar.
	if werr := a.Recorder.Wait(ctx); werr != nil && err == nil {
		err = fmt.Errorf("aguardando as trocas em curso: %w", werr)
	}
	a.Recorder.Close()
	// Os endpoints já enfileirados pelo aprendizado são gravados antes de
	// encerrar.
	a.Learner.Close()
	return errors.Join(err, a.History.Close())
}

// cause devolve a causa de uma falha de rede sem a operação e o endereço, que
// a mensagem já informa: "bind: address already in use".
func cause(err error) error {
	if u := errors.Unwrap(err); u != nil {
		return u
	}
	return err
}
