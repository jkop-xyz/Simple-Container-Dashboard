package demo_test

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/demo"
)

func get(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %d\n%s", path, resp.StatusCode, b)
	}
	return string(b)
}

func post(t *testing.T, srv *httptest.Server, path string) string {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func contains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("page missing %q", w)
		}
	}
}

func follow(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	resp, err := http.Get(srv.URL + "/apply/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if sc.Text() == "event: done" {
			break
		}
		out.WriteString(strings.TrimPrefix(sc.Text(), "data: ") + "\n")
	}
	return out.String()
}

func TestDemoNeedsNothingAndExercisesTheWholeUI(t *testing.T) {
	t.Setenv("PATH", "") // no docker, podman or git: the demo must not need them
	d, err := demo.New("dev")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	page := get(t, srv, "/") + get(t, srv, "/containers") + get(t, srv, "/deployment")
	contains(t, page, "Demo mode", "Simulate a push", "demo-host", "demo/homelab",
		`data-stack="jellyfin"`, "jellyfin-server-1", "homeassistant-core-1", "unhealthy",
		"Other", "scratch-shell", "adhoc-redis", "6379/tcp (local)", ">8096<svg", "last Apply")
	if strings.Contains(page, "missing tools") {
		t.Error("demo shows missing tools")
	}

	page = post(t, srv, "/sync")
	contains(t, page, "Sync Plan", "bump jellyfin to 10.10.3",
		`data-stack="jellyfin" data-outcome="redeploy"`,
		`data-stack="nextcloud" data-outcome="teardown"`,
		`data-stack="uptime" data-outcome="not selected"`,
		`data-stack="notes" data-outcome="invalid"`,
		"1 file changed", "<code>compose.yaml</code>", `class="add">+1<`, `class="del">−1<`)

	post(t, srv, "/apply")
	stream := follow(t, srv)
	contains(t, stream, "==> redeploy jellyfin", "Container jellyfin-server-1  Started", "==> teardown nextcloud", "Apply finished")
	page = get(t, srv, "/") + get(t, srv, "/containers")
	contains(t, page, "Apply finished successfully", "jellyfin/jellyfin:10.10.3")
	if strings.Contains(page, `class="name">nextcloud-app-1<`) {
		t.Error("nextcloud's containers should be gone after the teardown")
	}

	post(t, srv, "/demo/push")
	contains(t, post(t, srv, "/sync"), "bump traefik to v3.2", `data-stack="traefik" data-outcome="redeploy"`)
}

func TestDemoLogsStream(t *testing.T) {
	d, err := demo.New("dev")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	page := get(t, srv, "/containers")
	_, rest, _ := strings.Cut(page, `href="/containers/`)
	id, _, _ := strings.Cut(rest, "/logs")
	logs := get(t, srv, "/containers/"+id+"/logs")
	contains(t, logs, "container-name", "Pause", `class="crumb crumb-home" href="/">Overview</a>`)
}
