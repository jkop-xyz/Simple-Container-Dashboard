// Package deploy holds one deployer per Stack kind. Deployers run the runtime's own
// tools through the command runner; they never remove volumes (ADR 0002).
package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
)

// Validator checks a Stack's definition at a commit before it is planned for deploy.
type Validator interface {
	// Validate checks the Stack using its files' content at the planned commit (keyed by
	// path relative to the Stack's directory). It must not write anything.
	Validate(ctx context.Context, s stacks.Stack, files map[string][]byte) error
}

// Deployer deploys and tears down Stacks of one kind.
type Deployer interface {
	// Deploy deploys or redeploys the Stack from the clone.
	Deploy(ctx context.Context, s stacks.Stack, out io.Writer) error
	// Teardown removes the Stack's containers and networks, keeping its volumes. It must
	// work even when the Stack's files are gone from the clone.
	Teardown(ctx context.Context, s stacks.Stack, out io.Writer) error
}

// Compose deploys Compose Stacks with `docker compose`, straight from the clone.
type Compose struct {
	Runner runner.Runner
	// RepoPath is the Deployment Repo clone.
	RepoPath string
}

// Deploy runs `compose up -d --remove-orphans` for the Stack. Images are pulled only if
// missing: Apply never updates images of its own accord.
// When the Stack has a secrets.env it is passed as the env file; scd only reads it.
func (c Compose) Deploy(ctx context.Context, s stacks.Stack, out io.Writer) error {
	dir := filepath.Join(c.RepoPath, s.Name)
	args := []string{"--project-name", s.ProjectName(), "--project-directory", dir}
	args = append(args, c.envFile(s)...)
	args = append(args, "--file", filepath.Join(dir, stacks.ComposeFile), "up", "--detach", "--remove-orphans")
	return c.compose(ctx, out, args...)
}

// envFile returns the --env-file arguments for the Stack's secrets.env, if it has one.
func (c Compose) envFile(s stacks.Stack) []string {
	p := filepath.Join(c.RepoPath, s.Name, stacks.SecretsFile)
	if _, err := os.Stat(p); err != nil {
		return nil
	}
	return []string{"--env-file", p}
}

// Teardown runs `compose down --remove-orphans` by project name, without --volumes.
func (c Compose) Teardown(ctx context.Context, s stacks.Stack, out io.Writer) error {
	return c.compose(ctx, out, "--project-name", s.ProjectName(), "down", "--remove-orphans")
}

func (c Compose) compose(ctx context.Context, out io.Writer, args ...string) error {
	_, err := c.Runner.Run(ctx, runner.Cmd{Name: "docker", Args: append([]string{"compose"}, args...), Output: out})
	return err
}

// Validate runs `compose config --quiet` on the compose.yaml at the planned commit, fed
// on stdin so nothing is written to the clone.
func (c Compose) Validate(ctx context.Context, s stacks.Stack, files map[string][]byte) error {
	dir := filepath.Join(c.RepoPath, s.Name)
	if _, err := os.Stat(dir); err != nil {
		dir = c.RepoPath // a new Stack's directory only appears with the Apply
	}
	_, err := c.Runner.Run(ctx, runner.Cmd{
		Name: "docker",
		Args: append(append([]string{"compose", "--project-name", s.ProjectName(), "--project-directory", dir},
			c.envFile(s)...), "--file", "-", "config", "--quiet"),
		Stdin: bytes.NewReader(files[stacks.ComposeFile]),
	})
	var exit *runner.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("compose config failed: %s", strings.TrimSpace(string(exit.Result.Output)))
	}
	return err
}
