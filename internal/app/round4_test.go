package app_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
)

func TestCrashDetailsAreShown(t *testing.T) {
	e := newSyncEnv(t, compose("ha"))
	e.deployed("ha")
	selectAll(e, "ha")
	e.api.SetContainers(dockertest.Container{ID: "h1", Name: "ha-core-1", State: "running", Health: "unhealthy",
		RestartCount: 4, ExitCode: 137, OOMKilled: true, FinishedAt: time.Now().Add(-3 * time.Minute),
		HealthOutput: "curl: (7) Failed to connect to localhost port 8123", Labels: composeLabel("ha")})

	_, overview := e.get("/")
	_, att, _ := strings.Cut(overview, `id="attention"`)
	assertContains(t, att, `Last exit <b class="mono">137</b>`, "killed: out of memory", "3m ago", "Last failing healthcheck", "Failed to connect to localhost port 8123")

	_, logs := e.get("/containers/h1/logs")
	assertContains(t, logs, "crash-detail", "killed: out of memory", "Failed to connect to localhost port 8123")
}

func TestPendingChangesBarAndApplyHint(t *testing.T) {
	e := newSyncEnv(t, merge(compose("leaving"), compose("arriving")))
	e.deployed("leaving")
	e.setSelected("arriving", true)

	_, dep := e.get("/deployment")
	assertContains(t, dep, "2 pending changes:", "deploy <b>arriving</b>", "tear down <b>leaving</b>", "Sync now",
		`title="Nothing to apply yet: press Sync to fetch and review a plan"`)
	_, c := e.get("/containers")
	if strings.Contains(c, "pending-bar") {
		t.Error("the Containers page shouldn't carry the pending bar")
	}

	body := e.sync() // a fresh plan now covers the changes
	if strings.Contains(body, "pending-bar") {
		t.Error("the pending bar should give way to the Sync Plan")
	}
	if strings.Contains(body, `class="apply-button" disabled`) {
		t.Error("Apply should be enabled with a plan that changes things")
	}
}

func TestOverviewIsASummary(t *testing.T) {
	e := newSyncEnv(t, merge(compose("ha"), compose("web")))
	e.deployed("ha", "web")
	selectAll(e, "ha", "web")
	e.api.SetContainers(
		dockertest.Container{ID: "h1", Name: "ha-core-1", State: "running", Health: "unhealthy", Labels: composeLabel("ha")},
		dockertest.Container{ID: "w1", Name: "web-1", State: "running", Labels: composeLabel("web")},
	)
	_, o := e.get("/")
	assertContains(t, o, `class="summary-row si-err"`, `href="/containers?q=ha"`, "1 unhealthy · 1/1 running",
		`class="summary-row si-ok"`, `href="/deployment">Manage Stacks`, "Last Apply", "CPU usage")
	for _, absent := range []string{`id="containers"`, `id="stacks"`, `class="switch"`} {
		if strings.Contains(o, absent) {
			t.Errorf("the Overview shouldn't contain %s", absent)
		}
	}
}

func TestEachPageHasItsOwnHeading(t *testing.T) {
	e := newSyncEnv(t, compose("app"))
	for path, h1 := range map[string]string{
		"/": "Overview of testhost", "/containers": "Containers of testhost", "/deployment": "Deployment of testhost",
		"/containers?filter=stopped": "Containers: Not running of testhost",
	} {
		_, body := e.get(path)
		assertContains(t, body, `<h1 class="visually-hidden">`+h1+`</h1>`)
	}
	_, dep := e.get("/deployment")
	assertContains(t, dep, `<h3 class="list-heading">Not on this host</h3>`, `<h4 class="stack-name">app</h4>`,
		`Run here<span class="visually-hidden">: app</span>`)
}
