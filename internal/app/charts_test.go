package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
)

func TestMetricsAreSampledForTheCharts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := startDashboard(t, func(o *app.Options) { o.SampleEvery = 20 * time.Millisecond })
	h.app.Start(ctx) // starts the sampler with this test's context
	h.api.SetContainers(dockertest.Container{ID: "c1", Name: "web", State: "running", Stats: dockertest.Stats{
		CPUTotal: 2e9, SystemCPU: 20e9, PreCPUTotal: 1e9, PreSystemCPU: 10e9, OnlineCPUs: 2, MemoryUsage: 512 << 20,
	}})

	var got struct {
		CPU    []struct{ V float64 } `json:"cpu"`
		Memory []struct{ V float64 } `json:"memory"`
	}
	waitFor(t, func() bool {
		resp, err := http.Get(h.srv.URL + "/api/metrics")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		json.NewDecoder(resp.Body).Decode(&got)
		return len(got.CPU) >= 3
	})
	last := got.Memory[len(got.Memory)-1].V
	if last != 512<<20 {
		t.Errorf("host memory %v, want %v", last, 512<<20)
	}
	if got.CPU[0].V != 0.2 { // (1/10) × 2 CPUs = 20 % = 0.2 cores
		t.Errorf("first CPU point %v cores, want 0.2", got.CPU[0].V)
	}
	_, raw := h.get("/api/status")
	if !strings.Contains(raw, `"spark":"M`) || !strings.Contains(raw, `"memoryBytes":536870912`) {
		t.Errorf("status lacks sparkline/memory: %s", raw)
	}
	assertContains(t, row(mustGet(h), "c1"), `class="spark-fill`, `d="M`, `membar-fill" style="width: 100%"`)
}

func TestMetricsStartEmpty(t *testing.T) {
	_, raw := startDashboard(t).get("/api/metrics")
	if strings.TrimSpace(raw) != `{"cpu":[],"memory":[]}` {
		t.Errorf("got %s", raw)
	}
}

func TestShellHasSearchBreadcrumbsAndNavigation(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "a", Name: "web", Image: "nginx:1.27", State: "running"})
	body := mustGet(h)
	assertContains(t, body, `class="global-search"`, `aria-current="page">Overview`, `class="nav-item nav-top is-active" href="/"`,
		`href="/history"`, `data-name="web" data-image="nginx:1.27"`, "CPU usage", "Memory usage", "si-ok", `id="containers"`)

	_, hist := h.get("/history")
	assertContains(t, hist, `class="crumb crumb-home" href="/">Overview</a>`,
		`aria-current="page">Apply history`, `class="nav-item is-active" href="/history"`, `name="q"`)
}

func TestStatusIconsFollowTheContainerState(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(
		dockertest.Container{ID: "ok", Name: "a", State: "running", Health: "healthy"},
		dockertest.Container{ID: "sick", Name: "b", State: "running", Health: "unhealthy"},
		dockertest.Container{ID: "boot", Name: "c", State: "running", Health: "starting"},
		dockertest.Container{ID: "done", Name: "d", State: "exited", Status: "Exited (0) 1 hour ago"},
		dockertest.Container{ID: "crash", Name: "e", State: "exited", Status: "Exited (2) 1 hour ago"},
	)
	body := mustGet(h)
	for id, icon := range map[string]string{"ok": "si-ok", "sick": "si-err", "boot": "si-pend", "done": "si-off", "crash": "si-err"} {
		if !strings.Contains(body, icon+`" data-id="`+id+`"`) && !strings.Contains(body, icon+` is-unhealthy" data-id="`+id+`"`) {
			t.Errorf("%s should show %s", id, icon)
		}
	}
}

func TestSearchNarrowsTheDashboardServerSide(t *testing.T) {
	e := newSyncEnv(t, merge(compose("media"), compose("proxy")))
	e.deployed("media", "proxy")
	selectAll(e, "media", "proxy")
	e.api.SetContainers(
		dockertest.Container{ID: "m1", Name: "media-jellyfin-1", Image: "jellyfin/jellyfin:10", State: "running", Labels: composeLabel("media")},
		dockertest.Container{ID: "m2", Name: "media-db-1", Image: "postgres:16", State: "running", Labels: composeLabel("media")},
		dockertest.Container{ID: "p1", Name: "proxy-traefik-1", Image: "traefik:v3", State: "running", Labels: composeLabel("proxy")},
		dockertest.Container{ID: "o1", Name: "adhoc", Image: "postgres:15", State: "running"},
	)
	for q, want := range map[string]string{
		"postgres": "media-db-1,adhoc",
		"proxy":    "proxy-traefik-1", // a Stack name matches all its containers
		"JELLY":    "media-jellyfin-1",
	} {
		_, body := e.get("/?q=" + q)
		if got := strings.Join(names(body), ","); got != want {
			t.Errorf("q=%s shows %q, want %q", q, got, want)
		}
		assertContains(t, body, "Showing containers and Stacks matching", `value="`+q+`"`, "Show everything")
	}
}

func TestNavigationSectionsAreLinks(t *testing.T) {
	body := mustGet(startDashboard(t))
	assertContains(t, body, `class="nav-item nav-top" href="/containers">Containers`, `class="nav-item nav-top" href="/deployment">Deployment`)
}

func TestRobotoIsServedFromTheBinary(t *testing.T) {
	h := startDashboard(t)
	resp, err := http.Get(h.srv.URL + "/static/fonts/roboto-latin-400-normal.woff2")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "font/woff2" {
		t.Errorf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	_, css := h.get("/static/style.css")
	assertContains(t, css, "@font-face", "roboto-latin-500-normal.woff2", "Roboto Mono")
}
