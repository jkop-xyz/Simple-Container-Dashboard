package app_test

import (
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi/dockertest"
)

func TestProblemsAreListedUnderNeedsAttention(t *testing.T) {
	e := newSyncEnv(t, merge(compose("ha"), compose("web")))
	e.deployed("ha", "web")
	selectAll(e, "ha", "web")
	e.api.SetContainers(
		dockertest.Container{ID: "h1", Name: "ha-core-1", State: "running", Health: "unhealthy", RestartCount: 3, Labels: composeLabel("ha")},
		dockertest.Container{ID: "w1", Name: "web-1", State: "running", Health: "healthy", Labels: composeLabel("web")},
		dockertest.Container{ID: "x1", Name: "job", State: "exited", Status: "Exited (1) 2 hours ago"},
		dockertest.Container{ID: "x0", Name: "done", State: "exited", Status: "Exited (0) 2 hours ago"},
	)
	body := mustGet(e.harness)

	_, att, _ := strings.Cut(body, `id="attention"`)
	att, _, _ = strings.Cut(att, "</section>")
	assertContains(t, att, `href="#c-h1"`, "ha-core-1", "in ha", "unhealthy · restarted 3×", `href="#c-x1"`, "exited (1)", "unmanaged")
	for _, healthy := range []string{"web-1", ">done<"} {
		if strings.Contains(att, healthy) {
			t.Errorf("%s doesn't need attention", healthy)
		}
	}
	assertContains(t, body, `href="/containers?filter=attention"`, `data-count="attention">2<`)
	assertContains(t, splitSections(body)["ha"], "badge-bad", "1 unhealthy · 1/1 running")
	assertContains(t, splitSections(body)["web"], "badge-ok", "1/1 running")
	assertContains(t, body, `is-unhealthy" data-id="h1"`)
}

func TestPendingChangesAreLabelledOnTheStack(t *testing.T) {
	e := newSyncEnv(t, merge(compose("leaving"), compose("arriving")))
	e.deployed("leaving")
	e.setSelected("arriving", true)
	e.api.SetContainers(dockertest.Container{ID: "l1", Name: "leaving-1", State: "running", Labels: composeLabel("leaving")})
	sections := splitSections(mustGet(e.harness))
	assertContains(t, sections["leaving"], "torn down at next Apply")
	assertContains(t, sections["arriving"], "deploys at next Apply")
}
