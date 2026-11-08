package app_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
)

const mib = 1 << 20

type statusJSON struct {
	Containers []struct {
		ID, Name, State, Status, Health, CPU, Memory string
		Restarts                                     int
	} `json:"containers"`
	Error string `json:"error"`
}

func (h *harness) status() (int, statusJSON) {
	h.t.Helper()
	resp, err := http.Get(h.srv.URL + "/api/status")
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		h.t.Errorf("Content-Type %q", ct)
	}
	var s statusJSON
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		h.t.Fatal(err)
	}
	return resp.StatusCode, s
}

func row(body, id string) string {
	_, rest, _ := strings.Cut(body, `data-id="`+id+`"`)
	r, _, _ := strings.Cut(rest, "</tr>")
	return r
}

func TestHealthAndRestartsAreShown(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(
		dockertest.Container{ID: "ok", Name: "web", State: "running", Health: "healthy", RestartCount: 0},
		dockertest.Container{ID: "sick", Name: "db", State: "running", Health: "unhealthy", RestartCount: 7},
		dockertest.Container{ID: "plain", Name: "cron", State: "running"},
	)
	body := mustGet(h)
	assertContains(t, row(body, "ok"), "health-healthy", ">healthy<", `class="restarts">0<`)
	assertContains(t, row(body, "sick"), "health-unhealthy", ">unhealthy<", `class="restarts">7<`)
	assertContains(t, row(body, "plain"), ">—<")
}

func TestCPUAndMemoryFromRuntimeSamples(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "c1", Name: "web", State: "running", Stats: dockertest.Stats{
		CPUTotal: 2e9, SystemCPU: 20e9, PreCPUTotal: 1e9, PreSystemCPU: 10e9, OnlineCPUs: 2,
		MemoryUsage: 600 * mib, InactiveFile: 88 * mib,
	}})

	// First reading: the runtime's own previous sample gives (1/10) × 2 CPUs = 20 %.
	_, s := h.status()
	if c := s.Containers[0]; c.CPU != "20.0%" || c.Memory != "512 MiB" {
		t.Errorf("first reading: cpu %q mem %q", c.CPU, c.Memory)
	}

	// Next reading: scd's own previous sample gives (0.5/10) × 2 = 10 %.
	h.api.SetStats("c1", dockertest.Stats{CPUTotal: 2.5e9, SystemCPU: 30e9, OnlineCPUs: 2, MemoryUsage: 3 << 30})
	_, s = h.status()
	if c := s.Containers[0]; c.CPU != "10.0%" || c.Memory != "3.0 GiB" {
		t.Errorf("second reading: cpu %q mem %q", c.CPU, c.Memory)
	}
	assertContains(t, row(mustGet(h), "c1"), `class="cpu num">`, `>3.0 GiB<`)
}

func TestCPUIsUnknownUntilThereAreTwoReadings(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "c1", Name: "web", State: "running",
		Stats: dockertest.Stats{CPUTotal: 5e9, SystemCPU: 50e9, OnlineCPUs: 4, MemoryUsage: 10 * mib}})
	if _, s := h.status(); s.Containers[0].CPU != "—" {
		t.Errorf("cpu %q with a single reading and no runtime pre-sample", s.Containers[0].CPU)
	}
}

func TestStoppedContainersShowNoUsage(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "x", Name: "job", State: "exited", Status: "Exited (0) 1 hour ago",
		Stats: dockertest.Stats{MemoryUsage: 99 * mib}})
	_, s := h.status()
	c := s.Containers[0]
	if c.CPU != "—" || c.Memory != "—" || c.State != "exited" || c.Status != "Exited (0) 1 hour ago" {
		t.Errorf("got %+v", c)
	}
}

func TestStatusEndpointShape(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "a", Name: "web", State: "running", Status: "Up 1 minute",
		Health: "starting", RestartCount: 2})
	code, s := h.status()
	if code != http.StatusOK || len(s.Containers) != 1 {
		t.Fatalf("status %d, %+v", code, s)
	}
	c := s.Containers[0]
	if c.ID != "a" || c.Name != "web" || c.State != "running" || c.Status != "Up 1 minute" || c.Health != "starting" || c.Restarts != 2 {
		t.Errorf("got %+v", c)
	}
}

func TestStatusEndpointReportsUnreachableRuntime(t *testing.T) {
	h := startDashboard(t, func(o *app.Options) { o.Config.Socket = t.TempDir() + "/missing.sock" })
	code, s := h.status()
	if code != http.StatusBadGateway || s.Error == "" {
		t.Errorf("status %d, %+v", code, s)
	}
}

func TestDashboardScriptRefreshesAndPausesWhenHidden(t *testing.T) {
	_, js := startDashboard(t).get("/static/app.js")
	assertContains(t, js, "/api/status", "REFRESH_MS = 5000", "document.hidden", "visibilitychange")
}
