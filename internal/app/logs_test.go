package app_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
)

// logEvents opens a log stream and returns a channel of its events ("data" lines, or
// "event:<name>" markers).
func (h *harness) logEvents(ctx context.Context, id string) <-chan string {
	h.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.srv.URL+"/containers/"+id+"/logs/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("log stream status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		h.t.Errorf("Content-Type %q", ct)
	}
	ch := make(chan string, 1024)
	go func() {
		defer resp.Body.Close()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if ev, ok := strings.CutPrefix(line, "event: "); ok {
				ch <- "event:" + ev
			} else if data, ok := strings.CutPrefix(line, "data: "); ok {
				ch <- data
			}
		}
	}()
	return ch
}

// collect reads events until the "done" event, or fails after a timeout.
func collect(t *testing.T, ch <-chan string) []string {
	t.Helper()
	var out []string
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok || ev == "event:done" {
				return out
			}
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("stream didn't finish; got %q", out)
		}
	}
}

func next(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
		return ""
	}
}

func lines(n int) []string {
	var out []string
	for i := 1; i <= n; i++ {
		out = append(out, fmt.Sprintf("line %d", i))
	}
	return out
}

func TestLogsStartWithTheLast500Lines(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "c", Name: "web", State: "exited", Logs: lines(800)})

	got := collect(t, h.logEvents(context.Background(), "c"))
	if len(got) != 500 || got[0] != "line 301" || got[499] != "line 800" {
		t.Fatalf("got %d lines, first %q last %q", len(got), first(got), last(got))
	}
}

func TestLogsFollowARunningContainer(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "c", Name: "web", State: "running", Logs: []string{"booting"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := h.logEvents(ctx, "c")
	if got := next(t, ch); got != "booting" {
		t.Fatalf("first line %q", got)
	}
	h.api.AppendLogs("c", "GET /health 200", "GET / 200")
	if a, b := next(t, ch), next(t, ch); a != "GET /health 200" || b != "GET / 200" {
		t.Errorf("followed %q, %q", a, b)
	}
	h.api.EndLogs("c")
	if got := next(t, ch); got != "event:done" {
		t.Errorf("after the container stopped got %q, want done", got)
	}
}

func TestLogsOfStoppedContainersDontFollow(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "c", Name: "job", State: "exited", Logs: []string{"done."}})
	// No EndLogs: if scd asked to follow, this would hang until the timeout.
	if got := collect(t, h.logEvents(context.Background(), "c")); len(got) != 1 || got[0] != "done." {
		t.Errorf("got %q", got)
	}
}

func TestLogsOfTTYContainersAreNotDemultiplexed(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "c", Name: "shell", State: "exited", Tty: true,
		Logs: []string{"$ ls", "bin etc"}})
	if got := collect(t, h.logEvents(context.Background(), "c")); strings.Join(got, "|") != "$ ls|bin etc" {
		t.Errorf("got %q", got)
	}
}

func TestLogsPage(t *testing.T) {
	h := startDashboard(t)
	h.api.SetContainers(dockertest.Container{ID: "abc", Name: "jellyfin", State: "running"})

	assertContains(t, row(mustGet(h), "abc"), `href="/containers/abc/logs"`)
	code, page := h.get("/containers/abc/logs")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	assertContains(t, page, "jellyfin", `data-stream="/containers/abc/logs/stream"`, `href="/"`, "back to the dashboard",
		"last 500 lines, then live", `log-pause"`)

	if code, _ := h.get("/containers/nope/logs"); code != http.StatusNotFound {
		t.Errorf("unknown container: status %d", code)
	}
	if code, _ := h.get("/containers/nope/logs/stream"); code != http.StatusNotFound {
		t.Errorf("unknown container stream: status %d", code)
	}
}

func TestLogViewerScriptPausesWithoutLosingLines(t *testing.T) {
	_, js := startDashboard(t).get("/static/app.js")
	assertContains(t, js, "EventSource(logView.dataset.stream)", "held.push", "append(held)", `addEventListener("done"`)
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

func last(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}
