package app_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/gittest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
)

// gitOnly records every command, really runs only git, and answers everything else
// (docker, systemctl, ...) with an unscripted success, so tests never touch the host.
func gitOnly() *runnertest.Fake {
	return &runnertest.Fake{Delegate: runner.Exec{}, DelegateOnly: []string{"git"}}
}

// withRepo configures a Deployment Repo cloned with real git into a temp dir.
func withRepo(url, path string) func(*app.Options) {
	return func(o *app.Options) {
		o.Runner = gitOnly()
		o.Config.Repo = config.Repo{URL: url, Branch: "main", Path: path}
	}
}

func composeLabel(project string) map[string]string {
	return map[string]string{"com.docker.compose.project": project}
}

func TestStartClonesMissingDeploymentRepo(t *testing.T) {
	remote := gittest.NewRemote(t)
	head := remote.Commit("add jellyfin", map[string]string{"jellyfin/compose.yaml": "services: {}\n"})
	clone := filepath.Join(t.TempDir(), "nested", "repo")

	h := startDashboard(t, withRepo(remote.URL, clone))

	if got := strings.TrimSpace(gittest.Git(t, clone, "rev-parse", "HEAD")); got != head {
		t.Fatalf("clone at %s, want %s", got, head)
	}
	_, body := h.get("/")
	assertContains(t, body, `title="`+head+`"`, ">"+head[:7]+"<")
}

func TestContainersAreGroupedByStack(t *testing.T) {
	remote := gittest.NewRemote(t)
	remote.Commit("stacks", map[string]string{
		"jellyfin/compose.yaml": "services: {}\n",
		"traefik/compose.yaml":  "services: {}\n",
		"App1/compose.yaml":     "services: {}\n",
		"empty/compose.yaml":    "services: {}\n",
	})
	h := startDashboard(t, withRepo(remote.URL, filepath.Join(t.TempDir(), "repo")))
	h.api.SetContainers(
		dockertest.Container{ID: "1", Name: "jellyfin-1", State: "running", Labels: composeLabel("jellyfin")},
		dockertest.Container{ID: "2", Name: "traefik-1", State: "running", Labels: composeLabel("traefik")},
		dockertest.Container{ID: "3", Name: "app1-web-1", State: "running", Labels: composeLabel("app1")},
		dockertest.Container{ID: "4", Name: "handmade", State: "exited"},
		dockertest.Container{ID: "5", Name: "stray-1", State: "running", Labels: composeLabel("notinrepo")},
	)

	_, body := h.get("/")
	sections := splitSections(body)
	want := map[string][]string{
		"App1":     {"app1-web-1"},
		"empty":    {"not selected · no containers"},
		"jellyfin": {"jellyfin-1"},
		"traefik":  {"traefik-1"},
		"":         {"Other", "unmanaged", "handmade", "stray-1"},
	}
	for stack, items := range want {
		sec, ok := sections[stack]
		if !ok {
			t.Errorf("no section for Stack %q", stack)
			continue
		}
		assertContains(t, sec, items...)
	}
	if strings.Contains(sections["jellyfin"], "traefik-1") || strings.Contains(sections[""], "jellyfin-1") {
		t.Error("containers leaked into the wrong group")
	}
}

func TestHiddenDirsAndRootFilesAreNotStacks(t *testing.T) {
	remote := gittest.NewRemote(t)
	remote.Commit("tooling", map[string]string{
		".github/compose.yaml": "services: {}\n",
		"compose.yaml":         "services: {}\n",
		"README.md":            "hi\n",
		"notes/todo.txt":       "x\n",
		"real/compose.yaml":    "services: {}\n",
	})
	h := startDashboard(t, withRepo(remote.URL, filepath.Join(t.TempDir(), "repo")))

	_, body := h.get("/")
	sections := splitSections(body)
	if len(sections) != 2 || sections["real"] == "" {
		t.Errorf("want Stacks real and notes, got sections %v", keys(sections))
	}
	// A directory without a definition is an Invalid Stack, not silently ignored.
	assertContains(t, sections["notes"], "invalid", "has no compose.yaml and no Quadlet files")
}

func TestHeaderShowsDeploymentRepo(t *testing.T) {
	remote := gittest.NewRemote(t)
	remote.Commit("init", map[string]string{"a/compose.yaml": "services: {}\n"})
	h := startDashboard(t, withRepo(remote.URL, filepath.Join(t.TempDir(), "repo")))

	_, body := h.get("/")
	assertContains(t, body, `class="repo">`+remote.URL+`<`)
}

func TestExistingCloneIsNeverTouched(t *testing.T) {
	remote := gittest.NewRemote(t)
	first := remote.Commit("v1", map[string]string{"a/compose.yaml": "v1\n"})
	clone := filepath.Join(t.TempDir(), "repo")
	gittest.Git(t, filepath.Dir(clone), "clone", "--quiet", remote.URL, clone)

	// Local state scd must not disturb: an edit, an untracked file, and newer remote commits.
	edited := filepath.Join(clone, "a", "compose.yaml")
	os.WriteFile(edited, []byte("local edit\n"), 0o644)
	os.WriteFile(filepath.Join(clone, "a", "secrets.env"), []byte("TOKEN=x\n"), 0o600)
	remote.Commit("v2", map[string]string{"a/compose.yaml": "v2\n"})

	h := startDashboard(t, withRepo(remote.URL, clone))
	h.get("/")

	if got := strings.TrimSpace(gittest.Git(t, clone, "rev-parse", "HEAD")); got != first {
		t.Errorf("clone moved to %s, want it left at %s", got, first)
	}
	if b, _ := os.ReadFile(edited); string(b) != "local edit\n" {
		t.Errorf("local edit was changed to %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "a", "secrets.env")); string(b) != "TOKEN=x\n" {
		t.Errorf("untracked file was changed to %q", b)
	}
}

func TestFailedCloneIsShownOnDashboard(t *testing.T) {
	gittest.Isolate(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.git")
	h := startDashboard(t, withRepo(missing, filepath.Join(t.TempDir(), "repo")))

	code, body := h.get("/")
	if code != 200 {
		t.Errorf("status %d: a broken repo must not break the container view", code)
	}
	assertContains(t, body, "Deployment Repo unavailable", "cloning Deployment Repo")
}

func TestNonEmptyNonGitClonePathIsRefused(t *testing.T) {
	remote := gittest.NewRemote(t)
	remote.Commit("init", map[string]string{"a/compose.yaml": "x\n"})
	clone := t.TempDir()
	os.WriteFile(filepath.Join(clone, "precious.txt"), []byte("keep\n"), 0o644)

	h := startDashboard(t, withRepo(remote.URL, clone))
	_, body := h.get("/")
	assertContains(t, body, "is not a git clone, and is not empty")
	if b, _ := os.ReadFile(filepath.Join(clone, "precious.txt")); string(b) != "keep\n" {
		t.Error("existing file was touched")
	}
}

// splitSections returns each Stack's HTML keyed by its data-stack attribute ("" is the
// Other group): its group in the Containers table and its row in the Stacks card,
// concatenated.
func splitSections(body string) map[string]string {
	out := map[string]string{}
	re := regexp.MustCompile(`<(tbody|li|section) class="stack[^"]*" data-stack="([^"]*)"`)
	for _, m := range re.FindAllStringSubmatchIndex(body, -1) {
		tag, name := body[m[2]:m[3]], body[m[4]:m[5]]
		rest := body[m[1]:]
		html, _, _ := strings.Cut(rest, "</"+tag+">")
		out[name] += html
	}
	return out
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
