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
	"os/exec"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/setup"
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
	case "setup":
		return runSetup()
	case "version":
		fmt.Println("scd", version)
		return nil
	default:
		return fmt.Errorf("unknown command %q (commands: serve, setup, version)", cmd)
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

	a, err := app.New(app.Options{Config: cfg, Version: version})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	_ = a.Start(ctx) // failures are logged and shown on the dashboard

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

func runSetup() error {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	confDir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	tmp, err := os.MkdirTemp("", "scd-setup-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	osRelease, _ := os.ReadFile("/etc/os-release")

	return setup.Run(context.Background(), setup.Env{
		Runner:     runner.Exec{},
		Prompt:     setup.NewTerminal(os.Stdin, os.Stdout),
		Out:        os.Stdout,
		Stdin:      os.Stdin,
		LookPath:   exec.LookPath,
		OSRelease:  string(osRelease),
		ConfigPath: cfgPath,
		UnitDir:    filepath.Join(confDir, "systemd", "user"),
		DataDir:    dataDir,
		Binary:     bin,
		TempDir:    tmp,
		User:       username(),
	})
}

func username() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return os.Getenv("USER")
}
