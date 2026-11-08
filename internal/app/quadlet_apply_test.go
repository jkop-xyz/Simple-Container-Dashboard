package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// hostLines lists the commands that change the host (systemctl, podman), in order.
func (e *syncEnv) hostLines() []string {
	var out []string
	for _, l := range e.rec.Lines() {
		if strings.HasPrefix(l, "systemctl --user ") && !strings.HasPrefix(l, "systemctl --user show") && !strings.HasPrefix(l, "systemctl --user is-active") {
			out = append(out, l)
		}
		if strings.HasPrefix(l, "podman ") && !strings.HasPrefix(l, "podman version") {
			out = append(out, l)
		}
	}
	return out
}

const mediaContainer = "[Container]\nImage=docker.io/jellyfin/jellyfin:10\nVolume=media.volume:/media\nVolume=./config:/config\n"

func TestQuadletApplyDeploysIntoTheStacksOwnSubdirectory(t *testing.T) {
	quadlets := t.TempDir()
	e := newPodmanEnvIn(t, map[string]string{
		"media/media.container": mediaContainer,
		"media/media.volume":    "[Volume]\n",
		"media/config/app.ini":  "theme=dark\n",
	}, quadlets)
	secrets := e.placeSecrets("media", "API_KEY=s3cret\n")
	selectAll(e, "media")

	if got := planned(t, e.sync())["media"]; got != "deploy" {
		t.Fatalf("planned %q", got)
	}
	_, page := e.apply()
	assertContains(t, page, "Apply finished successfully")

	dest := filepath.Join(quadlets, "media")
	if readFile(t, filepath.Join(dest, "media.container")) != mediaContainer {
		t.Error("unit file not copied verbatim")
	}
	if readFile(t, filepath.Join(dest, "config", "app.ini")) != "theme=dark\n" {
		t.Error("supporting files must be copied too, so relative paths keep working")
	}
	if exists(filepath.Join(dest, "secrets.env")) {
		t.Error("secrets.env was copied into the Quadlet directory")
	}
	dropIn := readFile(t, filepath.Join(dest, "media.container.d", "scd-secrets.conf"))
	assertContains(t, dropIn, "[Container]", "EnvironmentFile="+secrets)
	if exists(filepath.Join(dest, "media.volume.d")) {
		t.Error("drop-ins are only for .container units")
	}

	want := []string{"systemctl --user daemon-reload", "systemctl --user restart -- media.service media-volume.service"}
	if got := e.hostLines(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestQuadletValidationRunsTheGeneratorDryRunOnThePlannedFiles(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{"app/app.container": "[Container]\nImage=a\n"})
	selectAll(e, "app")
	e.placeSecrets("app", "X=1\n")
	e.remote.Commit("bad unit", map[string]string{"app/app.container": "[Container]\nImgae=typo\n"})
	var seen string
	e.rec.On(fakeGenerator, runnertest.Response{ExitCode: 1, Output: "quadlet-generator[1]: unsupported key 'Imgae' in group 'Container' in app.container"})

	body := e.sync()
	if got := planned(t, body)["app"]; got != "invalid" {
		t.Fatalf("planned %q", got)
	}
	assertContains(t, planReason(body, "app"), "quadlet check failed", "unsupported key")
	for _, c := range e.rec.Calls() {
		if c.Name == fakeGenerator {
			seen = strings.Join(c.Args, " ") + " " + strings.Join(c.Env, " ")
		}
	}
	assertContains(t, seen, "--user --dryrun", "QUADLET_UNIT_DIRS=")
	if strings.Contains(seen, e.clone) {
		t.Error("validation must use a throwaway directory, not the clone")
	}
}

func TestQuadletRedeployStopsUnitsWhoseFilesWereRemoved(t *testing.T) {
	quadlets := t.TempDir()
	e := newPodmanEnvIn(t, map[string]string{
		"app/web.container":    "[Container]\nImage=web\n",
		"app/worker.container": "[Container]\nImage=worker\n",
	}, quadlets)
	selectAll(e, "app")
	e.sync()
	e.apply()

	e.remote.Commit("drop the worker", map[string]string{"app/worker.container": ""})
	if got := planned(t, e.sync())["app"]; got != "redeploy" {
		t.Fatalf("planned %q", got)
	}
	before := len(e.hostLines())
	e.apply()

	got := e.hostLines()[before:]
	want := []string{"systemctl --user stop -- worker.service", "systemctl --user daemon-reload", "systemctl --user restart -- web.service"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if exists(filepath.Join(quadlets, "app", "worker.container")) {
		t.Error("removed unit file still deployed")
	}
}

func TestQuadletTeardownRemovesOnlyTheStacksDirectory(t *testing.T) {
	quadlets := t.TempDir()
	os.WriteFile(filepath.Join(quadlets, "manual.container"), []byte("[Container]\n"), 0o644)
	os.MkdirAll(filepath.Join(quadlets, "mine"), 0o755)
	os.WriteFile(filepath.Join(quadlets, "mine", "x.container"), []byte("[Container]\n"), 0o644)

	e := newPodmanEnvIn(t, map[string]string{
		"app/app.container": "[Container]\n",
		"app/data.volume":   "[Volume]\n",
	}, quadlets)
	selectAll(e, "app")
	e.sync()
	e.apply()
	e.setSelected("app", false)
	if got := planned(t, e.sync())["app"]; got != "teardown" {
		t.Fatalf("planned %q", got)
	}
	before := len(e.hostLines())
	e.apply()

	got := e.hostLines()[before:]
	want := []string{"systemctl --user stop -- app.service data-volume.service", "systemctl --user daemon-reload"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if exists(filepath.Join(quadlets, "app")) {
		t.Error("the Stack's Quadlet directory should be gone")
	}
	if !exists(filepath.Join(quadlets, "manual.container")) || !exists(filepath.Join(quadlets, "mine", "x.container")) {
		t.Error("hand-placed Quadlet files were touched")
	}
	for _, l := range e.rec.Lines() {
		if strings.Contains(l, "volume rm") || strings.Contains(l, "volume prune") {
			t.Errorf("teardown removed a volume: %q", l)
		}
	}
}

func TestQuadletSecretsDropInFollowsTheSecretsFile(t *testing.T) {
	quadlets := t.TempDir()
	e := newPodmanEnvIn(t, map[string]string{"app/app.container": "[Container]\n"}, quadlets)
	selectAll(e, "app")
	e.sync()
	e.apply()
	dropIn := filepath.Join(quadlets, "app", "app.container.d", "scd-secrets.conf")
	if exists(dropIn) {
		t.Fatal("drop-in written without a secrets.env")
	}

	secrets := e.placeSecrets("app", "T=1\n")
	if got := planned(t, e.sync())["app"]; got != "redeploy" {
		t.Fatalf("adding secrets planned %q", got)
	}
	e.apply()
	assertContains(t, readFile(t, dropIn), "EnvironmentFile="+secrets)

	os.Remove(secrets)
	e.sync()
	e.apply()
	if exists(dropIn) {
		t.Error("drop-in left behind after secrets.env was removed")
	}
}

func TestQuadletUnitNamesMustBeUniqueAcrossTheRepo(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{
		"a/web.container": "[Container]\n",
		"b/web.container": "[Container]\n",
		"c/web.kube":      "[Kube]\n",
		"d/db.container":  "[Container]\n",
	})
	selectAll(e, "a", "b", "c", "d")
	body := e.sync()
	got := planned(t, body)
	for _, s := range []string{"a", "b", "c"} {
		if got[s] != "invalid" {
			t.Errorf("%s planned %q, want invalid", s, got[s])
		}
	}
	if got["d"] != "deploy" {
		t.Errorf("d planned %q", got["d"])
	}
	assertContains(t, planReason(body, "a"), "isn&#39;t unique", "a/web.container", "b/web.container", "c/web.kube", "web.service")
}

func TestQuadletNeedsARecentPodman(t *testing.T) {
	e := newPodmanEnv(t, map[string]string{"app/app.container": "[Container]\n"})
	e.rec.On("podman version", runnertest.Response{Output: "4.4.1\n"})
	selectAll(e, "app")
	body := e.sync()
	assertContains(t, planReason(body, "app"), "Podman 4.4.1 is too old", "4.8 or newer")
}

func TestQuadletGeneratorMissingIsReported(t *testing.T) {
	for _, p := range []string{"/usr/libexec/podman/quadlet", "/usr/lib/podman/quadlet", "/usr/lib/systemd/system-generators/podman-system-generator"} {
		if exists(p) {
			t.Skip("this machine has a real quadlet generator")
		}
	}
	e := newPodmanEnvIn(t, map[string]string{"app/app.container": "[Container]\n"}, t.TempDir(), "")
	selectAll(e, "app")
	assertContains(t, planReason(e.sync(), "app"), "quadlet generator wasn&#39;t found")
}
