// Package app wires up the process: it loads the configuration and brings up
// the traffic and admin ports.
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/joaovillas/devgateway/internal/admin"
	"github.com/joaovillas/devgateway/internal/capture"
	"github.com/joaovillas/devgateway/internal/config"
	"github.com/joaovillas/devgateway/internal/config/writer"
	"github.com/joaovillas/devgateway/internal/learn"
	"github.com/joaovillas/devgateway/internal/override"
	"github.com/joaovillas/devgateway/internal/proxy"
	"github.com/joaovillas/devgateway/internal/store"
	"github.com/joaovillas/devgateway/internal/upstream"
)

// App is the running process.
type App struct {
	Live *config.Live
	// History is the exchange history, with a hot-swappable backend.
	History *store.Switchable
	// Recorder records the traffic port's exchanges into the history in use
	// and publishes them on the real-time broker.
	Recorder *capture.Recorder
	// Writer serializes route document writes and applies the result live;
	// it is shared by learning and by the API.
	Writer *writer.Writer
	// Overrides holds the live state of the overrides: remaining lifetime
	// and application count.
	Overrides *override.Tracker
	// Upstreams is the recent availability of the upstreams, counted by the
	// traffic port and read by the API.
	Upstreams *upstream.Health
	// Learner writes the endpoints learned while learning mode is on.
	Learner *learn.Learner
	Log     *slog.Logger

	// traffic and admin are the two ports, under the listener supervisor.
	traffic, admin *port
	serveErr       chan error
}

// Options gathers what the process receives from outside.
type Options struct {
	Loader config.Loader
	Web    fs.FS
	Log    *slog.Logger
	// Heartbeat is the interval of the API event stream's heartbeat; zero
	// uses the default of 15 s.
	Heartbeat time.Duration
}

// Start loads the configuration and opens the two ports. It fails without
// leaving any port open when the configuration is invalid or a port is
// already taken.
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

	// The history backend opens before the ports: if it fails, the process
	// refuses to start instead of silently falling back to another backend.
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
	a.Upstreams = upstream.New()
	a.Learner = learn.New(a.Writer, log)
	a.traffic = &port{
		name: "traffic", key: "ports.traffic", fatal: a.fatal,
		handler: proxy.NewHandlerWith(a.Live, a.Recorder, proxy.Options{
			Tracker:   a.Overrides,
			Learner:   a.Learner,
			Upstreams: a.Upstreams,
		}),
	}
	a.admin = &port{
		name: "admin", key: "ports.admin", fatal: a.fatal,
		handler: admin.New(admin.Deps{
			Live:      a.Live,
			Web:       opts.Web,
			History:   a.History,
			Recorder:  a.Recorder,
			Writer:    a.Writer,
			Overrides: a.Overrides,
			Upstreams: a.Upstreams,
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
		return nil, fmt.Errorf("opening traffic port %d: %w", s.TrafficPort, err)
	}
	ab, err := a.admin.open(s.AdminPort)
	if err != nil {
		tb.abort()
		a.closeWorkers()
		return nil, fmt.Errorf("opening admin port %d: %w", s.AdminPort, err)
	}
	a.traffic.commit(tb)
	a.admin.commit(ab)
	log.Info("gateway is up",
		"traffic", a.TrafficAddr(), "admin", a.AdminAddr(), "routes", len(snap.Routes), "history", a.History.Backend())
	return a, nil
}

func (a *App) closeWorkers() {
	a.Learner.Close()
	a.Recorder.Close()
	a.History.Close()
}

// ports reports the ports the process is serving on right now.
func (a *App) ports() (traffic, adminPort int) { return a.traffic.number(), a.admin.number() }

// applySettings applies live whatever the process configuration controls
// outside the snapshot, before the new snapshot is published. The remaining
// values (seed, history recording and exposure, capture limit, learning,
// routes directory) are read from the snapshot on every request and take
// effect when it is swapped.
//
// Everything is prepared before anything is swapped: the listeners of the
// changed ports are opened and the new history backend is initialized. If
// anything fails, whatever was prepared is discarded and nothing changes.
// Then persist writes the configuration (gateway.json, on a change made
// through the API); if that fails, nothing changes either. Only then is the
// history backend swapped — without migrating earlier exchanges — and the
// ports start serving on the new listeners, while the old servers finish the
// requests in flight.
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
				Message: fmt.Sprintf("port %d unavailable: %v; the %s port stays on %d",
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
				Message: fmt.Sprintf("%v; history stays on %s", err, a.History.Backend()),
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
			a.Log.Warn("history backend swapped, but the previous one did not close", "error", err)
		}
		a.Log.Info("history backend swapped; earlier exchanges were not migrated",
			"from", store.BackendName(old), "to", store.BackendName(next))
	}
	for _, o := range ready {
		from := o.p.number()
		if !o.p.commit(o.b) {
			a.Log.Warn("port swap discarded: the process is shutting down",
				"port", o.p.name, "stays on", from)
			continue
		}
		a.Log.Info("port swapped live; requests in flight finish on the previous one",
			"port", o.p.name, "from", from, "to", o.p.number())
	}
	return nil
}

// fatal hands over the error of a server that stopped serving on its own.
// Only the first one matters to whoever is waiting on Err.
func (a *App) fatal(err error) {
	select {
	case a.serveErr <- err:
	default:
	}
}

// Err hands over the first fatal error from one of the servers.
func (a *App) Err() <-chan error { return a.serveErr }

// TrafficAddr and AdminAddr are the addresses the ports are serving on right
// now, which change with a live swap.
func (a *App) TrafficAddr() string { return a.traffic.addr().String() }
func (a *App) AdminAddr() string   { return a.admin.addr().String() }

// Shutdown closes both ports, waiting for the requests in flight — including
// those still finishing on a replaced port — and then closes the history.
func (a *App) Shutdown(ctx context.Context) error {
	err := errors.Join(a.traffic.shutdown(ctx), a.admin.shutdown(ctx))
	// With the ports closed no new exchange arrives. The server's Shutdown
	// does not wait for hijacked connections (protocol upgrades): their
	// records are waited for here, up to ctx's deadline. One that closes
	// after that is dropped with a warning in the log. Whatever is queued is
	// written before the history closes.
	if werr := a.Recorder.Wait(ctx); werr != nil && err == nil {
		err = fmt.Errorf("waiting for the exchanges in flight: %w", werr)
	}
	a.Recorder.Close()
	// The endpoints already queued by learning are written before shutting
	// down.
	a.Learner.Close()
	return errors.Join(err, a.History.Close())
}

// cause returns the cause of a network failure without the operation and the
// address, which the message already carries: "bind: address already in
// use".
func cause(err error) error {
	if u := errors.Unwrap(err); u != nil {
		return u
	}
	return err
}
