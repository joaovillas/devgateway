// Comando gateway: proxy reverso de desenvolvimento com overrides e captura.
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

	"github.com/gamerjp64/gateway"
	"github.com/gamerjp64/gateway/internal/app"
	"github.com/gamerjp64/gateway/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gateway:", err)
		os.Exit(1)
	}
}

func run() error {
	loader := config.DefaultLoader()
	flag.StringVar(&loader.ConfigPath, "config", loader.ConfigPath,
		"caminho do gateway.json (também via "+config.EnvConfigPath+")")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	a, err := app.Start(app.Options{Loader: loader, Web: gateway.WebFS(), Log: log})
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
	log.Info("encerrando")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return a.Shutdown(shutdownCtx)
}
