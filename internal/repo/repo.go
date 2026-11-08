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

// Commit is one commit in a range.
type Commit struct {
	Hash    string
	Subject string
}

// Refusal is returned when the clone is in a state scd will not touch (local edits,
// diverged history). The message tells the operator what is wrong.
type Refusal struct{ Reason string }

func (e *Refusal) Error() string { return e.Reason }

// TrackedBranch is the branch scd follows: the configured one, or the clone's
// checked-out branch.
func (r *Repo) TrackedBranch(ctx context.Context) (string, error) {
	if r.Branch != "" {
		return r.Branch, nil
	}
	out, err := r.git(ctx, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("clone is not on a branch: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// Fetch updates the remote-tracking ref for the tracked branch and returns the commit
// it points at. The working tree and local branch are not touched.
func (r *Repo) Fetch(ctx context.Context) (string, error) {
	branch, err := r.TrackedBranch(ctx)
	if err != nil {
		return "", err
	}
	if _, err := r.git(ctx, "fetch", "--quiet", "origin", branch); err != nil {
		return "", fmt.Errorf("fetching Deployment Repo: %w", err)
	}
	return r.Latest(ctx)
}

// Latest returns the most recently fetched remote commit of the tracked branch, without
// fetching. Before any fetch this is the commit the clone was made from.
func (r *Repo) Latest(ctx context.Context) (string, error) {
	branch, err := r.TrackedBranch(ctx)
	if err != nil {
		return "", err
	}
	out, err := r.git(ctx, "rev-parse", "refs/remotes/origin/"+branch)
	return strings.TrimSpace(out), err
}

// CheckClean refuses when the clone has local edits or untracked files (git-ignored
// files such as secrets.env don't count), or when its history has diverged from the
// remote commit.
func (r *Repo) CheckClean(ctx context.Context, remote string) error {
	out, err := r.git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	var dirty []string
	for _, entry := range strings.Split(out, "\x00") {
		if len(entry) > 3 {
			dirty = append(dirty, entry[3:])
		}
	}
	if len(dirty) > 0 {
		return &Refusal{Reason: fmt.Sprintf("the clone at %s has local changes, so scd won't touch it: %s",
			r.Path, strings.Join(dirty, ", "))}
	}

	head, err := r.Head(ctx)
	if err != nil {
		return err
	}
	if head == remote || r.isAncestor(ctx, head, remote) {
		return nil
	}
	if r.isAncestor(ctx, remote, head) {
		return &Refusal{Reason: fmt.Sprintf("the clone at %s has commits that aren't on the remote, so scd won't touch it", r.Path)}
	}
	return &Refusal{Reason: fmt.Sprintf("the clone at %s has diverged from the remote, so scd won't touch it", r.Path)}
}

func (r *Repo) isAncestor(ctx context.Context, a, b string) bool {
	_, err := r.git(ctx, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

// Log lists the commits in from..to, newest first.
func (r *Repo) Log(ctx context.Context, from, to string) ([]Commit, error) {
	if from == to {
		return nil, nil
	}
	out, err := r.git(ctx, "log", "--format=%H%x00%s%x00", from+".."+to)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		commits = append(commits, Commit{Hash: strings.TrimSpace(fields[i]), Subject: fields[i+1]})
	}
	return commits, nil
}

// Trees returns the git tree id of every top-level directory at rev. A directory's
// tree id changes exactly when any file under it changes.
func (r *Repo) Trees(ctx context.Context, rev string) (map[string]string, error) {
	out, err := r.git(ctx, "ls-tree", "-z", rev)
	if err != nil {
		return nil, err
	}
	trees := map[string]string{}
	for _, line := range strings.Split(out, "\x00") {
		meta, name, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if ok && len(f) == 3 && f[1] == "tree" {
			trees[name] = f[2]
		}
	}
	return trees, nil
}
