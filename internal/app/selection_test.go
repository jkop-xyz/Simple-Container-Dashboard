package app_test

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/gittest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
)

// selectionHarness is a dashboard with a two-Stack Deployment Repo, running real git
// through a recording fake so tests can assert which commands were issued.
func selectionHarness(t *testing.T, dataDir string) (*harness, *runnertest.Fake) {
	t.Helper()
	remote := gittest.NewRemote(t)
	remote.Commit("stacks", map[string]string{
		"jellyfin/compose.yaml": "services: {}\n",
		"traefik/compose.yaml":  "services: {}\n",
	})
	rec := gitOnly()
	clone := filepath.Join(t.TempDir(), "repo")
	h := startDashboard(t, withRepo(remote.URL, clone), func(o *app.Options) {
		o.Runner = rec
		o.DataDir = dataDir
	})
	return h, rec
}

// noRedirect returns the selection endpoint's own response rather than following it.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func (h *harness) setSelected(stack string, selected bool) int {
	h.t.Helper()
	form := url.Values{}
	if selected {
		form.Set("selected", "true")
	}
	resp, err := noRedirect.PostForm(h.srv.URL+"/stacks/"+stack+"/selection", form)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func isChecked(section string) bool {
	return strings.Contains(section, `value="true" checked`)
}

func TestStacksAreUnselectedByDefault(t *testing.T) {
	h, _ := selectionHarness(t, t.TempDir())
	sections := splitSections(mustGet(h))
	for _, name := range []string{"jellyfin", "traefik"} {
		if isChecked(sections[name]) || !strings.Contains(sections[name], "not selected") {
			t.Errorf("Stack %s should start unselected and say so", name)
		}
	}
}

func TestSelectingAndUnselectingAStack(t *testing.T) {
	h, _ := selectionHarness(t, t.TempDir())

	if code := h.setSelected("jellyfin", true); code != http.StatusSeeOther {
		t.Fatalf("select: status %d", code)
	}
	sections := splitSections(mustGet(h))
	if !isChecked(sections["jellyfin"]) || strings.Contains(sections["jellyfin"], "not selected") {
		t.Error("jellyfin should be selected")
	}
	if isChecked(sections["traefik"]) {
		t.Error("traefik should still be unselected")
	}

	h.setSelected("jellyfin", false)
	if isChecked(splitSections(mustGet(h))["jellyfin"]) {
		t.Error("jellyfin should be unselected again")
	}
}

func TestSelectionSurvivesRestart(t *testing.T) {
	dataDir := t.TempDir()
	first, _ := selectionHarness(t, dataDir)
	first.setSelected("traefik", true)

	second, _ := selectionHarness(t, dataDir)
	if !isChecked(splitSections(mustGet(second))["traefik"]) {
		t.Error("selection was lost across a restart")
	}
}

func TestSelectingAnUnknownStackIs404(t *testing.T) {
	h, _ := selectionHarness(t, t.TempDir())
	if code := h.setSelected("nope", true); code != http.StatusNotFound {
		t.Errorf("status %d, want 404", code)
	}
}

func TestChangingSelectionDeploysNothing(t *testing.T) {
	h, rec := selectionHarness(t, t.TempDir())
	h.setSelected("jellyfin", true)
	h.setSelected("traefik", true)
	h.setSelected("jellyfin", false)

	for _, line := range rec.Lines() {
		if !strings.HasPrefix(line, "git ") {
			t.Errorf("selection change ran %q; only read-only git calls are allowed", line)
		}
		for _, verb := range []string{" fetch", " pull", " merge", " reset", " checkout"} {
			if strings.Contains(line, verb) {
				t.Errorf("selection change ran %q", line)
			}
		}
	}
}

func mustGet(h *harness) string {
	h.t.Helper()
	code, body := h.get("/")
	if code != http.StatusOK {
		h.t.Fatalf("GET /: status %d\n%s", code, body)
	}
	return body
}
