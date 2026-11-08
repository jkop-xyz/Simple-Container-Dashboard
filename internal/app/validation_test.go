package app_test

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/gittest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
)

// planReason returns the plan row's reason text for a Stack.
func planReason(body, stack string) string {
	for _, row := range strings.Split(body, `<tr class="plan-entry"`)[1:] {
		if strings.Contains(row, `data-stack="`+stack+`"`) {
			_, rest, _ := strings.Cut(row, `class="reason">`)
			reason, _, _ := strings.Cut(rest, "</td>")
			return reason
		}
	}
	return ""
}

func selectAll(e *syncEnv, names ...string) {
	e.t.Helper()
	for _, n := range names {
		if code := e.setSelected(n, true); code != 303 {
			e.t.Fatalf("selecting %s: status %d", n, code)
		}
	}
}

func TestStacksWithBothOrNeitherDefinitionAreInvalid(t *testing.T) {
	e := newSyncEnv(t, merge(
		map[string]string{"both/compose.yaml": "services: {}\n", "both/web.container": "[Container]\n"},
		map[string]string{"neither/README.md": "hi\n"},
		compose("good"),
	))
	selectAll(e, "both", "neither", "good")

	body := e.sync()
	got := planned(t, body)
	if got["both"] != "invalid" || got["neither"] != "invalid" || got["good"] != "deploy" {
		t.Fatalf("planned %v", got)
	}
	assertContains(t, planReason(body, "both"), "both a compose.yaml and Quadlet files")
	assertContains(t, planReason(body, "neither"), "no compose.yaml and no Quadlet files")

	e.apply()
	if lines := e.composeLines(); len(lines) != 1 || !strings.Contains(lines[0], "--project-name good ") {
		t.Errorf("only the valid Stack should deploy, ran %q", lines)
	}
}

func TestQuadletStacksAreInvalidOnDockerHosts(t *testing.T) {
	e := newSyncEnv(t, map[string]string{"jellyfin/jellyfin.container": "[Container]\nImage=jellyfin\n"})
	selectAll(e, "jellyfin")

	body := e.sync()
	if planned(t, body)["jellyfin"] != "invalid" {
		t.Fatalf("planned %v", planned(t, body))
	}
	assertContains(t, planReason(body, "jellyfin"), "Quadlet Stack, but this is a Docker host")
}

func TestComposeStacksAreInvalidOnPodmanHosts(t *testing.T) {
	remote := gittest.NewRemote(t)
	remote.Commit("init", compose("app"))
	h := startDashboard(t, withRepo(remote.URL, filepath.Join(t.TempDir(), "repo")), func(o *app.Options) {
		o.Config.Runtime = config.Podman
	})
	e := &syncEnv{harness: h, remote: remote}
	selectAll(e, "app")

	body := e.sync()
	assertContains(t, planReason(body, "app"), "Compose Stack, but this is a Podman host")
}

func TestComposeConfigFailureMakesTheStackInvalid(t *testing.T) {
	e := newSyncEnv(t, merge(compose("broken"), compose("fine")))
	selectAll(e, "broken", "fine")
	e.remote.Commit("break it", map[string]string{"broken/compose.yaml": "services:\n  web: [nope\n"})
	e.rec.On("docker compose --project-name broken ", runnertest.Response{
		ExitCode: 15, Output: "yaml: line 2: did not find expected ',' or ']'",
	})

	body := e.sync()
	got := planned(t, body)
	if got["broken"] != "invalid" || got["fine"] != "deploy" {
		t.Fatalf("planned %v", got)
	}
	assertContains(t, planReason(body, "broken"), "compose config failed", "did not find expected")

	// The validator saw the content at the planned commit, fed on stdin.
	for _, c := range e.rec.Calls() {
		if strings.HasPrefix(c.String(), "docker compose --project-name broken ") {
			b, _ := io.ReadAll(c.Stdin)
			if string(b) != "services:\n  web: [nope\n" {
				t.Errorf("validator got %q", b)
			}
		}
	}

	e.apply()
	for _, l := range e.composeLines() {
		if strings.Contains(l, "--project-name broken ") {
			t.Errorf("Invalid Stack was deployed: %q", l)
		}
	}
}

func TestInvalidDeployedStackIsLeftAlone(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	e.deployed("app")
	selectAll(e, "app")
	e.remote.Commit("oops", map[string]string{"app/web.container": "[Container]\n"})

	body := e.sync()
	if planned(t, body)["app"] != "invalid" {
		t.Fatalf("planned %v", planned(t, body))
	}
	assertContains(t, planReason(body, "app"), "left as it is")
}

func TestStackNamesAreLowercasedWithAWarning(t *testing.T) {
	e := newSyncEnv(t, compose("App1"))
	selectAll(e, "App1")

	body := e.sync()
	if planned(t, body)["App1"] != "deploy" {
		t.Fatalf("planned %v", planned(t, body))
	}
	assertContains(t, planReason(body, "App1"), `runs as project &#34;app1&#34;`)
	assertContains(t, splitSections(body)["App1"], "names are lowercased")
}

func TestNamesThatCollideAfterLowercasingAreBothInvalid(t *testing.T) {
	e := newSyncEnv(t, merge(compose("App"), compose("app")))
	selectAll(e, "App", "app")

	body := e.sync()
	got := planned(t, body)
	if got["App"] != "invalid" || got["app"] != "invalid" {
		t.Fatalf("planned %v", got)
	}
	assertContains(t, planReason(body, "App"), "collides with app")
	assertContains(t, planReason(body, "app"), "collides with App")
}

func TestNamesThatCantBeProjectNamesAreInvalid(t *testing.T) {
	e := newSyncEnv(t, compose("my.app"))
	selectAll(e, "my.app")
	assertContains(t, planReason(e.sync(), "my.app"), "can&#39;t be used as a project name")
}

func TestHiddenDirectoriesNeverBecomeStacks(t *testing.T) {
	e := newSyncEnv(t, merge(
		map[string]string{".github/workflows/ci.yml": "on: push\n", ".renovate/x.json": "{}\n", "README.md": "x\n"},
		compose("app"),
	))
	got := planned(t, e.sync())
	if len(got) != 1 || got["app"] == "" {
		t.Errorf("planned %v, want only app", got)
	}
}
