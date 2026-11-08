package app_test

import (
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
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
		assertContains(t, body, "Showing only", `href="/"`, "Show everything", `nav-item is-active" href="/?filter=`+filter+`" aria-current="page"`)
		if strings.Contains(body, "Not on this host") {
			t.Errorf("filter=%s still lists inactive Stacks", filter)
		}
	}
}

func TestTilesLinkToFiltersAndCountTheWholeHost(t *testing.T) {
	e := filterEnv(t)
	_, body := e.get("/?filter=unmanaged")
	// The side navigation keeps describing the whole host while a filter is active.
	assertContains(t, body, `href="/?filter=attention"`, `<span class="nav-count nav-count-bad">1</span>`,
		`href="/?filter=stopped"`, `href="/?filter=selected"`)
	_, overview := e.get("/")
	assertContains(t, overview, `data-tile="running">3<small>/ 4</small>`, `data-tile="attention">1<`, "1 not running · show")
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
