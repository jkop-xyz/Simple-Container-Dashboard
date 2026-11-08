// Package hostcheck finds the external tools scd needs that are missing on the host.
package hostcheck

import (
	"context"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
)

// LookPath reports where a command is, like exec.LookPath.
type LookPath func(file string) (string, error)

// Missing lists the tools the host's runtime needs that aren't available, e.g.
// "git", "docker compose" or "journalctl".
func Missing(ctx context.Context, rt config.Runtime, lookPath LookPath, r runner.Runner) []string {
	var missing []string
	need := func(tools ...string) {
		for _, t := range tools {
			if _, err := lookPath(t); err != nil {
				missing = append(missing, t)
			}
		}
	}
	need("git")
	switch rt {
	case config.Docker:
		if _, err := lookPath("docker"); err != nil {
			missing = append(missing, "docker", "docker compose")
		} else if _, err := r.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "version"}}); err != nil {
			missing = append(missing, "docker compose")
		}
	case config.Podman:
		need("podman", "systemctl", "journalctl")
	}
	return missing
}
