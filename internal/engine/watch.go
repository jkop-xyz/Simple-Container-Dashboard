package engine

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/plan"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

// Default settle window after an Apply, and how often it samples.
const (
	DefaultWatchFor   = 60 * time.Second
	DefaultWatchEvery = 5 * time.Second
)

// ContainerSample is one container's condition at one moment.
type ContainerSample struct {
	ID       string
	State    string // "running", "exited", …
	Health   string // "healthy", "unhealthy", "starting" or "" (no healthcheck)
	Restarts int
}

// Probe reads the current containers of the given Stacks, keyed by Stack name.
type Probe interface {
	Sample(ctx context.Context, ss []stacks.Stack) (map[string][]ContainerSample, error)
}

// watched lists the Stacks of a finished Apply that should be watched: the ones it
// deployed or redeployed successfully.
func watched(run *ApplyRun) []string {
	var out []string
	for _, r := range run.Results() {
		if r.Err == "" && (r.Action == plan.Deploy || r.Action == plan.Redeploy) {
			out = append(out, r.Stack)
		}
	}
	return out
}

// watch samples the Stacks for the settle window, then records each one's verdict in
// the Apply's history entry.
func (e *Engine) watch(id string, names []string) {
	window, every := e.WatchFor, e.WatchEvery
	if window <= 0 {
		window = DefaultWatchFor
	}
	if every <= 0 {
		every = DefaultWatchEvery
	}
	ctx, cancel := context.WithTimeout(context.Background(), window+time.Minute)
	defer cancel()

	files, err := e.Repo.Files(ctx, "HEAD")
	if err != nil {
		slog.Error("post-Apply health check", "err", err)
		e.recordAfter(id, nil)
		return
	}
	var ss []stacks.Stack
	for _, s := range stacks.Discover(files, e.Runtime) {
		if slices.Contains(names, s.Name) {
			ss = append(ss, s)
		}
	}

	first := map[string]int{} // container → restart count when first seen
	rose := map[string]bool{} // Stack → a restart count went up
	var last map[string][]ContainerSample
	deadline := time.Now().Add(window)
	for {
		sample, err := e.Probe.Sample(ctx, ss)
		if err == nil {
			last = sample
			for stack, cs := range sample {
				for _, c := range cs {
					if n, ok := first[c.ID]; !ok {
						first[c.ID] = c.Restarts
					} else if c.Restarts > n {
						rose[stack] = true
					}
				}
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-time.After(min(every, time.Until(deadline))):
		case <-ctx.Done():
			e.recordAfter(id, nil)
			return
		}
	}

	verdicts := map[string]string{}
	for _, name := range names {
		verdicts[name] = verdict(last[name], rose[name])
	}
	slog.Info("post-Apply health check", "apply", id, "verdicts", verdicts)
	e.recordAfter(id, verdicts)
}

// verdict sums up a Stack's containers at the end of the window.
func verdict(cs []ContainerSample, restarted bool) string {
	if len(cs) == 0 {
		return state.AfterNotRunning
	}
	result := state.AfterHealthy
	for _, c := range cs {
		switch {
		case c.Health == "unhealthy":
			return state.AfterUnhealthy
		case c.State != "running":
			result = state.AfterNotRunning
		case c.Health == "starting" && result == state.AfterHealthy:
			result = state.AfterStarting
		}
	}
	if restarted && result != state.AfterNotRunning {
		return state.AfterRestarting
	}
	return result
}

// recordAfter stores the verdicts in the Apply's history entry and marks it checked.
func (e *Engine) recordAfter(id string, verdicts map[string]string) {
	err := e.State.Update(func(st *state.State) {
		for i := range st.History {
			if st.History[i].ID != id {
				continue
			}
			st.History[i].HealthCheck = "done"
			for j := range st.History[i].Results {
				if v, ok := verdicts[st.History[i].Results[j].Stack]; ok {
					st.History[i].Results[j].After = v
				}
			}
		}
	})
	if err != nil {
		slog.Error("saving post-Apply health check", "err", err)
	}
}
