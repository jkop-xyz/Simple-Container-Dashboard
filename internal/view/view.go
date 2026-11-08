// Package view assembles the dashboard's view model: containers grouped by Stack.
package view

import (
	"sort"
	"strings"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
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

// Group is one Stack and its containers.
type Group struct {
	Stack stacks.Stack
	// Selected reports whether the Stack is chosen to run on this host.
	Selected   bool
	Containers []Container
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
	index := map[string]int{}
	for _, s := range ss {
		index[s.ProjectName()] = len(d.Groups)
		d.Groups = append(d.Groups, Group{Stack: s, Selected: selected(s.Name)})
	}
	for _, c := range containers {
		project := strings.ToLower(c.Labels[ComposeProjectLabel])
		if i, ok := index[project]; ok && project != "" {
			d.Groups[i].Containers = append(d.Groups[i].Containers, c)
			continue
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
