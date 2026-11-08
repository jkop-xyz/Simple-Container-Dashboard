package app_test

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/gittest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
)

// apply presses Apply, follows the live output stream to the end, and returns the
// streamed output and the dashboard afterwards.
func (e *syncEnv) apply() (stream, page string) {
	e.t.Helper()
	resp, err := http.Post(e.srv.URL+"/apply", "", nil)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return e.follow(), mustGet(e.harness)
}

// follow reads the Apply output stream until its done event.
func (e *syncEnv) follow() string {
	e.t.Helper()
	resp, err := http.Get(e.srv.URL + "/apply/stream")
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if line == "event: done" {
			break
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			out.WriteString(data + "\n")
		}
	}
	return out.String()
}

func (e *syncEnv) composeLines() []string {
	var out []string
	for _, l := range e.rec.Lines() {
		if strings.HasPrefix(l, "docker ") {
			out = append(out, l)
		}
	}
	return out
}

func (e *syncEnv) up(stack string) string {
	dir := filepath.Join(e.clone, stack)
	return "docker compose --project-name " + strings.ToLower(stack) + " --project-directory " + dir +
		" --file " + filepath.Join(dir, "compose.yaml") + " up --detach --remove-orphans"
}

func down(stack string) string {
	return "docker compose --project-name " + stack + " down --remove-orphans"
}

func TestApplyCarriesOutThePlan(t *testing.T) {
	e := newSyncEnv(t, merge(compose("dropped"), compose("deleted"), compose("same")))
	e.deployed("dropped", "deleted", "same")
	e.setSelected("same", true)
	to := e.remote.Commit("change", merge(compose("b"), compose("App1"), map[string]string{"deleted/compose.yaml": ""}))
	e.sync()
	e.setSelected("b", true)
	e.setSelected("App1", true)
	e.sync()

	_, page := e.apply()

	if e.head() != to {
		t.Errorf("clone at %s, want fast-forwarded to %s", e.head(), to)
	}
	want := []string{e.up("App1"), e.up("b"), down("deleted"), down("dropped")}
	if got := e.composeLines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("compose commands:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	assertContains(t, page, "Apply finished successfully")

	st, _ := e.state.Get()
	for _, name := range []string{"App1", "b", "same"} {
		if _, ok := st.Deployed[name]; !ok {
			t.Errorf("%s should be recorded as deployed", name)
		}
	}
	for _, name := range []string{"dropped", "deleted"} {
		if _, ok := st.Deployed[name]; ok {
			t.Errorf("%s should be forgotten after teardown", name)
		}
	}
	// A Sync right after a successful Apply has nothing left to do.
	assertContains(t, e.sync(), "Already at")
	if strings.Contains(mustGet(e.harness), `data-outcome="deploy"`) {
		t.Error("deployed Stacks planned again")
	}
}

func TestApplyNeverRemovesVolumesOrPullsImages(t *testing.T) {
	e := newSyncEnv(t, merge(compose("a"), compose("b")))
	e.deployed("b")
	e.setSelected("a", true)
	e.sync()
	e.apply()

	for _, l := range e.composeLines() {
		for _, bad := range []string{"--volumes", " -v", "pull", "--pull"} {
			if strings.Contains(l, bad) {
				t.Errorf("Apply ran %q", l)
			}
		}
	}
	if len(e.composeLines()) != 2 {
		t.Errorf("want one up and one down, got %q", e.composeLines())
	}
}

func TestApplyUsesThePlannedCommit(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	e.setSelected("a", true)
	planned := e.remote.Commit("planned", compose("b"))
	e.sync()
	e.remote.Commit("arrived after the Sync", compose("c"))

	e.apply()
	if e.head() != planned {
		t.Errorf("clone at %s, want the planned commit %s", e.head(), planned)
	}
}

func TestApplyContinuesPastFailuresAndReportsThem(t *testing.T) {
	e := newSyncEnv(t, merge(compose("a"), compose("b"), compose("c")))
	for _, n := range []string{"a", "b", "c"} {
		e.setSelected(n, true)
	}
	e.rec.On(e.up("b"), runnertest.Response{ExitCode: 1, Output: "Error: pull access denied for nope/image"})
	e.sync()

	stream, page := e.apply()

	if got := e.composeLines(); len(got) != 3 {
		t.Errorf("want all three Stacks attempted, got %q", got)
	}
	assertContains(t, page, "1 Stack(s) failed", "<b>b</b>", "pull access denied")
	assertContains(t, stream, "==> deploy a", "==> deploy b", "!!! deploy b failed", "==> deploy c", "finished with 1 failed")

	st, _ := e.state.Get()
	if !st.Deployed["b"].Failed || st.Deployed["a"].Failed || st.Deployed["c"].Failed {
		t.Errorf("failure markers wrong: %+v", st.Deployed)
	}
	// The failed Stack is retried on the next Sync even though nothing changed.
	got := planned(t, e.sync())
	if got["b"] != "redeploy" || got["a"] != "unchanged" {
		t.Errorf("after failure planned %v", got)
	}
	assertContains(t, mustGet(e.harness), "retry: the last Apply failed")
}

func TestApplyStreamsLiveOutput(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	e.setSelected("a", true)
	e.rec.On(e.up("a"), runnertest.Response{Output: "Container a-web-1  Started\n"})
	e.sync()

	stream, page := e.apply()
	assertContains(t, stream, "==> Fast-forwarding", "==> deploy a", "Container a-web-1  Started", "==> Apply finished")
	assertContains(t, page, "Container a-web-1  Started")
}

func TestApplyIsRejectedWithoutAFreshPlan(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	if !strings.Contains(mustGet(e.harness), `class="apply-button" disabled`) {
		t.Error("Apply should be disabled before any Sync")
	}
	_, page := e.apply()
	assertContains(t, page, "apply-error", "no Sync Plan")

	e.setSelected("a", true)
	e.sync()
	if strings.Contains(mustGet(e.harness), `class="apply-button" disabled`) {
		t.Error("Apply should be enabled with a fresh plan")
	}
	e.setSelected("a", false)
	_, page = e.apply()
	assertContains(t, page, "selection changed after the last Sync")

	if got := e.composeLines(); len(got) != 0 {
		t.Errorf("rejected Applies ran %q", got)
	}
}

func TestApplyWithNothingToDoIsRejected(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	e.sync()
	if !strings.Contains(mustGet(e.harness), `class="apply-button" disabled`) {
		t.Error("Apply should be disabled when the plan changes nothing")
	}
	_, page := e.apply()
	assertContains(t, page, "nothing to apply")
}

func TestAPlanIsAppliedOnlyOnce(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	e.setSelected("a", true)
	e.sync()
	e.apply()
	_, page := e.apply()
	assertContains(t, page, "no Sync Plan")
	if n := len(e.composeLines()); n != 1 {
		t.Errorf("%d compose runs, want 1", n)
	}
}

func TestApplyRefusesIfTheCloneWasEditedAfterSync(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	e.setSelected("a", true)
	before := e.head()
	e.remote.Commit("new", compose("b"))
	e.sync()
	os.WriteFile(filepath.Join(e.clone, "a", "compose.yaml"), []byte("edited\n"), 0o644)

	_, page := e.apply()
	assertContains(t, page, "Apply stopped before deploying anything", "has local changes")
	if e.head() != before || len(e.composeLines()) != 0 {
		t.Error("Apply touched the host after refusing")
	}
}

func TestConcurrentAppliesJoinAndSyncWaits(t *testing.T) {
	e := newSyncEnv(t, compose("a"))
	e.setSelected("a", true)
	e.sync()
	release := make(chan struct{})
	e.rec.On(e.up("a"), runnertest.Response{Wait: release})

	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if resp, err := http.Post(e.srv.URL+"/apply", "", nil); err == nil {
				resp.Body.Close()
			}
		}()
	}
	waitFor(t, func() bool { return len(e.composeLines()) == 1 })
	time.Sleep(100 * time.Millisecond)
	assertContains(t, e.sync(), "an Apply is running")
	assertContains(t, mustGet(e.harness), "Apply running")
	close(release)
	wg.Wait()
	e.follow()

	if n := len(e.composeLines()); n != 1 {
		t.Errorf("%d compose runs for 3 concurrent Applies, want 1", n)
	}
	if strings.TrimSpace(gittest.Git(t, e.clone, "status", "--porcelain")) != "" {
		t.Error("clone left dirty")
	}
}
