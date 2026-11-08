// Package deploy holds one deployer per Stack kind. Deployers run the runtime's own
// tools through the command runner; they never remove volumes (ADR 0002).
package deploy

import (
	"context"
	"io"
	"path/filepath"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
)

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
func (c Compose) Deploy(ctx context.Context, s stacks.Stack, out io.Writer) error {
	dir := filepath.Join(c.RepoPath, s.Name)
	return c.compose(ctx, out,
		"--project-name", s.ProjectName(),
		"--project-directory", dir,
		"--file", filepath.Join(dir, stacks.ComposeFile),
		"up", "--detach", "--remove-orphans")
}

// Teardown runs `compose down --remove-orphans` by project name, without --volumes.
func (c Compose) Teardown(ctx context.Context, s stacks.Stack, out io.Writer) error {
	return c.compose(ctx, out, "--project-name", s.ProjectName(), "down", "--remove-orphans")
}

func (c Compose) compose(ctx context.Context, out io.Writer, args ...string) error {
	_, err := c.Runner.Run(ctx, runner.Cmd{Name: "docker", Args: append([]string{"compose"}, args...), Output: out})
	return err
}
