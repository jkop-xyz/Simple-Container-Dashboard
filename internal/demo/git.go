package demo

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
)

// commit is one commit of the in-memory Deployment Repo.
type commit struct {
	hash    string
	subject string
	files   map[string]string
}

// fakeGit emulates the handful of git commands scd's repo package runs, over a linear
// in-memory history, so demo mode needs no git binary and no real repository.
type fakeGit struct {
	mu      sync.Mutex
	commits []commit
	head    int // the clone's checked-out commit
	remote  int // origin/<branch>
}

func (g *fakeGit) add(subject string, files map[string]string) {
	h := sha1.New()
	fmt.Fprintf(h, "%d\x00%s", len(g.commits), subject)
	g.commits = append(g.commits, commit{hash: hex.EncodeToString(h.Sum(nil)), subject: subject, files: files})
}

// push adds a commit upstream, as if someone pushed to GitHub.
func (g *fakeGit) push(subject string, change func(files map[string]string)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	files := map[string]string{}
	for k, v := range g.commits[len(g.commits)-1].files {
		files[k] = v
	}
	change(files)
	g.add(subject, files)
	g.remote = len(g.commits) - 1
}

// latestFiles returns the newest commit's files.
func (g *fakeGit) latestFiles() map[string]string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.commits[len(g.commits)-1].files
}

// tree is the tree id of a directory at a commit: a hash of everything under it.
func tree(files map[string]string, dir string) string {
	var names []string
	for f := range files {
		if strings.HasPrefix(f, dir+"/") {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	h := sha1.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%s\x00", n, files[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (g *fakeGit) resolve(rev string) (int, bool) {
	switch rev {
	case "HEAD":
		return g.head, true
	}
	if strings.HasPrefix(rev, "refs/remotes/origin/") {
		return g.remote, true
	}
	for i, c := range g.commits {
		if c.hash == rev {
			return i, true
		}
	}
	return 0, false
}

func ok(out string) (runner.Result, error) { return runner.Result{Output: []byte(out)}, nil }

func fail(cmd runner.Cmd, code int, msg string) (runner.Result, error) {
	res := runner.Result{ExitCode: code, Output: []byte(msg)}
	return res, &runner.ExitError{Cmd: cmd, Result: res}
}

// run handles one git command.
func (g *fakeGit) run(cmd runner.Cmd) (runner.Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	args := cmd.Args
	if len(args) > 0 && args[0] == "clone" {
		path := args[len(args)-1]
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
			return fail(cmd, 128, err.Error())
		}
		return ok("")
	}
	if len(args) < 3 || args[0] != "-C" {
		return fail(cmd, 129, "demo git: unsupported command")
	}
	args = args[2:]
	switch args[0] {
	case "rev-parse":
		i, found := g.resolve(args[1])
		if !found {
			return fail(cmd, 128, "fatal: bad revision")
		}
		return ok(g.commits[i].hash + "\n")
	case "symbolic-ref":
		return ok("main\n")
	case "fetch", "status":
		return ok("") // the remote is in memory; the clone is always clean
	case "merge-base": // --is-ancestor a b
		a, okA := g.resolve(args[2])
		b, okB := g.resolve(args[3])
		if okA && okB && a <= b {
			return ok("")
		}
		return fail(cmd, 1, "")
	case "merge": // --ff-only --quiet rev
		i, found := g.resolve(args[len(args)-1])
		if !found || i < g.head {
			return fail(cmd, 128, "fatal: Not possible to fast-forward, aborting.")
		}
		g.head = i
		return ok("")
	case "log": // --format=%H%x00%s%x00 from..to
		from, to, _ := strings.Cut(args[len(args)-1], "..")
		a, _ := g.resolve(from)
		b, _ := g.resolve(to)
		var out strings.Builder
		for i := b; i > a; i-- {
			fmt.Fprintf(&out, "%s\x00%s\x00\n", g.commits[i].hash, g.commits[i].subject)
		}
		return ok(out.String())
	case "show": // rev:path
		rev, path, _ := strings.Cut(args[1], ":")
		i, found := g.resolve(rev)
		if content, has := g.commits[i].files[path]; found && has {
			return ok(content)
		}
		return fail(cmd, 128, "fatal: path does not exist")
	case "ls-tree":
		i, found := g.resolve(args[len(args)-1])
		if !found {
			return fail(cmd, 128, "fatal: not a tree object")
		}
		files := g.commits[i].files
		var names []string
		for f := range files {
			names = append(names, f)
		}
		sort.Strings(names)
		if args[1] == "-r" { // -r --name-only -z rev
			return ok(strings.Join(names, "\x00") + "\x00")
		}
		var out strings.Builder // -z rev: top-level entries
		seen := map[string]bool{}
		for _, f := range names {
			dir, _, nested := strings.Cut(f, "/")
			if seen[dir] {
				continue
			}
			seen[dir] = true
			if nested {
				fmt.Fprintf(&out, "040000 tree %s\t%s\x00", tree(files, dir), dir)
			} else {
				fmt.Fprintf(&out, "100644 blob %s\t%s\x00", tree(files, f), f)
			}
		}
		return ok(out.String())
	}
	return fail(cmd, 129, "demo git: unsupported command "+args[0])
}
