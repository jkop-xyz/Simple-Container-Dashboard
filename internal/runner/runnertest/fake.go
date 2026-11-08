// Package runnertest provides a recording, scriptable fake of runner.Runner.
package runnertest

import (
	"context"
	"strings"
	"sync"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
)

// Response is a scripted outcome for a matching command.
type Response struct {
	ExitCode int
	Output   string
	// Err simulates a process that failed to start (e.g. binary not found).
	Err error
}

type rule struct {
	prefix string
	resp   Response
}

// Fake records every command it is asked to run and answers from scripted rules.
// Commands with no matching rule succeed with empty output.
type Fake struct {
	mu    sync.Mutex
	rules []rule
	calls []runner.Cmd
}

// On scripts the response for any command whose command line starts with prefix,
// e.g. On("docker compose -p app up", ...). Later rules take precedence.
func (f *Fake) On(prefix string, resp Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{prefix: prefix, resp: resp})
}

// Run implements runner.Runner.
func (f *Fake) Run(_ context.Context, cmd runner.Cmd) (runner.Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	resp := Response{}
	line := cmd.String()
	for i := len(f.rules) - 1; i >= 0; i-- {
		if strings.HasPrefix(line, f.rules[i].prefix) {
			resp = f.rules[i].resp
			break
		}
	}
	f.mu.Unlock()

	if resp.Err != nil {
		return runner.Result{}, resp.Err
	}
	if cmd.Output != nil && resp.Output != "" {
		cmd.Output.Write([]byte(resp.Output))
	}
	res := runner.Result{ExitCode: resp.ExitCode, Output: []byte(resp.Output)}
	if resp.ExitCode != 0 {
		return res, &runner.ExitError{Cmd: cmd, Result: res}
	}
	return res, nil
}

// Calls returns every command run so far, in order.
func (f *Fake) Calls() []runner.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runner.Cmd(nil), f.calls...)
}

// Lines returns the command lines run so far, in order.
func (f *Fake) Lines() []string {
	var out []string
	for _, c := range f.Calls() {
		out = append(out, c.String())
	}
	return out
}
