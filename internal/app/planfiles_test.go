package app_test

import (
	"strings"
	"testing"
)

func planRow(body, stack string) string {
	_, rest, _ := strings.Cut(body, `<tr class="plan-entry" data-stack="`+stack+`"`)
	r, _, _ := strings.Cut(rest, "</tr>")
	return r
}

func TestSyncPlanListsChangedFilesPerStack(t *testing.T) {
	e := newSyncEnv(t, merge(
		map[string]string{"app/compose.yaml": "services:\n  web:\n    image: nginx:1.26\n"},
		compose("same"),
	))
	e.deployed("app", "same")
	selectAll(e, "app", "same")
	e.remote.Commit("bump nginx, add config", map[string]string{
		"app/compose.yaml":        "services:\n  web:\n    image: nginx:1.27\n    restart: always\n",
		"app/config/default.conf": "server {}\n",
		"app/logo.png":            "\x89PNG\x00\x01\x02binary",
		"fresh/compose.yaml":      "services: {}\n",
	})
	e.sync()
	selectAll(e, "fresh")

	body := e.sync()
	app := planRow(body, "app")
	assertContains(t, app, "3 files changed", "<code>compose.yaml</code>", `class="add">+2<`, `class="del">−1<`,
		"<code>config/default.conf</code>", `class="add">+1<`, "<code>logo.png</code>", "binary")
	assertContains(t, planRow(body, "fresh"), "1 file changed", "<code>compose.yaml</code>")
	if strings.Contains(planRow(body, "same"), "changed</summary>") {
		t.Error("an unchanged Stack lists files")
	}
}

func TestSecretsOnlyRedeployListsNoFilesAndNoSecrets(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	e.placeSecrets("app", "TOKEN=old-value\n")
	selectAll(e, "app")
	e.sync()
	e.apply()
	e.placeSecrets("app", "TOKEN=very-secret-new-value\n")

	body := e.sync()
	row := planRow(body, "app")
	assertContains(t, row, "secrets changed")
	if strings.Contains(row, "changed</summary>") {
		t.Error("a secrets-only redeploy lists files")
	}
	if strings.Contains(body, "very-secret-new-value") || strings.Contains(body, "old-value") {
		t.Fatal("secret contents leaked into the page")
	}
}

func TestTeardownsListNoFiles(t *testing.T) {
	e := newSyncEnv(t, compose("gone"))
	e.deployed("gone")
	e.remote.Commit("retire", map[string]string{"gone/compose.yaml": ""})
	row := planRow(e.sync(), "gone")
	assertContains(t, row, "teardown")
	if strings.Contains(row, "<details") {
		t.Error("a teardown lists files")
	}
}
