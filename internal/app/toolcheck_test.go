package app_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/app"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner/runnertest"
)

func without(missing ...string) func(*app.Options) {
	return func(o *app.Options) {
		o.LookPath = func(f string) (string, error) {
			for _, m := range missing {
				if f == m {
					return "", errors.New("not found")
				}
			}
			return "/usr/bin/" + f, nil
		}
	}
}

func TestMissingToolsAreShown(t *testing.T) {
	h := startDashboard(t, without("git", "docker"))
	body := mustGet(h)
	assertContains(t, body, "missing tools scd needs", "<code>git</code>", "<code>docker</code>", "<code>docker compose</code>", "scd setup")
	code, raw := h.get("/api/status")
	if code != 200 || !strings.Contains(raw, `"missing":["git","docker","docker compose"]`) {
		t.Errorf("status %d: %s", code, raw)
	}
}

func TestComposePluginIsChecked(t *testing.T) {
	rec := &runnertest.Fake{}
	rec.On("docker compose version", runnertest.Response{ExitCode: 1, Output: "docker: 'compose' is not a docker command."})
	h := startDashboard(t, func(o *app.Options) { o.Runner = rec })
	assertContains(t, mustGet(h), "<code>docker compose</code>")
}

func TestPodmanHostsNeedPodmanAndSystemdTools(t *testing.T) {
	h := startDashboard(t, without("journalctl"), func(o *app.Options) { o.Config.Runtime = config.Podman })
	body := mustGet(h)
	assertContains(t, body, "<code>journalctl</code>")
	if strings.Contains(body, "<code>docker compose</code>") {
		t.Error("Podman hosts don't need docker compose")
	}
}

func TestNoBannerWhenNothingIsMissing(t *testing.T) {
	if strings.Contains(mustGet(startDashboard(t)), "missing tools") {
		t.Error("banner shown with every tool present")
	}
}

func releaseHarness(t *testing.T, current, latest string, calls *atomic.Int32) *harness {
	return startDashboard(t, func(o *app.Options) {
		o.Version = current
		o.LatestRelease = func(context.Context) (string, error) {
			calls.Add(1)
			return latest, nil
		}
	})
}

func TestNewerReleaseIsHinted(t *testing.T) {
	var calls atomic.Int32
	h := releaseHarness(t, "v1.0.0", "v1.1.0", &calls)
	mustGet(h) // the first view starts the check in the background
	waitFor(t, func() bool { return strings.Contains(mustGet(h), "new version v1.1.0 available") })
	assertContains(t, mustGet(h), `href="https://github.com/jkop-xyz/Simple-Container-Dashboard/releases/tag/v1.1.0"`)
	if n := calls.Load(); n != 1 {
		t.Errorf("%d release lookups for several page views, want 1 (cached for a day)", n)
	}
}

func TestNoHintWhenUpToDateOrADevBuild(t *testing.T) {
	var calls atomic.Int32
	for _, tc := range []struct{ current, latest string }{{"v1.1.0", "v1.1.0"}, {"v1.2.0", "v1.1.0"}, {"dev", "v9.9.9"}} {
		h := releaseHarness(t, tc.current, tc.latest, &calls)
		mustGet(h)
		mustGet(h)
		if strings.Contains(mustGet(h), "new version") {
			t.Errorf("%s vs %s: hint shown", tc.current, tc.latest)
		}
	}
}

func TestReleaseLookupFailuresAreSilent(t *testing.T) {
	h := startDashboard(t, func(o *app.Options) {
		o.Version = "v1.0.0"
		o.LatestRelease = func(context.Context) (string, error) { return "", errors.New("no network") }
	})
	mustGet(h)
	body := mustGet(h)
	if strings.Contains(body, "new version") || strings.Contains(body, "no network") {
		t.Error("a failed release lookup should not show anything")
	}
}
