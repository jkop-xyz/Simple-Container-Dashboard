// Package app wires scd's pieces together from a config. It is the seam tests use to
// start the real dashboard in-process.
package app

import (
	"net/http"
	"os"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/web"
)

// Options are the outside-world dependencies of the app. Zero values mean "the real thing".
type Options struct {
	Config config.Config
	// Env is used for runtime socket detection.
	Env *dockerapi.Env
	// Runner runs external processes.
	Runner runner.Runner
}

// App is a wired-up dashboard.
type App struct {
	Handler http.Handler
	Runtime config.Runtime
	Socket  string
}

// HostEnv is the real host environment for socket detection.
var HostEnv = dockerapi.Env{
	Getenv: os.Getenv,
	Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
}

// New builds the app.
func New(opts Options) (*App, error) {
	env := HostEnv
	if opts.Env != nil {
		env = *opts.Env
	}
	if opts.Runner == nil {
		opts.Runner = runner.Exec{}
	}
	socket, rt, err := dockerapi.DetectSocket(opts.Config.Runtime, opts.Config.Socket, env)
	if err != nil {
		return nil, err
	}
	client := dockerapi.New(socket)
	return &App{
		Handler: web.New(client),
		Runtime: rt,
		Socket:  socket,
	}, nil
}
