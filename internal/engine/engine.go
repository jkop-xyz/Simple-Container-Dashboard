// Package engine runs Sync (fetch, check, plan) and, later, Apply. Only one operation
// runs at a time; a request that arrives while one is running joins it.
package engine

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/plan"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/repo"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

// syncTimeout bounds one Sync, independent of the request that started it.
const syncTimeout = 5 * time.Minute

// Engine owns the current Sync Plan.
type Engine struct {
	Repo  *repo.Repo
	State *state.Store

	mu       sync.Mutex
	inflight *call
	plan     *plan.Plan
	lastErr  error
}

type call struct {
	done chan struct{}
	plan *plan.Plan
	err  error
}

// Status is what the dashboard shows about Sync.
type Status struct {
	// Plan is the latest Sync Plan, or nil.
	Plan *plan.Plan
	// Stale is set when the selection changed after the plan was computed.
	Stale bool
	// Err is why the latest Sync failed or was refused, or nil.
	Err error
	// Running is set while a Sync is in progress.
	Running bool
}

// Status reports the current Sync state.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{Plan: e.plan, Err: e.lastErr, Running: e.inflight != nil}
	if e.plan != nil {
		if st, err := e.State.Get(); err == nil {
			s.Stale = plan.SelectionKey(st.Selected) != e.plan.Selection
		}
	}
	return s
}

// Sync fetches the Deployment Repo and computes a Sync Plan. It changes nothing on the
// host except the clone's remote-tracking refs. Concurrent calls share one run.
func (e *Engine) Sync(ctx context.Context) (*plan.Plan, error) {
	e.mu.Lock()
	c := e.inflight
	if c == nil {
		c = &call{done: make(chan struct{})}
		e.inflight = c
		go e.runSync(c)
	}
	e.mu.Unlock()

	select {
	case <-c.done:
		return c.plan, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *Engine) runSync(c *call) {
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	defer cancel()
	c.plan, c.err = e.sync(ctx)
	if c.err != nil {
		slog.Error("Sync failed", "err", c.err)
	} else {
		slog.Info("Sync planned", "from", c.plan.From, "to", c.plan.To, "entries", len(c.plan.Entries))
	}

	e.mu.Lock()
	e.inflight = nil
	e.lastErr = c.err
	if c.err == nil {
		e.plan = c.plan
	} else {
		e.plan = nil
	}
	e.mu.Unlock()
	close(c.done)
}

func (e *Engine) sync(ctx context.Context) (*plan.Plan, error) {
	remote, err := e.Repo.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	if err := e.Repo.CheckClean(ctx, remote); err != nil {
		return nil, err
	}
	head, err := e.Repo.Head(ctx)
	if err != nil {
		return nil, err
	}
	commits, err := e.Repo.Log(ctx, head, remote)
	if err != nil {
		return nil, err
	}
	files, err := e.Repo.Files(ctx, remote)
	if err != nil {
		return nil, err
	}
	trees, err := e.Repo.Trees(ctx, remote)
	if err != nil {
		return nil, err
	}
	st, err := e.State.Get()
	if err != nil {
		return nil, err
	}
	return &plan.Plan{
		From:    head,
		To:      remote,
		Commits: commits,
		Entries: plan.Entries(plan.Input{
			Stacks:   stacks.Discover(files),
			Trees:    trees,
			Selected: st.Selected,
			Deployed: st.Deployed,
		}),
		Selection: plan.SelectionKey(st.Selected),
	}, nil
}
