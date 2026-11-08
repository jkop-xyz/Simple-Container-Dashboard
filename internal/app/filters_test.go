package app_test

import (
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/view"
)

func filterEnv(t *testing.T) *syncEnv {
	t.Helper()
	e := newSyncEnv(t, merge(compose("ha"), compose("web"), compose("idle")))
	e.deployed("ha", "web")
	selectAll(e, "ha", "web")
	e.api.SetContainers(
		dockertest.Container{ID: "h1", Name: "ha-core-1", State: "running", Health: "unhealthy", Labels: composeLabel("ha")},
		dockertest.Container{ID: "h2", Name: "ha-db-1", State: "running", Labels: composeLabel("ha")},
		dockertest.Container{ID: "w1", Name: "web-1", State: "exited", Status: "Exited (0) 1 hour ago", Labels: composeLabel("web")},
		dockertest.Container{ID: "o1", Name: "adhoc", State: "running"},
	)
	return e
}

func names(body string) []string {
	var out []string
	for _, part := range strings.Split(body, `<span class="name">`)[1:] {
		n, _, _ := strings.Cut(part, "<")
		out = append(out, n)
	}
	return out
}

func TestFiltersNarrowTheContainerList(t *testing.T) {
	e := filterEnv(t)
	for filter, want := range map[string]string{
		"attention": "ha-core-1",
		"stopped":   "web-1",
		"unmanaged": "adhoc",
		"selected":  "ha-core-1,ha-db-1,web-1",
	} {
		_, body := e.get("/?filter=" + filter)
		if got := strings.Join(names(body), ","); got != want {
			t.Errorf("filter=%s shows %q, want %q", filter, got, want)
		}
		assertContains(t, body, "Showing only", "Show everything", `aria-current="page">`+view.FilterTitles[filter]+`</span>`)
		if filter != "selected" { // no nav item: Stacks live on the Deployment page
			assertContains(t, body, `nav-item is-active" href="/containers?filter=`+filter+`" aria-current="page"`)
		}
		if strings.Contains(body, "Not on this host") {
			t.Errorf("filter=%s still lists inactive Stacks", filter)
		}
	}
}

func TestTilesLinkToFiltersAndCountTheWholeHost(t *testing.T) {
	e := filterEnv(t)
	_, body := e.get("/?filter=unmanaged")
	// The side navigation keeps describing the whole host while a filter is active.
	assertContains(t, body, `href="/containers?filter=attention"`, `<span class="nav-count nav-count-bad" data-count="attention">1</span>`,
		`href="/containers?filter=stopped"`)
	_, overview := e.get("/")
	assertContains(t, overview, `data-count="total">4<`, `data-count="stopped">1<`, "3 of 4 running")
	_, all := e.get("/")
	if strings.Contains(all, "Showing only") {
		t.Error("no filter, but the filter bar shows")
	}
	if got := len(names(all)); got != 4 {
		t.Errorf("unfiltered shows %d containers, want 4", got)
	}
	_, unknown := e.get("/?filter=bogus")
	if strings.Contains(unknown, "Showing only") || len(names(unknown)) != 4 {
		t.Error("an unknown filter should show everything")
	}
}

func TestRisingRestartCountIsFlaggedAsCrashLooping(t *testing.T) {
	e := newSyncEnv(t, compose("ha"))
	e.deployed("ha")
	selectAll(e, "ha")
	c := dockertest.Container{ID: "h1", Name: "ha-core-1", State: "running", RestartCount: 3, Labels: composeLabel("ha")}
	e.api.SetContainers(c)

	body := mustGet(e.harness)
	if strings.Contains(body, `is-crashloop`) {
		t.Fatal("a steady restart count isn't a crash loop")
	}
	c.RestartCount = 4
	e.api.SetContainers(c)

	body = mustGet(e.harness)
	assertContains(t, body, `is-crashloop" data-id="h1"`, "1 crash-looping · 1/1 running", "crash-looping · restarted 4×")
	_, raw := e.get("/api/status")
	assertContains(t, raw, `"crashLooping":true`)
}

func TestContainersAndDeploymentArePagesOfTheirOwn(t *testing.T) {
	e := filterEnv(t)
	_, c := e.get("/containers")
	assertContains(t, c, `class="crumb crumb-home" href="/">Overview</a>`, `aria-current="page">Containers</span>`,
		`class="nav-item nav-top is-active" href="/containers" aria-current="page"`, `id="containers"`)
	for _, absent := range []string{`id="stacks"`, `id="attention"`, `class="charts"`} {
		if strings.Contains(c, absent) {
			t.Errorf("/containers shouldn't contain %s", absent)
		}
	}
	_, d := e.get("/deployment")
	assertContains(t, d, `aria-current="page">Deployment</span>`, `class="nav-item nav-top is-active" href="/deployment" aria-current="page"`,
		`id="stacks"`, "Apply history", `href="/history"`)
	if strings.Contains(d, `id="containers"`) {
		t.Error("/deployment shouldn't show the Containers table")
	}
	_, o := e.get("/")
	assertContains(t, o, `aria-current="page">Overview</span>`, `class="nav-item nav-top is-active" href="/" aria-current="page"`)
	// Old filter links land on the Containers page.
	_, old := e.get("/?filter=stopped")
	assertContains(t, old, `aria-current="page">Not running</span>`, `href="/containers">Containers</a>`)
	// The history sits under Deployment.
	_, h := e.get("/history")
	assertContains(t, h, `href="/deployment">Deployment</a>`, `aria-current="page">Apply history`)
}

func TestHeaderStaysPinned(t *testing.T) {
	_, css := startDashboard(t).get("/static/style.css")
	assertContains(t, css, ".header { position: sticky; top: 0;", "scroll-margin-top: calc(var(--header)")
	_, body := startDashboard(t).get("/")
	if !strings.Contains(body, `<div class="header">`) || strings.Index(body, `class="toolbar"`) > strings.Index(body, `<div class="layout">`) {
		t.Error("the app bar and toolbar should share the sticky header")
	}
}
