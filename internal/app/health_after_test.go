package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

// settled waits for the latest Apply's post-Apply health check and returns its record.
func (e *syncEnv) settled() state.ApplyRecord {
	e.t.Helper()
	var rec state.ApplyRecord
	waitFor(e.t, func() bool {
		st, _ := e.state.Get()
		if len(st.History) == 0 {
			return false
		}
		rec = st.History[0]
		return rec.HealthCheck == "done"
	})
	return rec
}

func after(rec state.ApplyRecord, stack string) string {
	for _, r := range rec.Results {
		if r.Stack == stack {
			return r.After
		}
	}
	return "?"
}

func TestHealthyStacksAreRecordedHealthyAfterApply(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	selectAll(e, "app")
	e.sync()
	e.api.SetContainers(dockertest.Container{ID: "a1", Name: "app-web-1", State: "running", Health: "healthy", Labels: composeLabel("app")})
	e.apply()

	if got := after(e.settled(), "app"); got != "healthy" {
		t.Fatalf("after = %q", got)
	}
	_, hist := e.get("/history")
	assertContains(t, historyEntries(hist)[0], `after-healthy">healthy<`, "succeeded")
	assertContains(t, mustGet(e.harness), `last-apply chip-ok`)
}

func TestUnhealthyAndMissingStacksAreFlaggedAfterApply(t *testing.T) {
	e := newSyncEnv(t, merge(compose("sick"), compose("gone"), compose("slow")))
	selectAll(e, "sick", "gone", "slow")
	e.sync()
	e.api.SetContainers(
		dockertest.Container{ID: "s1", Name: "sick-1", State: "running", Health: "unhealthy", Labels: composeLabel("sick")},
		dockertest.Container{ID: "w1", Name: "slow-1", State: "running", Health: "starting", Labels: composeLabel("slow")},
	) // "gone" never got a container
	e.apply()

	rec := e.settled()
	for stack, want := range map[string]string{"sick": "unhealthy", "gone": "not running", "slow": "still starting"} {
		if got := after(rec, stack); got != want {
			t.Errorf("%s after = %q, want %q", stack, got, want)
		}
	}
	_, hist := e.get("/history")
	assertContains(t, historyEntries(hist)[0], "applied · 2 unhealthy after", `after-unhealthy">unhealthy<`, `after-not running">not running<`)
	assertContains(t, mustGet(e.harness), "last-apply chip-warn", "✓ · 2 unhealthy after")
}

func TestARestartDuringTheWindowIsFlagged(t *testing.T) {
	e := newSyncEnvWith(t, compose("app"), func(o *app.Options) {
		o.WatchFor, o.WatchEvery = 400*time.Millisecond, 20*time.Millisecond
	})
	selectAll(e, "app")
	e.sync()
	c := dockertest.Container{ID: "a1", Name: "app-web-1", State: "running", Labels: composeLabel("app")}
	e.api.SetContainers(c)
	e.apply()

	waitFor(t, func() bool { st, _ := e.state.Get(); return len(st.History) > 0 })
	time.Sleep(100 * time.Millisecond) // a few samples in
	c.RestartCount = 2
	e.api.SetContainers(c)

	if got := after(e.settled(), "app"); got != "restarting" {
		t.Errorf("after = %q, want restarting", got)
	}
}

func TestHistoryShowsTheCheckWhileItRuns(t *testing.T) {
	e := newSyncEnvWith(t, compose("app"), func(o *app.Options) {
		o.WatchFor, o.WatchEvery = time.Second, 50*time.Millisecond
	})
	selectAll(e, "app")
	e.sync()
	e.api.SetContainers(dockertest.Container{ID: "a1", Name: "app-web-1", State: "running", Labels: composeLabel("app")})
	e.apply()

	_, hist := e.get("/history")
	assertContains(t, historyEntries(hist)[0], "checking health…")
	assertContains(t, mustGet(e.harness), "checking health…")
	e.settled()
	_, hist = e.get("/history")
	if strings.Contains(historyEntries(hist)[0], "checking health…") {
		t.Error("still checking after the window")
	}
}

func TestOnlySuccessfulDeploysAreWatched(t *testing.T) {
	e := newSyncEnv(t, merge(compose("ok"), compose("broken"), compose("old")))
	e.deployed("old")
	selectAll(e, "ok", "broken")
	e.rec.On(e.up("broken"), runnertest.Response{ExitCode: 1, Output: "boom"})
	e.sync()
	e.api.SetContainers(dockertest.Container{ID: "k1", Name: "ok-1", State: "running", Labels: composeLabel("ok")})
	e.apply()

	rec := e.settled()
	if after(rec, "ok") != "healthy" || after(rec, "broken") != "" || after(rec, "old") != "" {
		t.Errorf("watched the wrong Stacks: %+v", rec.Results)
	}
}

func TestNothingIsWatchedWhenNothingWasDeployed(t *testing.T) {
	e := newSyncEnv(t, compose("old"))
	e.deployed("old")
	e.sync()
	e.apply() // only a teardown
	st, _ := e.state.Get()
	if len(st.History) != 1 || st.History[0].HealthCheck != "" {
		t.Errorf("a teardown-only Apply was watched: %+v", st.History)
	}
}

func TestQuadletStacksAreWatchedByUnitLabel(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{"app/app.container": "[Container]\n"})
	selectAll(e, "app")
	e.sync()
	e.api.SetContainers(dockertest.Container{ID: "q1", Name: "systemd-app", State: "running", Health: "unhealthy", Labels: quadletLabel("app.service")})
	e.apply()
	if got := after(e.settled(), "app"); got != "unhealthy" {
		t.Errorf("after = %q", got)
	}
}
