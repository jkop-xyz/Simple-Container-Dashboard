package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/deploy"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/plan"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

// applyTimeout bounds one Apply, independent of the request that started it.
const applyTimeout = 30 * time.Minute

// Errors that keep Apply from starting.
var (
	ErrNoPlan       = errors.New("there is no Sync Plan to apply; press Sync first")
	ErrStalePlan    = errors.New("the selection changed after the last Sync; press Sync again before applying")
	ErrNothingToDo  = errors.New("the Sync Plan has nothing to apply")
	ErrSyncRunning  = errors.New("a Sync is running; wait for it to finish")
	ErrApplyRunning = errors.New("an Apply is running; wait for it to finish")
)

// StackResult is the outcome of one Stack in an Apply.
type StackResult struct {
	Stack  string
	Action plan.Action
	// Err is the failure text, or empty on success.
	Err string
}

// ApplyRun is one Apply: its live output and, once finished, its results.
type ApplyRun struct {
	Plan    *plan.Plan
	Started time.Time
	Output  *Log

	// Done is closed when the Apply has finished.
	Done chan struct{}

	mu       sync.Mutex
	finished time.Time
	results  []StackResult
	err      error
}

// Results returns the per-Stack outcomes so far.
func (r *ApplyRun) Results() []StackResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StackResult(nil), r.results...)
}

// Err is set when Apply stopped before deploying anything (e.g. the fast-forward was
// refused).
func (r *ApplyRun) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Finished is when the Apply ended (zero while running).
func (r *ApplyRun) Finished() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

func (r *ApplyRun) setErr(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

// Failed lists the Stacks that failed.
func (r *ApplyRun) Failed() []StackResult {
	var out []StackResult
	for _, res := range r.Results() {
		if res.Err != "" {
			out = append(out, res)
		}
	}
	return out
}

// Running reports whether the Apply is still in progress.
func (r *ApplyRun) Running() bool {
	select {
	case <-r.Done:
		return false
	default:
		return true
	}
}

// Apply carries out the current Sync Plan. It returns once the Apply has started (or, if
// one is already running, returns that one); follow its Output for progress.
func (e *Engine) Apply() (*ApplyRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.apply != nil && e.apply.Running() {
		return e.apply, nil
	}
	if err := e.checkApplicable(); err != nil {
		e.applyErr = err
		return nil, err
	}
	run := &ApplyRun{Plan: e.plan, Started: time.Now(), Output: newLog(), Done: make(chan struct{})}
	e.apply = run
	e.applyErr = nil
	e.plan = nil // a plan is applied at most once
	go e.runApply(run)
	return run, nil
}

// checkApplicable is the Apply guard. Call with e.mu held.
func (e *Engine) checkApplicable() error {
	if e.inflight != nil {
		return ErrSyncRunning
	}
	if e.plan == nil {
		return ErrNoPlan
	}
	st, err := e.State.Get()
	if err != nil {
		return err
	}
	if plan.SelectionKey(st.Selected) != e.plan.Selection {
		return ErrStalePlan
	}
	if e.plan.Blocked != "" {
		return errors.New(e.plan.Blocked)
	}
	if !e.plan.HasChanges() {
		return ErrNothingToDo
	}
	return nil
}

// LastApply returns the running or most recent Apply, or nil.
func (e *Engine) LastApply() *ApplyRun {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.apply
}

func (e *Engine) runApply(run *ApplyRun) {
	ctx, cancel := context.WithTimeout(context.Background(), applyTimeout)
	defer cancel()
	defer func() {
		run.mu.Lock()
		run.finished = time.Now()
		run.mu.Unlock()
		run.Output.Close()
		close(run.Done)
	}()
	p, out := run.Plan, run.Output

	out.line(fmt.Sprintf("==> Fast-forwarding the clone to %s", short(p.To)))
	if err := e.Repo.CheckClean(ctx, p.To); err != nil {
		run.setErr(err)
		out.line("!!! " + err.Error())
		slog.Error("Apply refused", "err", err)
		return
	}
	if err := e.Repo.FastForward(ctx, p.To); err != nil {
		run.setErr(err)
		out.line("!!! " + err.Error())
		slog.Error("Apply fast-forward failed", "err", err)
		return
	}

	for _, entry := range p.Entries {
		if !entry.Action.Changes() {
			continue
		}
		res := StackResult{Stack: entry.Stack, Action: entry.Action}
		out.line(fmt.Sprintf("==> %s %s (%s)", entry.Action, entry.Stack, entry.Reason))
		if err := e.applyEntry(ctx, entry, out); err != nil {
			res.Err = err.Error()
			out.line(fmt.Sprintf("!!! %s %s failed: %v", entry.Action, entry.Stack, err))
			slog.Error("Apply step failed", "stack", entry.Stack, "action", entry.Action, "err", err)
		} else {
			slog.Info("Apply step done", "stack", entry.Stack, "action", entry.Action)
		}
		run.mu.Lock()
		run.results = append(run.results, res)
		run.mu.Unlock()
		if err := e.record(entry, res); err != nil {
			out.line("!!! saving state: " + err.Error())
		}
	}

	if failed := len(run.Failed()); failed > 0 {
		out.line(fmt.Sprintf("==> Apply finished with %d failed Stack(s)", failed))
	} else {
		out.line("==> Apply finished")
	}
}

func (e *Engine) applyEntry(ctx context.Context, entry plan.Entry, out *Log) error {
	d, ok := e.Deployers[entry.Kind]
	if !ok {
		return fmt.Errorf("no deployer for %s Stacks on this host", entry.Kind)
	}
	s := stacks.Stack{Name: entry.Stack, Kind: entry.Kind}
	if entry.Action == plan.Teardown {
		return d.Teardown(ctx, s, out)
	}
	return d.Deploy(ctx, s, out)
}

// record saves the outcome so the next Sync plans from it: successful deploys store the
// new tree, failures are marked for retry, successful teardowns forget the Stack.
func (e *Engine) record(entry plan.Entry, res StackResult) error {
	return e.State.Update(func(st *state.State) {
		if st.Deployed == nil {
			st.Deployed = map[string]state.DeployedStack{}
		}
		prev := st.Deployed[entry.Stack]
		switch {
		case entry.Action == plan.Teardown && res.Err == "":
			delete(st.Deployed, entry.Stack)
		case entry.Action == plan.Teardown:
			prev.Failed = true
			st.Deployed[entry.Stack] = prev
		case res.Err == "":
			st.Deployed[entry.Stack] = state.DeployedStack{Kind: entry.Kind, Tree: entry.Tree, Secrets: entry.Secrets}
		default:
			st.Deployed[entry.Stack] = state.DeployedStack{Kind: entry.Kind, Tree: entry.Tree, Secrets: entry.Secrets, Failed: true}
		}
	})
}

func short(commit string) string {
	if len(commit) > 7 {
		return commit[:7]
	}
	return commit
}

// Deployers is the set of deployers by Stack kind; see deploy.Compose.
type Deployers = map[stacks.Kind]deploy.Deployer
