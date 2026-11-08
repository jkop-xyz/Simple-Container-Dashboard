package app_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
)

func round5Env(t *testing.T) *syncEnv {
	t.Helper()
	e := newSyncEnv(t, merge(compose("ha"), compose("web")))
	e.deployed("ha", "web")
	selectAll(e, "ha", "web")
	e.api.SetContainers(
		dockertest.Container{ID: "h1", Name: "ha-core-1", State: "running", Health: "unhealthy", Labels: composeLabel("ha")},
		dockertest.Container{ID: "w1", Name: "web-1", State: "running", Labels: composeLabel("web")},
		dockertest.Container{ID: "x1", Name: "scratch", State: "exited", Status: "Exited (1) 1 hour ago"},
	)
	return e
}

func TestStatusNeverContradictsItself(t *testing.T) {
	e := round5Env(t)
	_, o := e.get("/")
	assertContains(t, o, `class="summary-row si-err">`, "1 needs attention · 1 container", `class="summary-state bad">1 unhealthy</span>`)
	if strings.Contains(o, "1 unhealthy · 1/1 running") {
		t.Error("a problem and \"all running\" in one breath")
	}
}

func TestSubpagesKeepTheHeaderAndNavigationCounts(t *testing.T) {
	e := round5Env(t)
	for _, path := range []string{"/history", "/containers/h1/logs"} {
		_, body := e.get(path)
		assertContains(t, body, `class="sync-button`, `class="apply-button"`, `data-count="attention">2<`, `class="chip-repo"`)
	}
	_, logs := e.get("/containers/h1/logs")
	assertContains(t, logs, `href="/containers">Containers</a>`, `href="/containers?q=ha">ha</a>`, "ha-core-1 logs")
}

func TestSyncFromAnywhereButTheOverviewShowsThePlan(t *testing.T) {
	e := round5Env(t)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/sync", nil)
	req.Header.Set("Referer", e.srv.URL+"/containers")
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/deployment#sync" {
		t.Errorf("redirected to %q", loc)
	}
	req.Header.Set("Referer", e.srv.URL+"/")
	resp, _ = noRedirect.Do(req)
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/#sync" {
		t.Errorf("from the Overview redirected to %q", loc)
	}
}

func TestEachContainerRowHasOneTabStop(t *testing.T) {
	e := round5Env(t)
	r := row(mustGet(e.harness), "w1")
	assertContains(t, r, `class="name-link"`, `class="icon-btn logs-link" href="/containers/w1/logs" tabindex="-1" aria-hidden="true"`)
}
