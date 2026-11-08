// Package view assembles the dashboard's view model: containers grouped by Stack.
package view

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/systemd"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/usage"
)

// Container is a container with everything the dashboard shows about it.
type Container struct {
	dockerapi.Container
	dockerapi.Details
	usage.Usage
	// CrashLooping is set while the container keeps restarting.
	CrashLooping bool
}

// NeedsAttention reports whether the container is unhealthy, crash-looping, restarting,
// dead, or exited with a non-zero code.
func (c Container) NeedsAttention() bool { return c.problem() != "" }

func (c Container) problem() string {
	switch {
	case c.CrashLooping:
		return "crash-looping"
	case c.Health == "unhealthy":
		return "unhealthy"
	case c.State == "restarting":
		return "restarting"
	case c.State == "dead":
		return "dead"
	case c.State == "exited" && !strings.HasPrefix(c.Status, "Exited (0)"):
		return strings.ToLower(strings.SplitN(c.Status, ")", 2)[0] + ")")
	}
	return ""
}

// ComposeProjectLabel is the label Docker Compose (and podman compose) put on containers.
const ComposeProjectLabel = "com.docker.compose.project"

// SystemdUnitLabel is the label Podman puts on containers run by a systemd unit, which
// is how Quadlet containers are tied to their Stack.
const SystemdUnitLabel = "PODMAN_SYSTEMD_UNIT"

// Group is one Stack and its containers.
type Group struct {
	Stack stacks.Stack
	// Selected reports whether the Stack is chosen to run on this host.
	Selected   bool
	Containers []Container
	// Units are a Quadlet Stack's systemd units and their states.
	Units []systemd.UnitState
	// Deployed reports whether Apply has deployed the Stack (so it is managed here).
	Deployed bool
}

// Dashboard is the grouped view of the host.
type Dashboard struct {
	Groups []Group
	// Other holds containers that belong to no Stack in the Deployment Repo.
	Other []Container
}

// Build groups containers under the Stacks they belong to. Every Stack gets a group,
// even with no containers; containers matching no Stack go to Other.
func Build(containers []Container, ss []stacks.Stack, selected func(name string) bool) Dashboard {
	var d Dashboard
	byProject := map[string]int{}
	byService := map[string]int{}
	for _, s := range ss {
		i := len(d.Groups)
		switch s.Kind {
		case stacks.Compose:
			byProject[s.ProjectName()] = i
		case stacks.Quadlet:
			for _, svc := range s.Services() {
				byService[svc] = i
			}
		}
		d.Groups = append(d.Groups, Group{Stack: s, Selected: selected(s.Name)})
	}
	for _, c := range containers {
		if project := strings.ToLower(c.Labels[ComposeProjectLabel]); project != "" {
			if i, ok := byProject[project]; ok {
				d.Groups[i].Containers = append(d.Groups[i].Containers, c)
				continue
			}
		}
		if unit := c.Labels[SystemdUnitLabel]; unit != "" {
			if i, ok := byService[unit]; ok {
				d.Groups[i].Containers = append(d.Groups[i].Containers, c)
				continue
			}
		}
		d.Other = append(d.Other, c)
	}
	for i := range d.Groups {
		sortByName(d.Groups[i].Containers)
	}
	sortByName(d.Other)
	return d
}

func sortByName(cs []Container) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
}

// Running counts the group's running containers.
func (g Group) Running() int { return countRunning(g.Containers) }

// Summary is the dashboard's at-a-glance overview.
type Summary struct {
	Running, Total, Unhealthy, Restarting int
	Selected, Stacks, Invalid, Unmanaged  int
}

// Summary counts containers and Stacks for the overview tiles.
func (d Dashboard) Summary() Summary {
	var s Summary
	add := func(cs []Container) {
		for _, c := range cs {
			s.Total++
			if c.State == "running" {
				s.Running++
			}
			if c.Health == "unhealthy" {
				s.Unhealthy++
			}
			if c.State == "restarting" {
				s.Restarting++
			}
		}
	}
	for _, g := range d.Groups {
		add(g.Containers)
		s.Stacks++
		if g.Selected {
			s.Selected++
		}
		if g.Stack.Invalid() {
			s.Invalid++
		}
	}
	add(d.Other)
	s.Unmanaged = len(d.Other)
	return s
}

func countRunning(cs []Container) int {
	n := 0
	for _, c := range cs {
		if c.State == "running" {
			n++
		}
	}
	return n
}

// Unhealthy counts the group's containers failing their healthcheck.
func (g Group) Unhealthy() int {
	n := 0
	for _, c := range g.Containers {
		if c.Health == "unhealthy" {
			n++
		}
	}
	return n
}

// CrashLooping counts the group's crash-looping containers.
func (g Group) CrashLooping() int {
	n := 0
	for _, c := range g.Containers {
		if c.CrashLooping {
			n++
		}
	}
	return n
}

// Restarted reports whether any of the group's containers has restarted.
func (g Group) Restarted() bool {
	for _, c := range g.Containers {
		if c.RestartCount > 0 {
			return true
		}
	}
	return false
}

// Active reports whether the Stack belongs in the main grid: it is selected or deployed
// here, or it still has containers or installed units (e.g. it awaits teardown).
func (g Group) Active() bool {
	if g.Selected || g.Deployed || len(g.Containers) > 0 {
		return true
	}
	for _, u := range g.Units { // installed Quadlet units, e.g. one that failed before starting
		if u.LoadState == "loaded" {
			return true
		}
	}
	return false
}

// Problem is one container that needs a look, and why.
type Problem struct {
	Container
	Stack  string
	Reason string
	// Severity is "bad" or "warn".
	Severity string
}

// Attention lists containers that need a look: unhealthy, restarting, crashed (exited
// non-zero) or restarted at least once. Worst first.
func (d Dashboard) Attention() []Problem {
	var out []Problem
	check := func(stack string, cs []Container) {
		for _, c := range cs {
			reason := c.problem()
			if reason == "" {
				continue
			}
			p := Problem{Container: c, Stack: stack, Reason: reason, Severity: "bad"}
			if c.RestartCount > 0 {
				p.Reason += fmt.Sprintf(" · restarted %d×", c.RestartCount)
			}
			out = append(out, p)
		}
	}
	for _, g := range d.Groups {
		check(g.Stack.Name, g.Containers)
	}
	check("", d.Other)
	return out
}

// Inactive lists Stacks that neither run nor are selected here.
func (d Dashboard) Inactive() []Group {
	var out []Group
	for _, g := range d.Groups {
		if !g.Active() {
			out = append(out, g)
		}
	}
	return out
}

// ActiveGroups lists the Stacks shown as full cards.
func (d Dashboard) ActiveGroups() []Group {
	var out []Group
	for _, g := range d.Groups {
		if g.Active() {
			out = append(out, g)
		}
	}
	return out
}

// Filters the dashboard can narrow to.
var Filters = map[string]string{
	"attention": "containers that need attention",
	"stopped":   "containers that aren't running",
	"selected":  "Stacks selected on this host",
	"unmanaged": "unmanaged containers",
}

// FilterTitles are the filters' short names, as the navigation shows them.
var FilterTitles = map[string]string{
	"attention": "Needs attention",
	"stopped":   "Not running",
	"selected":  "Selected Stacks",
	"unmanaged": "Unmanaged",
}

// Filter narrows the dashboard to one of Filters. Unknown names leave it unchanged.
func (d Dashboard) Filter(name string) Dashboard {
	keepRows := func(cs []Container, ok func(Container) bool) []Container {
		var out []Container
		for _, c := range cs {
			if ok(c) {
				out = append(out, c)
			}
		}
		return out
	}
	rows := func(ok func(Container) bool) Dashboard {
		var f Dashboard
		for _, g := range d.Groups {
			g.Containers = keepRows(g.Containers, ok)
			if len(g.Containers) > 0 {
				f.Groups = append(f.Groups, g)
			}
		}
		f.Other = keepRows(d.Other, ok)
		return f
	}
	switch name {
	case "attention":
		return rows(Container.NeedsAttention)
	case "stopped":
		return rows(func(c Container) bool { return c.State != "running" })
	case "selected":
		var f Dashboard
		for _, g := range d.Groups {
			if g.Selected {
				f.Groups = append(f.Groups, g)
			}
		}
		return f
	case "unmanaged":
		return Dashboard{Other: d.Other}
	}
	return d
}

// Search narrows the dashboard to containers whose name or image contains q, and to
// every container of Stacks whose name contains q (case-insensitive).
func (d Dashboard) Search(q string) Dashboard {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return d
	}
	hit := func(c Container) bool {
		return strings.Contains(strings.ToLower(c.Name), q) || strings.Contains(strings.ToLower(c.Image), q)
	}
	var f Dashboard
	for _, g := range d.Groups {
		if strings.Contains(strings.ToLower(g.Stack.Name), q) {
			f.Groups = append(f.Groups, g)
			continue
		}
		var cs []Container
		for _, c := range g.Containers {
			if hit(c) {
				cs = append(cs, c)
			}
		}
		if len(cs) > 0 {
			g.Containers = cs
			f.Groups = append(f.Groups, g)
		}
	}
	for _, c := range d.Other {
		if hit(c) {
			f.Other = append(f.Other, c)
		}
	}
	return f
}
