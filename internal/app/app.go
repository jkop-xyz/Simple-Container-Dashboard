// Package app wires scd's pieces together from a config. It is the seam tests use to
// start the real dashboard in-process.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/deploy"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/engine"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/repo"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/web"
)

// Options are the outside-world dependencies of the app. Zero values mean "the real thing".
type Options struct {
	Config config.Config
	// Env is used for runtime socket detection.
	Env *dockerapi.Env
	// Runner runs external processes.
	Runner runner.Runner
	// Hostname is shown in the header. Defaults to the OS hostname.
	Hostname string
	// DataDir holds scd's state file. Defaults to config.DataDir().
	DataDir string
}

// App is a wired-up dashboard.
type App struct {
	Handler http.Handler
	Runtime config.Runtime
	Socket  string

	repo   *repo.Repo
	cloner *cloner
}

// HostEnv is the real host environment for socket detection.
var HostEnv = dockerapi.Env{
	Getenv: os.Getenv,
	Exists: func(p string) bool { _, err := os.Stat(p); return err == nil },
}

// New builds the app. It performs no I/O beyond socket detection; call Start to clone.
func New(opts Options) (*App, error) {
	env := HostEnv
	if opts.Env != nil {
		env = *opts.Env
	}
	if opts.Runner == nil {
		opts.Runner = runner.Exec{}
	}
	if opts.Hostname == "" {
		opts.Hostname, _ = os.Hostname()
	}
	if opts.DataDir == "" {
		dir, err := config.DataDir()
		if err != nil {
			return nil, err
		}
		opts.DataDir = dir
	}
	socket, rt, err := dockerapi.DetectSocket(opts.Config.Runtime, opts.Config.Socket, env)
	if err != nil {
		return nil, err
	}

	a := &App{Runtime: rt, Socket: socket}
	deps := web.Deps{
		Runtime:  dockerapi.New(socket),
		Hostname: opts.Hostname,
		State:    state.Open(filepath.Join(opts.DataDir, "state.json")),
	}
	if rc := opts.Config.Repo; rc.URL != "" {
		a.repo = &repo.Repo{Runner: opts.Runner, URL: rc.URL, Branch: rc.Branch, Path: rc.Path}
		a.cloner = &cloner{repo: a.repo, retryEvery: time.Minute}
		deps.Repo = a.repo
		deps.RepoName = repo.DisplayName(rc.URL)
		deps.RepoError = a.cloner.check
		deployers := engine.Deployers{}
		if rt == config.Docker {
			deployers[stacks.Compose] = deploy.Compose{Runner: opts.Runner, RepoPath: rc.Path}
		}
		deps.Engine = &engine.Engine{Repo: a.repo, State: deps.State, Deployers: deployers}
	}
	a.Handler = web.New(deps)
	return a, nil
}

// Start clones the Deployment Repo if the clone is missing. A failed clone is reported
// on the dashboard and retried later, so Start only returns an error for logging.
func (a *App) Start(ctx context.Context) error {
	if a.cloner == nil {
		return nil
	}
	return a.cloner.clone(ctx)
}

// cloner (re)creates a missing clone, at most once per retryEvery.
type cloner struct {
	repo       *repo.Repo
	retryEvery time.Duration

	mu      sync.Mutex
	lastTry time.Time
	lastErr error
}

func (c *cloner) clone(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastTry = time.Now()
	c.lastErr = c.repo.EnsureClone(ctx)
	if c.lastErr != nil {
		slog.Error("Deployment Repo clone failed", "url", c.repo.URL, "path", c.repo.Path, "err", c.lastErr)
	}
	return c.lastErr
}

// check reports why the clone is unavailable, retrying a missing clone when due.
func (c *cloner) check() error {
	if c.repo.Exists() {
		return nil
	}
	c.mu.Lock()
	due := time.Since(c.lastTry) >= c.retryEvery
	err := c.lastErr
	c.mu.Unlock()
	if due || err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return c.clone(ctx)
	}
	return err
}
