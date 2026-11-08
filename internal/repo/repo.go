// Package repo wraps the host's clone of the Deployment Repo. It only ever clones,
// fetches, reads and fast-forwards: it never resets, stashes or writes tracked files.
package repo

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
)

// Repo is the host's clone of the Deployment Repo.
type Repo struct {
	Runner runner.Runner
	// URL is the remote to clone from.
	URL string
	// Branch is the branch to track.
	Branch string
	// Path is where the clone lives on the host.
	Path string
}

// Exists reports whether the clone is present.
func (r *Repo) Exists() bool {
	_, err := os.Stat(filepath.Join(r.Path, ".git"))
	return err == nil
}

// EnsureClone clones the Deployment Repo if the clone is missing. An existing clone is
// left exactly as it is.
func (r *Repo) EnsureClone(ctx context.Context) error {
	if r.Exists() {
		return nil
	}
	if entries, err := os.ReadDir(r.Path); err == nil && len(entries) > 0 {
		return fmt.Errorf("clone path %s exists, is not a git clone, and is not empty", r.Path)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--quiet"}
	if r.Branch != "" {
		args = append(args, "--branch", r.Branch)
	}
	args = append(args, "--", r.URL, r.Path)
	if _, err := r.Runner.Run(ctx, runner.Cmd{Name: "git", Args: args}); err != nil {
		return fmt.Errorf("cloning Deployment Repo: %w", err)
	}
	return nil
}

// Head returns the commit the clone is checked out at.
func (r *Repo) Head(ctx context.Context) (string, error) {
	out, err := r.git(ctx, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// Files lists every tracked file path at the given revision.
func (r *Repo) Files(ctx context.Context, rev string) ([]string, error) {
	out, err := r.git(ctx, "ls-tree", "-r", "--name-only", "-z", rev)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

func (r *Repo) git(ctx context.Context, args ...string) (string, error) {
	res, err := r.Runner.Run(ctx, runner.Cmd{Name: "git", Args: append([]string{"-C", r.Path}, args...)})
	if err != nil {
		return "", err
	}
	return string(res.Output), nil
}

// DisplayName shortens a remote URL to "owner/name" for GitHub-style URLs
// (https or ssh), falling back to the URL itself.
func DisplayName(url string) string {
	s := strings.TrimSuffix(strings.TrimSpace(url), "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j+1:]
		}
	} else if i := strings.Index(s, ":"); i >= 0 && strings.Contains(s[:i], "@") {
		s = s[i+1:]
	} else {
		return url
	}
	parts := strings.Split(s, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return url
}
