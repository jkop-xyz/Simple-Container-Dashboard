package app

import (
	"context"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/engine"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/view"
)

// runtimeProbe samples containers for the post-Apply health check, grouping them under
// Stacks the same way the dashboard does (compose project or systemd unit label).
type runtimeProbe struct{ client *dockerapi.Client }

func (p runtimeProbe) Sample(ctx context.Context, ss []stacks.Stack) (map[string][]engine.ContainerSample, error) {
	cs, err := p.client.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	vcs := make([]view.Container, len(cs))
	for i, c := range cs {
		vcs[i].Container = c
		if d, err := p.client.Inspect(ctx, c.ID); err == nil {
			vcs[i].Details = d
		}
	}
	out := map[string][]engine.ContainerSample{}
	for _, g := range view.Build(vcs, ss, func(string) bool { return true }).Groups {
		for _, c := range g.Containers {
			out[g.Stack.Name] = append(out[g.Stack.Name], engine.ContainerSample{
				ID: c.ID, State: c.State, Health: c.Health, Restarts: c.RestartCount,
			})
		}
	}
	return out, nil
}
