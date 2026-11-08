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
			p := Problem{Container: c, Stack: stack}
			switch {
			case c.Health == "unhealthy":
				p.Reason, p.Severity = "unhealthy", "bad"
			case c.State == "restarting":
				p.Reason, p.Severity = "restarting", "bad"
			case c.State == "exited" && !strings.HasPrefix(c.Status, "Exited (0)"):
				p.Reason, p.Severity = strings.ToLower(strings.SplitN(c.Status, ")", 2)[0]+")"), "bad"
			case c.State == "dead":
				p.Reason, p.Severity = "dead", "bad"
			default:
				continue
			}
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
