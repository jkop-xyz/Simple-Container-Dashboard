package app_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

func TestHistoryKeepsEachApplysOutputDurationAndCommits(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	selectAll(e, "app")
	e.remote.Commit("bump app to 2.0", map[string]string{"app/compose.yaml": "services: {web: {image: app:2}}\n"})
	e.rec.On(e.up("app"), runnertest.Response{Output: "Container app-web-1  Started\n"})
	e.sync()
	e.apply()

	_, hist := e.get("/history")
	entry := historyEntries(hist)[0]
	assertContains(t, entry, ">today ", "took ", "bump app to 2.0", "View output", `href="/history/`)

	_, rest, _ := strings.Cut(entry, `href="/history/`)
	id, _, _ := strings.Cut(rest, `"`)
	code, out := e.get("/history/" + id)
	if code != 200 {
		t.Fatalf("output page: %d", code)
	}
	assertContains(t, out, "==&gt; deploy app", "Container app-web-1  Started", "==&gt; Apply finished", `class="crumb crumb-home" href="/">Overview</a>`)
	if _, err := os.Stat(filepath.Join(e.dataDir, "applies", id+".log")); err != nil {
		t.Errorf("output file: %v", err)
	}
	st, _ := e.state.Get()
	if b, _ := os.ReadFile(filepath.Join(e.dataDir, "state.json")); strings.Contains(string(b), "Container app-web-1") {
		t.Error("Apply output belongs in its own file, not in state.json")
	}
	if len(st.History[0].Commits) != 1 || st.History[0].Duration() <= 0 {
		t.Errorf("record %+v", st.History[0])
	}
}

func TestHistoryOutputRejectsUnknownAndUnsafeIDs(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	for _, id := range []string{"nope", "..%2Fstate", "%2E%2E"} {
		if code, _ := e.get("/history/" + id); code != 404 {
			t.Errorf("/history/%s: %d, want 404", id, code)
		}
	}
}

func TestDroppedHistoryEntriesTakeTheirOutputWithThem(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	dir := filepath.Join(e.dataDir, "applies")
	os.MkdirAll(dir, 0o755)
	base := time.Now().Add(-48 * time.Hour)
	e.state.Update(func(st *state.State) {
		for i := range 20 {
			id := fmt.Sprintf("old-%02d", i)
			os.WriteFile(filepath.Join(dir, id+".log"), []byte("old\n"), 0o600)
			st.AddHistory(state.ApplyRecord{ID: id, Started: base, Finished: base.Add(time.Duration(i) * time.Minute)})
		}
	})
	selectAll(e, "app")
	e.sync()
	e.apply()

	if _, err := os.Stat(filepath.Join(dir, "old-00.log")); !os.IsNotExist(err) {
		t.Errorf("the dropped entry's output should be gone (err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "old-01.log")); err != nil {
		t.Error("kept entries' output must stay")
	}
}
