package app_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

func historyEntries(body string) []string {
	return strings.Split(body, `<section class="history-entry`)[1:]
}

func TestHeaderAndHistoryShowASuccessfulApply(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	if strings.Contains(mustGet(e.harness), "last Apply") {
		t.Error("no Apply yet, but the header shows one")
	}
	_, empty := e.get("/history")
	assertContains(t, empty, "No Applies yet")

	selectAll(e, "app")
	from := e.head()
	to := e.remote.Commit("more", compose("b"))
	e.sync()
	_, page := e.apply()

	assertContains(t, page, "last Apply", `href="/history"`, "just now", `class="ok">succeeded`)
	_, hist := e.get("/history")
	entries := historyEntries(hist)
	if len(entries) != 1 {
		t.Fatalf("%d history entries, want 1", len(entries))
	}
	assertContains(t, entries[0], from[:7], to[:7], `data-stack="app"`, "deploy", `class="ok">ok`)
}

func TestHistoryShowsFailuresWithTheirErrors(t *testing.T) {
	e := newSyncEnv(t, merge(compose("good"), compose("bad")))
	selectAll(e, "good", "bad")
	e.rec.On(e.up("bad"), runnertest.Response{ExitCode: 1, Output: "manifest unknown"})
	e.sync()
	_, page := e.apply()

	assertContains(t, page, "last Apply", `class="bad">1 failed`)
	_, hist := e.get("/history")
	assertContains(t, historyEntries(hist)[0], "failed:", "manifest unknown", `data-stack="good"`)
}

func TestHistoryRecordsAnApplyThatStoppedEarly(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	selectAll(e, "app")
	e.sync()
	os.WriteFile(filepath.Join(e.clone, "app", "compose.yaml"), []byte("edited\n"), 0o644)
	_, page := e.apply()

	assertContains(t, page, `class="bad">stopped`)
	_, hist := e.get("/history")
	assertContains(t, historyEntries(hist)[0], "Stopped before deploying anything", "has local changes")
}

func TestHistoryKeepsTheLatest20NewestFirst(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	base := time.Now().Add(-48 * time.Hour)
	e.state.Update(func(st *state.State) {
		for i := range 20 {
			st.AddHistory(state.ApplyRecord{
				From: fmt.Sprintf("old%04d", i), To: fmt.Sprintf("old%04d", i+1),
				Started: base.Add(time.Duration(i) * time.Minute), Finished: base.Add(time.Duration(i) * time.Minute),
			})
		}
	})
	selectAll(e, "app")
	e.sync()
	e.apply()

	_, hist := e.get("/history")
	entries := historyEntries(hist)
	if len(entries) != 20 {
		t.Fatalf("%d entries, want 20", len(entries))
	}
	if !strings.Contains(entries[0], `data-stack="app"`) {
		t.Error("the newest Apply should come first")
	}
	if strings.Contains(hist, ">old0000<") {
		t.Error("the oldest entry should have been dropped")
	}
	assertContains(t, entries[19], ">old0001<")
}

func TestHistorySurvivesRestart(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	selectAll(e, "app")
	e.sync()
	e.apply()

	st, _ := e.state.Get()
	if len(st.History) != 1 || st.History[0].Results[0].Stack != "app" {
		t.Fatalf("history not persisted: %+v", st.History)
	}
}

func TestSyncAndApplyAreLoggedForTheJournal(t *testing.T) {
	var buf syncBuffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := newSyncEnv(t, compose("app"))
	selectAll(e, "app")
	e.sync()
	e.apply()

	assertContains(t, buf.String(), "Sync planned", "Apply started", "Apply step done", "stack=app", "Apply finished", "ok=true")
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
