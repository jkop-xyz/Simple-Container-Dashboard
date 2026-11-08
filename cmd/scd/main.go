// Command scd is the Simple Container Dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "scd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "serve":
		return serve(args)
	case "version":
		fmt.Println("scd", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (commands: serve, version)", cmd)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	defaultPath, _ := config.DefaultPath()
	cfgPath := fs.String("config", defaultPath, "path to the config file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if errors.Is(err, config.ErrNotFound) {
		return fmt.Errorf("no config at %s; run `scd setup` first", *cfgPath)
	}
	if err != nil {
		return err
	}

	a, err := app.New(app.Options{Config: cfg})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: cfg.Listen, Handler: a.Handler, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("scd listening", "addr", cfg.Listen, "runtime", a.Runtime, "socket", a.Socket, "version", version)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
