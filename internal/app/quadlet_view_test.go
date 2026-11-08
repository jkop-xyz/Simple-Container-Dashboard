package app_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/gittest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

// newPodmanEnv is a dashboard on a Podman host whose Deployment Repo holds Quadlet Stacks.
// Quadlet Stacks deploy into a temp dir, never the developer's real Quadlet directory.
func newPodmanEnv(t *testing.T, initial map[string]string) *syncEnv {
	t.Helper()
	return newPodmanEnvIn(t, initial, t.TempDir())
}

// newPodmanEnvIn is newPodmanEnv with a given Quadlet directory and, optionally, a
// generator path (default fakeGenerator; "" means auto-detect).
func newPodmanEnvIn(t *testing.T, initial map[string]string, quadletDir string, generator ...string) *syncEnv {
	t.Helper()
	gen := fakeGenerator
	if len(generator) > 0 {
		gen = generator[0]
	}
	remote := gittest.NewRemote(t)
	remote.Commit("initial", merge(initial, map[string]string{".gitignore": "secrets.env\n"}))
	rec := gitOnly()
	clone := filepath.Join(t.TempDir(), "repo")
	dataDir := t.TempDir()
	h := startDashboard(t, withRepo(remote.URL, clone), func(o *app.Options) {
		o.Runner = rec
		o.DataDir = dataDir
		o.Config.Runtime = config.Podman
		o.QuadletDir = quadletDir
		o.QuadletGenerator = gen
	})
	rec.On("podman version", runnertest.Response{Output: "5.2.1\n"})
	return &syncEnv{harness: h, remote: remote, rec: rec, clone: clone, state: state.Open(filepath.Join(dataDir, "state.json"))}
}

// fakeGenerator is the quadlet generator path tests use; the recording fake answers it.
const fakeGenerator = "/fake/libexec/podman/quadlet"

func quadletLabel(unit string) map[string]string {
	return map[string]string{"PODMAN_SYSTEMD_UNIT": unit}
}

func showOutput(units ...[4]string) string {
	var blocks []string
	for _, u := range units {
		blocks = append(blocks, "Id="+u[0]+"\nLoadState="+u[1]+"\nActiveState="+u[2]+"\nSubState="+u[3])
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

func TestQuadletStacksGroupContainersAndShowUnits(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{
		"jellyfin/jellyfin.container": "[Container]\nImage=jellyfin\n",
		"jellyfin/media.volume":       "[Volume]\n",
		"traefik/traefik.container":   "[Container]\nImage=traefik\n",
	})
	e.rec.On("systemctl --user show", runnertest.Response{Output: showOutput(
		[4]string{"jellyfin.service", "loaded", "active", "running"},
		[4]string{"media-volume.service", "loaded", "active", "exited"},
		[4]string{"traefik.service", "loaded", "failed", "failed"},
	)})
	e.rec.On("systemctl --user is-active -- podman-auto-update.timer", runnertest.Response{Output: "active\n"})
	e.api.SetContainers(
		dockertest.Container{ID: "j", Name: "systemd-jellyfin", State: "running", Labels: quadletLabel("jellyfin.service")},
		dockertest.Container{ID: "x", Name: "manual", State: "running", Labels: quadletLabel("hand-made.service")},
	)

	body := mustGet(e.harness)
	sections := splitSections(body)
	assertContains(t, sections["jellyfin"], "systemd-jellyfin", `data-unit="jellyfin.service"`, `data-unit="media-volume.service"`,
		"unit-active", "active (running)", `href="/units/jellyfin.service/logs"`)
	// traefik's unit failed before it ever created a container: the unit is still visible.
	assertContains(t, sections["traefik"], `data-unit="traefik.service"`, "unit-failed", ">failed<", "No containers yet.")
	assertContains(t, sections[""], "manual")
	assertContains(t, body, "podman-auto-update.timer", `class="timer-active">active<`)
}

func TestMissingUnitsShowAsNotInstalled(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{"app/app.container": "[Container]\n"})
	e.rec.On("systemctl --user show", runnertest.Response{Output: showOutput(
		[4]string{"app.service", "not-found", "inactive", "dead"},
	)})
	e.rec.On("systemctl --user is-active -- podman-auto-update.timer", runnertest.Response{ExitCode: 3, Output: "inactive\n"})

	body := mustGet(e.harness)
	// Not selected, not deployed, unit not installed: it's listed under "Not on this host".
	assertContains(t, body, "Not on this host")
	assertContains(t, splitSections(body)["app"], "not selected · no containers")
	assertContains(t, body, `podman-auto-update.timer: <b class="timer-inactive">inactive<`)
}

func TestSelectedQuadletStackShowsUninstalledUnits(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{"app/app.container": "[Container]\n"})
	e.setSelected("app", true)
	e.rec.On("systemctl --user show", runnertest.Response{Output: showOutput(
		[4]string{"app.service", "not-found", "inactive", "dead"},
	)})
	assertContains(t, splitSections(mustGet(e.harness))["app"], "not installed", "unit-inactive", "deploys at next Apply")
}

func TestUnitLogStreamsTheJournal(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{"app/app.container": "[Container]\n"})
	e.rec.On("journalctl --user --unit app.service", runnertest.Response{
		Output: "2026-11-08T10:00:00+0100 host app[1]: Error: image not found\n2026-11-08T10:00:01+0100 host systemd[1]: app.service: Failed\n",
	})

	code, page := e.get("/units/app.service/logs")
	if code != 200 {
		t.Fatalf("page status %d", code)
	}
	assertContains(t, page, "app.service", `data-stream="/units/app.service/logs/stream"`, "Back to the dashboard")

	got := collect(t, e.logEvents(context.Background(), "../units/app.service"))
	if len(got) != 2 || !strings.Contains(got[0], "image not found") || !strings.Contains(got[1], "Failed") {
		t.Errorf("unit log %q", got)
	}
	var journal string
	for _, l := range e.rec.Lines() {
		if strings.HasPrefix(l, "journalctl") {
			journal = l
		}
	}
	if journal != "journalctl --user --unit app.service --lines 500 --no-pager --output short-iso --follow" {
		t.Errorf("ran %q", journal)
	}
}

func TestUnitLogsAreOnlyForQuadletUnits(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{"app/app.container": "[Container]\n"})
	for _, unit := range []string{"sshd.service", "app-other.service"} {
		if code, _ := e.get("/units/" + unit + "/logs"); code != 404 {
			t.Errorf("%s: status %d, want 404", unit, code)
		}
		if code, _ := e.get("/units/" + unit + "/logs/stream"); code != 404 {
			t.Errorf("%s stream: status %d, want 404", unit, code)
		}
	}
	for _, l := range e.rec.Lines() {
		if strings.HasPrefix(l, "journalctl") {
			t.Errorf("read a journal it shouldn't: %q", l)
		}
	}
}

func TestDockerHostsDontQuerySystemd(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	mustGet(e.harness)
	for _, l := range e.rec.Lines() {
		if strings.HasPrefix(l, "systemctl") || strings.HasPrefix(l, "journalctl") {
			t.Errorf("Docker host ran %q", l)
		}
	}
	if strings.Contains(mustGet(e.harness), "podman-auto-update.timer") {
		t.Error("Docker host shows the Podman timer")
	}
}
