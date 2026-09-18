// Package app monta o processo: carrega a configuração e sobe as portas de
// tráfego e de administração.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gamerjp64/gateway/internal/admin"
	"github.com/gamerjp64/gateway/internal/config"
	"github.com/gamerjp64/gateway/internal/proxy"
)

// App é o processo em execução.
type App struct {
	Live *config.Live
	Log  *slog.Logger

	traffic, admin     *http.Server
	trafficLn, adminLn net.Listener
	serveErr           chan error
}

// Options reúne o que o processo recebe de fora.
type Options struct {
	Loader config.Loader
	Web    fs.FS
	Log    *slog.Logger
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

	a := &App{Live: config.NewLive(snap), Log: log, serveErr: make(chan error, 2)}
	if a.trafficLn, err = listen("tráfego", s.TrafficPort); err != nil {
		return nil, err
	}
	if a.adminLn, err = listen("administração", s.AdminPort); err != nil {
		a.trafficLn.Close()
		return nil, err
	}
	a.traffic = &http.Server{
		Handler:           proxy.NewHandler(a.Live),
		ReadHeaderTimeout: 10 * time.Second,
	}
	a.admin = &http.Server{
		Handler:           admin.NewHandler(a.Live, opts.Web),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go a.serve(a.traffic, a.trafficLn)
	go a.serve(a.admin, a.adminLn)
	log.Info("gateway no ar",
		"trafego", a.TrafficAddr(), "administracao", a.AdminAddr(), "rotas", len(snap.Routes))
	return a, nil
}

func listen(name string, port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, fmt.Errorf("abrindo a porta de %s %d: %w", name, port, err)
	}
	return ln, nil
}

func (a *App) serve(s *http.Server, ln net.Listener) {
	if err := s.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		a.serveErr <- err
	}
}

// Err entrega o primeiro erro fatal de um dos servidores.
func (a *App) Err() <-chan error { return a.serveErr }

func (a *App) TrafficAddr() string { return a.trafficLn.Addr().String() }
func (a *App) AdminAddr() string   { return a.adminLn.Addr().String() }

// Shutdown encerra as duas portas, aguardando as requisições em curso.
func (a *App) Shutdown(ctx context.Context) error {
	return errors.Join(a.traffic.Shutdown(ctx), a.admin.Shutdown(ctx))
}
