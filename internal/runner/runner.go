// Package runner is the single way scd runs external processes (git, docker compose,
// podman, systemctl, ...). Production code uses Exec; tests use runnertest.Fake.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Cmd describes one process to run.
type Cmd struct {
	Name string
	Args []string
	Dir  string
	// Env is added to the inherited environment, as "KEY=value" entries.
	Env   []string
	Stdin io.Reader
	// Output, if set, receives combined stdout/stderr as it is produced.
	Output io.Writer
}

// String renders the command line, for logs and test assertions.
func (c Cmd) String() string {
	return strings.Join(append([]string{c.Name}, c.Args...), " ")
}

// Result is what a finished process produced.
type Result struct {
	ExitCode int
	// Output is the combined stdout/stderr.
	Output []byte
}

// ExitError is returned when a process ran but exited non-zero.
type ExitError struct {
	Cmd    Cmd
	Result Result
}

func (e *ExitError) Error() string {
	out := strings.TrimSpace(string(e.Result.Output))
	if out == "" {
		return fmt.Sprintf("%s: exit status %d", e.Cmd, e.Result.ExitCode)
	}
	return fmt.Sprintf("%s: exit status %d: %s", e.Cmd, e.Result.ExitCode, out)
}

// Runner runs external processes. A non-zero exit returns the Result and an *ExitError.
type Runner interface {
	Run(ctx context.Context, cmd Cmd) (Result, error)
}

// Exec runs real processes.
type Exec struct{}

// Run implements Runner.
func (Exec) Run(ctx context.Context, cmd Cmd) (Result, error) {
	c := exec.CommandContext(ctx, cmd.Name, cmd.Args...)
	c.Dir = cmd.Dir
	if len(cmd.Env) > 0 {
		c.Env = append(c.Environ(), cmd.Env...)
	}
	c.Stdin = cmd.Stdin
	var buf bytes.Buffer
	var out io.Writer = &buf
	if cmd.Output != nil {
		out = io.MultiWriter(&buf, cmd.Output)
	}
	c.Stdout, c.Stderr = out, out

	err := c.Run()
	res := Result{Output: buf.Bytes()}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		res.ExitCode = exitErr.ExitCode()
		return res, &ExitError{Cmd: cmd, Result: res}
	}
	if err != nil {
		return res, fmt.Errorf("%s: %w", cmd, err)
	}
	return res, nil
}
