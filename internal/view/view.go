// Package view assembles the dashboard's view model: containers grouped by Stack.
package view

import (
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
