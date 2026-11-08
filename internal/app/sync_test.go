package app_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/gittest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

// syncEnv is a dashboard whose Deployment Repo is a real local git remote.
type syncEnv struct {
	*harness
	remote *gittest.Remote
	rec    *runnertest.Fake
	clone  string
	state  *state.Store
}

func newSyncEnv(t *testing.T, initial map[string]string) *syncEnv {
	t.Helper()
	remote := gittest.NewRemote(t)
	if _, ok := initial[".gitignore"]; !ok {
		initial = merge(initial, map[string]string{".gitignore": "secrets.env\n"})
	}
	remote.Commit("initial", initial)
	rec := gitOnly()
	clone := filepath.Join(t.TempDir(), "repo")
	dataDir := t.TempDir()
	h := startDashboard(t, withRepo(remote.URL, clone), func(o *app.Options) {
		o.Runner = rec
		o.DataDir = dataDir
	})
	return &syncEnv{harness: h, remote: remote, rec: rec, clone: clone, state: state.Open(filepath.Join(dataDir, "state.json"))}
}

// sync presses the Sync button and returns the dashboard it lands on.
func (e *syncEnv) sync() string {
	e.t.Helper()
	resp, err := http.Post(e.srv.URL+"/sync", "", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("POST /sync landed on status %d", resp.StatusCode)
	}
	return mustGet(e.harness)
}

// deployed pretends Apply already deployed these Stacks at the remote's current commit.
func (e *syncEnv) deployed(names ...string) {
	e.t.Helper()
	trees := map[string]string{}
	for _, n := range names {
		trees[n] = strings.TrimSpace(gittest.Git(e.t, e.clone, "rev-parse", "HEAD:"+n))
	}
	e.seed(trees, nil)
}

func (e *syncEnv) seed(trees map[string]string, failed map[string]bool) {
	e.t.Helper()
	err := e.state.Update(func(s *state.State) {
		if s.Deployed == nil {
			s.Deployed = map[string]state.DeployedStack{}
		}
		for n, tree := range trees {
			s.Deployed[n] = state.DeployedStack{Kind: stacks.Compose, Tree: tree, Failed: failed[n]}
		}
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *syncEnv) head() string {
	return strings.TrimSpace(gittest.Git(e.t, e.clone, "rev-parse", "HEAD"))
}

// planned returns the Sync Plan's action per Stack.
func planned(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, row := range strings.Split(body, `<tr class="plan-entry"`)[1:] {
		_, rest, _ := strings.Cut(row, `data-stack="`)
		name, rest, _ := strings.Cut(rest, `"`)
		_, rest, _ = strings.Cut(rest, `data-outcome="`)
		action, _, _ := strings.Cut(rest, `"`)
		out[name] = action
	}
	return out
}

func compose(name string) map[string]string {
	return map[string]string{name + "/compose.yaml": "services: {}\n"}
}

func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func TestSyncPlansEveryKindOfChange(t *testing.T) {
	e := newSyncEnv(t, merge(compose("same"), compose("edited"), compose("retry"),
		compose("dropped"), compose("deleted"), compose("ignored")))
	e.deployed("same", "edited", "retry", "dropped", "deleted")
	e.seed(map[string]string{"retry": strings.TrimSpace(gittest.Git(t, e.clone, "rev-parse", "HEAD:retry"))}, map[string]bool{"retry": true})
	// "dropped" is deployed but no longer selected; "ignored" was never selected.
	e.remote.Commit("change things", merge(
		map[string]string{"edited/compose.yaml": "services: {web: {image: nginx}}\n"},
		compose("fresh"),
		map[string]string{"deleted/compose.yaml": ""},
	))
	e.sync() // makes "fresh" known so it can be selected
	for _, n := range []string{"same", "edited", "retry", "fresh", "deleted"} {
		if code := e.setSelected(n, true); code != http.StatusSeeOther {
			t.Fatalf("selecting %s: status %d", n, code)
		}
	}

	got := planned(t, e.sync())
	want := map[string]string{
		"fresh":   "deploy",
		"edited":  "redeploy",
		"retry":   "redeploy",
		"dropped": "teardown",
		"deleted": "teardown",
		"same":    "unchanged",
		"ignored": "not selected",
	}
	for stack, action := range want {
		if got[stack] != action {
			t.Errorf("%s: planned %q, want %q", stack, got[stack], action)
		}
	}
	if len(got) != len(want) {
		t.Errorf("planned %v, want exactly %v", got, want)
	}
}

func TestSyncPlanOrderIsDeploysThenTeardownsAlphabetical(t *testing.T) {
	e := newSyncEnv(t, merge(compose("b-old"), compose("a-old")))
	e.deployed("a-old", "b-old")
	e.remote.Commit("new", merge(compose("z-new"), compose("c-new")))
	e.sync()
	// New upstream Stacks can be selected before any Apply brings them into the clone.
	for _, n := range []string{"z-new", "c-new"} {
		if code := e.setSelected(n, true); code != http.StatusSeeOther {
			t.Fatalf("selecting %s: status %d", n, code)
		}
	}

	body := e.sync()
	var order []string
	for _, row := range strings.Split(body, `<tr class="plan-entry"`)[1:] {
		_, rest, _ := strings.Cut(row, `data-stack="`)
		name, _, _ := strings.Cut(rest, `"`)
		order = append(order, name)
	}
	if got, want := strings.Join(order, ","), "c-new,z-new,a-old,b-old"; got != want {
		t.Errorf("plan order %s, want %s", got, want)
	}
}

func TestSyncShowsCommitRangeAndMessages(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	from := e.head()
	e.remote.Commit("bump jellyfin to 10.9", compose("b"))
	to := e.remote.Commit("add traefik", compose("c"))

	body := e.sync()
	assertContains(t, body, "Sync Plan", from[:7], to[:7], "bump jellyfin to 10.9", "add traefik")
}

func TestSyncChangesNothingOnTheHost(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	e.setSelected("a", true)
	before := e.head()
	e.remote.Commit("more", compose("b"))

	e.sync()

	if e.head() != before {
		t.Error("Sync moved the clone; only Apply may do that")
	}
	if _, err := os.Stat(filepath.Join(e.clone, "b")); err == nil {
		t.Error("Sync updated the working tree")
	}
	for _, line := range e.rec.Lines() {
		readOnlyValidation := strings.HasPrefix(line, "docker compose ") && strings.HasSuffix(line, " --file - config --quiet") ||
			line == "docker compose version"
		if !strings.HasPrefix(line, "git ") && !readOnlyValidation {
			t.Errorf("Sync ran %q", line)
		}
		for _, verb := range []string{" pull", " merge ", " reset", " checkout", " stash"} {
			if strings.Contains(line, verb) {
				t.Errorf("Sync ran %q", line)
			}
		}
	}
	if s, _ := e.state.Get(); len(s.Deployed) != 0 {
		t.Error("Sync recorded deployments")
	}
}

func TestSyncRefusesADirtyClone(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	os.WriteFile(filepath.Join(e.clone, "a", "compose.yaml"), []byte("edited on the host\n"), 0o644)

	body := e.sync()
	assertContains(t, body, "Sync failed", "has local changes", "a/compose.yaml")
	if strings.Contains(body, "Sync Plan") {
		t.Error("a refused Sync must not show a plan")
	}
}

func TestSyncRefusesUntrackedFilesButNotIgnoredOnes(t *testing.T) {
	e := newSyncEnv(t, merge(compose("a"), map[string]string{".gitignore": "secrets.env\n"}))
	os.WriteFile(filepath.Join(e.clone, "a", "secrets.env"), []byte("T=1\n"), 0o600)
	if body := e.sync(); strings.Contains(body, "Sync failed") {
		t.Fatalf("an ignored secrets.env must not make the clone dirty:\n%s", body)
	}

	os.WriteFile(filepath.Join(e.clone, "a", "notes.txt"), []byte("x\n"), 0o644)
	assertContains(t, e.sync(), "has local changes", "a/notes.txt")
}

func TestSyncRefusesLocalCommits(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	gittest.Git(t, e.clone, "commit", "--quiet", "--allow-empty", "-m", "made on the host")

	assertContains(t, e.sync(), "has commits that aren&#39;t on the remote")
}

func TestSyncRefusesDivergedHistory(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	gittest.Git(t, e.clone, "commit", "--quiet", "--allow-empty", "-m", "made on the host")
	e.remote.Commit("made upstream", compose("b"))

	assertContains(t, e.sync(), "has diverged from the remote")
}

func TestSyncReportsFetchFailure(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	e.rec.On("git -C "+e.clone+" fetch", runnertest.Response{ExitCode: 128, Output: "fatal: Authentication failed"})

	assertContains(t, e.sync(), "Sync failed", "fetching Deployment Repo", "Authentication failed")
}

func TestChangingSelectionMakesThePlanStale(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	if strings.Contains(e.sync(), "selection changed") {
		t.Fatal("fresh plan reported stale")
	}
	e.setSelected("a", true)
	assertContains(t, mustGet(e.harness), "The selection changed after this Sync")

	if strings.Contains(e.sync(), "The selection changed after this Sync") {
		t.Error("a new Sync should clear staleness")
	}
}

func TestConcurrentSyncsShareOneRun(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	release := make(chan struct{})
	e.rec.On("git -C "+e.clone+" fetch", runnertest.Response{Wait: release})

	post := func() {
		resp, err := http.Post(e.srv.URL+"/sync", "", nil)
		if err == nil {
			resp.Body.Close()
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); post() }()
	waitFor(t, func() bool { return countPrefix(e.rec.Lines(), "git -C "+e.clone+" fetch") >= 1 })
	// The first Sync is now blocked in fetch; these arrive while it runs.
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); post() }()
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	if n := countPrefix(e.rec.Lines(), "git -C "+e.clone+" fetch"); n != 1 {
		t.Errorf("%d fetches for 5 concurrent Syncs, want 1", n)
	}
}

func countPrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
