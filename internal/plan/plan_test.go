package plan_test

import (
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/plan"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

func compose(names ...string) []stacks.Stack {
	var out []stacks.Stack
	for _, n := range names {
		out = append(out, stacks.Stack{Name: n, Kind: stacks.Compose})
	}
	return out
}

func TestEntries(t *testing.T) {
	tests := []struct {
		name     string
		in       plan.Input
		want     map[string]plan.Action
		wantWhy  map[string]string
		wantList []string
	}{
		{
			name: "selected and not deployed is a deploy",
			in:   plan.Input{Stacks: compose("a"), Trees: map[string]string{"a": "t1"}, Selected: []string{"a"}},
			want: map[string]plan.Action{"a": plan.Deploy},
		},
		{
			name: "same tree is unchanged, different tree is a redeploy",
			in: plan.Input{
				Stacks: compose("same", "diff"), Trees: map[string]string{"same": "t1", "diff": "t2"},
				Selected: []string{"same", "diff"},
				Deployed: map[string]state.DeployedStack{"same": {Tree: "t1"}, "diff": {Tree: "old"}},
			},
			want: map[string]plan.Action{"same": plan.Unchanged, "diff": plan.Redeploy},
		},
		{
			name: "a failed Stack is retried even when unchanged",
			in: plan.Input{
				Stacks: compose("a"), Trees: map[string]string{"a": "t1"}, Selected: []string{"a"},
				Deployed: map[string]state.DeployedStack{"a": {Tree: "t1", Failed: true}},
			},
			want:    map[string]plan.Action{"a": plan.Redeploy},
			wantWhy: map[string]string{"a": "retry: the last Apply failed"},
		},
		{
			name: "deselected while deployed is a teardown; never deployed is not selected",
			in: plan.Input{
				Stacks: compose("was", "never"), Trees: map[string]string{"was": "t", "never": "t"},
				Deployed: map[string]state.DeployedStack{"was": {Tree: "t"}},
			},
			want: map[string]plan.Action{"was": plan.Teardown, "never": plan.NotSelected},
		},
		{
			name: "deployed but gone from the repo is a teardown, selected or not",
			in: plan.Input{
				Selected: []string{"gone"},
				Deployed: map[string]state.DeployedStack{"gone": {Kind: stacks.Compose, Tree: "t"}},
			},
			want:    map[string]plan.Action{"gone": plan.Teardown},
			wantWhy: map[string]string{"gone": "removed from the Deployment Repo"},
		},
		{
			name: "order: deploys and redeploys, then teardowns, then the rest; alphabetical within",
			in: plan.Input{
				Stacks:   compose("d-unchanged", "c-redeploy", "b-deploy", "z-not"),
				Trees:    map[string]string{"d-unchanged": "t", "c-redeploy": "new", "b-deploy": "t", "z-not": "t"},
				Selected: []string{"d-unchanged", "c-redeploy", "b-deploy"},
				Deployed: map[string]state.DeployedStack{
					"d-unchanged": {Tree: "t"}, "c-redeploy": {Tree: "old"}, "a-gone": {Tree: "t"},
				},
			},
			wantList: []string{"b-deploy", "c-redeploy", "a-gone", "d-unchanged", "z-not"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := plan.Entries(tt.in)
			byName := map[string]plan.Entry{}
			var order []string
			for _, e := range got {
				byName[e.Stack] = e
				order = append(order, e.Stack)
			}
			for stack, action := range tt.want {
				if byName[stack].Action != action {
					t.Errorf("%s: %q, want %q", stack, byName[stack].Action, action)
				}
			}
			for stack, why := range tt.wantWhy {
				if byName[stack].Reason != why {
					t.Errorf("%s reason: %q, want %q", stack, byName[stack].Reason, why)
				}
			}
			if tt.wantList != nil && len(order) == len(tt.wantList) {
				for i := range order {
					if order[i] != tt.wantList[i] {
						t.Fatalf("order %v, want %v", order, tt.wantList)
					}
				}
			} else if tt.wantList != nil {
				t.Fatalf("order %v, want %v", order, tt.wantList)
			}
		})
	}
}

func TestSelectionKeyIgnoresOrder(t *testing.T) {
	if plan.SelectionKey([]string{"b", "a"}) != plan.SelectionKey([]string{"a", "b"}) {
		t.Error("selection key depends on order")
	}
	if plan.SelectionKey([]string{"a"}) == plan.SelectionKey([]string{"a", "b"}) {
		t.Error("different selections share a key")
	}
}
