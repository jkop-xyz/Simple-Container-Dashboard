package app_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
)

// noHostEnv makes sure tests never detect real sockets on the developer's machine.
var noHostEnv = &dockerapi.Env{
	Getenv: func(string) string { return "" },
	Exists: func(string) bool { return false },
}

// harness is one in-process dashboard plus its fakes.
type harness struct {
	t   *testing.T
	api *dockertest.Server
	app *app.App
	srv *httptest.Server
}

// startDashboard starts the real dashboard against a fake runtime API. Pass a
// customize func to adjust the options (e.g. set a Deployment Repo).
func startDashboard(t *testing.T, customize ...func(*app.Options)) *harness {
	t.Helper()
	api := dockertest.New(t)
	opts := app.Options{
		Config:   config.Config{Runtime: config.Docker, Socket: api.Socket},
		Env:      noHostEnv,
		Runner:   &runnertest.Fake{},
		Hostname: "testhost",
		DataDir:  t.TempDir(),
		// Every tool is "installed", and the release lookup never reaches the network.
		LookPath:      func(f string) (string, error) { return "/usr/bin/" + f, nil },
		LatestRelease: func(context.Context) (string, error) { return "", errors.New("offline in tests") },
		// Post-Apply health checks settle fast in tests.
		WatchFor: 60 * time.Millisecond, WatchEvery: 20 * time.Millisecond,
	}
	for _, c := range customize {
		c(&opts)
	}
	a, err := app.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Wait) // let background health checks finish before temp dirs go
	if err := a.Start(context.Background()); err != nil {
		t.Logf("Start: %v", err)
	}
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	return &harness{t: t, api: api, app: a, srv: srv}
}

func (h *harness) get(path string) (int, string) {
	h.t.Helper()
	resp, err := http.Get(h.srv.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func assertContains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("page missing %q", w)
		}
	}
}

func TestDashboardListsAllContainersIncludingStopped(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(
		dockertest.Container{ID: "a1", Name: "jellyfin", Image: "jellyfin/jellyfin:10", State: "running", Status: "Up 2 hours"},
		dockertest.Container{ID: "b2", Name: "backup", Image: "restic:0.17", State: "exited", Status: "Exited (1) 3 hours ago"},
	)

	code, body := h.get("/containers")
	if code != http.StatusOK {
		t.Fatalf("status %d, body:\n%s", code, body)
	}
	assertContains(t, body, "jellyfin", "running", "Up 2 hours", "backup", "exited", "Exited (1) 3 hours ago")
	if strings.Index(body, "backup") > strings.Index(body, "jellyfin") {
		t.Error("containers should be sorted by name")
	}
}

func TestDashboardWithNoContainers(t *testing.T) {
	_, body := startDashboard(t).get("/containers")
	assertContains(t, body, "No containers on this host")
}

func TestDashboardReportsUnreachableRuntime(t *testing.T) {
	h := startDashboard(t, func(o *app.Options) { o.Config.Socket = t.TempDir() + "/missing.sock" })
	code, body := h.get("/")
	if code != http.StatusBadGateway {
		t.Errorf("status %d, want %d", code, http.StatusBadGateway)
	}
	assertContains(t, body, "reach the container runtime")
}

func TestDashboardServesEmbeddedAssets(t *testing.T) {
	h := startDashboard(t)
	for _, path := range []string{"/static/app.js", "/static/style.css"} {
		if code, _ := h.get(path); code != http.StatusOK {
			t.Errorf("GET %s: status %d", path, code)
		}
	}
}

func TestHeaderShowsHostname(t *testing.T) {
	_, body := startDashboard(t).get("/")
	assertContains(t, body, `class="hostname">testhost<`)
}

func TestNewFailsWithoutAnySocket(t *testing.T) {
	_, err := app.New(app.Options{Env: noHostEnv})
	if err == nil || !strings.Contains(err.Error(), "no Docker or Podman socket found") {
		t.Fatalf("got %v", err)
	}
}
