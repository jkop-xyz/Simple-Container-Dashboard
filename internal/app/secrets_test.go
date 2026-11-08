package app_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// placeSecrets writes a Secrets File on the host, as the operator would by hand.
func (e *syncEnv) placeSecrets(stack, content string) string {
	e.t.Helper()
	p := filepath.Join(e.clone, stack, "secrets.env")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func TestSecretsFileIsPassedAsEnvFile(t *testing.T) {
	e := newSyncEnv(t, merge(compose("with"), compose("without")))
	secrets := e.placeSecrets("with", "DB_PASSWORD=hunter2\n")
	selectAll(e, "with", "without")
	e.sync()
	e.apply()

	for _, l := range e.rec.Lines() {
		switch {
		case strings.Contains(l, "--project-name with "):
			if !strings.Contains(l, "--env-file "+secrets+" ") {
				t.Errorf("Stack with secrets.env ran without it: %q", l)
			}
		case strings.Contains(l, "--project-name without "):
			if strings.Contains(l, "--env-file") {
				t.Errorf("Stack without secrets.env got an env file: %q", l)
			}
		}
	}
	if len(e.composeLines()) != 2 {
		t.Errorf("both Stacks should deploy, got %q", e.composeLines())
	}
}

func TestChangedSecretsTriggerARedeploy(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	e.placeSecrets("app", "TOKEN=old\n")
	selectAll(e, "app")
	e.sync()
	e.apply()
	if got := planned(t, e.sync())["app"]; got != "unchanged" {
		t.Fatalf("after Apply planned %q, want unchanged", got)
	}

	e.placeSecrets("app", "TOKEN=rotated\n")
	body := e.sync()
	if got := planned(t, body)["app"]; got != "redeploy" {
		t.Fatalf("after rotating secrets planned %q, want redeploy", got)
	}
	assertContains(t, planReason(body, "app"), "secrets changed")
}

func TestAddingOrRemovingSecretsTriggersARedeploy(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	selectAll(e, "app")
	e.sync()
	e.apply()

	p := e.placeSecrets("app", "NEW=1\n")
	if got := planned(t, e.sync())["app"]; got != "redeploy" {
		t.Errorf("adding secrets.env planned %q", got)
	}
	e.apply()
	os.Remove(p)
	if got := planned(t, e.sync())["app"]; got != "redeploy" {
		t.Errorf("removing secrets.env planned %q", got)
	}
}

func TestSyncRequiresSecretsToBeGitIgnored(t *testing.T) {
	e := newSyncEnv(t, merge(compose("app"), map[string]string{".gitignore": "node_modules/\n"}))
	body := e.sync()
	assertContains(t, body, "Sync failed", "add secrets.env to the Deployment Repo&#39;s root .gitignore")

	e.remote.Commit("ignore secrets", map[string]string{".gitignore": "node_modules/\n**/secrets.env\n"})
	if body := e.sync(); strings.Contains(body, "Sync failed") {
		t.Errorf("**/secrets.env should satisfy the check:\n%s", body)
	}
}

func TestCommittedSecretsBlockTheFastForward(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	local := e.placeSecrets("app", "MINE=1\n")
	selectAll(e, "app")
	before := e.head()
	e.remote.Commit("oops, committed a secret", map[string]string{"app/secrets.env": "LEAKED=1\n"})

	body := e.sync()
	if got := planned(t, body)["app"]; got != "invalid" {
		t.Errorf("planned %q, want invalid", got)
	}
	assertContains(t, planReason(body, "app"), "secrets.env committed to git")
	assertContains(t, body, "blocked", "plan can", "tracks app/secrets.env")
	if !strings.Contains(body, `class="apply-button" disabled`) {
		t.Error("Apply should be disabled")
	}

	_, page := e.apply()
	assertContains(t, page, "apply-error", "tracks app/secrets.env")
	if e.head() != before {
		t.Error("the clone was fast-forwarded onto a commit that tracks secrets.env")
	}
	if b, _ := os.ReadFile(local); string(b) != "MINE=1\n" {
		t.Errorf("host secrets.env overwritten: %q", b)
	}
}

func TestLeftoverSecretsFileIsNotAStack(t *testing.T) {
	e := newSyncEnv(t, merge(compose("old"), compose("keep")))
	secrets := e.placeSecrets("old", "OLD=1\n")
	selectAll(e, "old", "keep")
	e.sync()
	e.apply()

	e.remote.Commit("retire old", map[string]string{"old/compose.yaml": ""})
	body := e.sync()
	if got := planned(t, body)["old"]; got != "teardown" {
		t.Fatalf("planned %q, want teardown", got)
	}
	e.apply()

	// The directory now holds only the operator's secrets.env: not a Stack, and untouched.
	if _, ok := splitSections(mustGet(e.harness))["old"]; ok {
		t.Error("a directory holding only secrets.env is shown as a Stack")
	}
	if _, ok := planned(t, e.sync())["old"]; ok {
		t.Error("a directory holding only secrets.env was planned")
	}
	if b, err := os.ReadFile(secrets); err != nil || string(b) != "OLD=1\n" {
		t.Errorf("leftover secrets.env changed: %q %v", b, err)
	}
}

func TestScdNeverWritesSecretsFiles(t *testing.T) {
	e := newSyncEnv(t, merge(compose("a"), compose("b")))
	paths := []string{e.placeSecrets("a", "A=1\n"), e.placeSecrets("b", "B=1\n")}
	old := time.Now().Add(-time.Hour)
	for _, p := range paths {
		os.Chtimes(p, old, old)
	}
	stat := func() []string {
		var out []string
		for _, p := range paths {
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(p)
			out = append(out, fi.ModTime().String()+fi.Mode().String()+string(b))
		}
		return out
	}
	before := stat()

	selectAll(e, "a", "b")
	e.sync()
	e.apply()
	e.remote.Commit("change", map[string]string{"a/compose.yaml": "services: {x: {image: y}}\n"})
	e.setSelected("b", false)
	e.sync()
	e.apply()

	if after := stat(); strings.Join(after, "|") != strings.Join(before, "|") {
		t.Errorf("secrets.env files changed:\nbefore %q\nafter  %q", before, after)
	}
	for _, c := range e.rec.Calls() {
		if strings.Contains(c.String(), "secrets.env") && !strings.Contains(c.String(), "--env-file") {
			t.Errorf("command touched secrets.env other than as an env file: %q", c)
		}
	}
}
