// Package gittest provides a real git "remote" (a bare repo in a temp dir) standing in
// for GitHub, so tests can push commits and let scd fetch them with real git.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Remote is a bare repository plus a scratch working copy used to author commits.
type Remote struct {
	t testing.TB
	// URL is what scd should clone from (a local path).
	URL  string
	work string
}

// Isolate points git at no global or system config, so the developer's ~/.gitconfig
// (signing, hooks, credential helpers) never affects tests.
func Isolate(t testing.TB) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "scd test",
		"GIT_AUTHOR_EMAIL":    "test@example.invalid",
		"GIT_COMMITTER_NAME":  "scd test",
		"GIT_COMMITTER_EMAIL": "test@example.invalid",
		"GIT_TERMINAL_PROMPT": "0",
	} {
		t.Setenv(k, v)
	}
}

// NewRemote creates an empty bare repo on branch main. It calls Isolate.
func NewRemote(t testing.TB) *Remote {
	t.Helper()
	Isolate(t)
	dir := t.TempDir()
	r := &Remote{t: t, URL: filepath.Join(dir, "remote.git"), work: filepath.Join(dir, "work")}
	r.git(dir, "init", "--quiet", "--bare", "--initial-branch=main", r.URL)
	r.git(dir, "clone", "--quiet", r.URL, r.work)
	r.git(r.work, "checkout", "--quiet", "-B", "main")
	return r
}

// Commit writes files (an empty value deletes the file), commits everything and pushes
// to main. It returns the new commit hash.
func (r *Remote) Commit(msg string, files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.work, name)
		if content == "" {
			if err := os.RemoveAll(p); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	r.git(r.work, "add", "-A")
	r.git(r.work, "commit", "--quiet", "--allow-empty", "-m", msg)
	r.git(r.work, "push", "--quiet", "origin", "main")
	return strings.TrimSpace(r.git(r.work, "rev-parse", "HEAD"))
}

// Git runs git in dir and returns its output, failing the test on error.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *Remote) git(dir string, args ...string) string {
	r.t.Helper()
	return Git(r.t, dir, args...)
}
