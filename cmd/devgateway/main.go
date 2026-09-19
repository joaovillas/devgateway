// Command devgateway is a development reverse proxy with overrides and capture.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joaovillas/devgateway"
	"github.com/joaovillas/devgateway/internal/app"
	"github.com/joaovillas/devgateway/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "devgateway:", err)
		os.Exit(1)
	}
}

func run() error {
	loader := config.DefaultLoader()
	flag.StringVar(&loader.ConfigPath, "config", loader.ConfigPath,
		"path to gateway.json (also via "+config.EnvConfigPath+")")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	a, err := app.Start(app.Options{Loader: loader, Web: devgateway.WebFS(), Log: log})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err := <-a.Err():
		return err
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.Shutdown(shutdownCtx)
}
