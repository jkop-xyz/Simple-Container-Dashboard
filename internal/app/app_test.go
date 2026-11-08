package app_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func startDashboard(t *testing.T, api *dockertest.Server) *httptest.Server {
	t.Helper()
	a, err := app.New(app.Options{
		Config: config.Config{Runtime: config.Docker, Socket: api.Socket},
		Env:    noHostEnv,
		Runner: &runnertest.Fake{},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestDashboardListsAllContainersIncludingStopped(t *testing.T) {
	api := dockertest.New(t)
	api.SetContainers(
		dockertest.Container{ID: "a1", Name: "jellyfin", Image: "jellyfin/jellyfin:10", State: "running", Status: "Up 2 hours"},
		dockertest.Container{ID: "b2", Name: "backup", Image: "restic:0.17", State: "exited", Status: "Exited (1) 3 hours ago"},
	)
	srv := startDashboard(t, api)

	code, body := get(t, srv.URL+"/")
	if code != http.StatusOK {
		t.Fatalf("status %d, body:\n%s", code, body)
	}
	for _, want := range []string{"jellyfin", "running", "Up 2 hours", "backup", "exited", "Exited (1) 3 hours ago"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	if strings.Index(body, "backup") > strings.Index(body, "jellyfin") {
		t.Error("containers should be sorted by name")
	}
}

func TestDashboardWithNoContainers(t *testing.T) {
	api := dockertest.New(t)
	srv := startDashboard(t, api)

	_, body := get(t, srv.URL+"/")
	if !strings.Contains(body, "No containers on this host") {
		t.Errorf("expected empty-state message, got:\n%s", body)
	}
}

func TestDashboardReportsUnreachableRuntime(t *testing.T) {
	a, err := app.New(app.Options{
		Config: config.Config{Runtime: config.Docker, Socket: t.TempDir() + "/missing.sock"},
		Env:    noHostEnv,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler)
	defer srv.Close()

	code, body := get(t, srv.URL+"/")
	if code != http.StatusBadGateway {
		t.Errorf("status %d, want %d", code, http.StatusBadGateway)
	}
	if !strings.Contains(body, "reach the container runtime") {
		t.Errorf("expected runtime error message, got:\n%s", body)
	}
}

func TestDashboardServesEmbeddedAssets(t *testing.T) {
	srv := startDashboard(t, dockertest.New(t))
	for _, path := range []string{"/static/app.js", "/static/style.css"} {
		if code, _ := get(t, srv.URL+path); code != http.StatusOK {
			t.Errorf("GET %s: status %d", path, code)
		}
	}
}

func TestNewFailsWithoutAnySocket(t *testing.T) {
	_, err := app.New(app.Options{Env: noHostEnv})
	if err == nil || !strings.Contains(err.Error(), "no Docker or Podman socket found") {
		t.Fatalf("got %v", err)
	}
}
