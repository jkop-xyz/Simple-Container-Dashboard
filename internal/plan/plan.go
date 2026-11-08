// Package plan computes a Sync Plan: what Apply must do to each Stack to bring the host
// in line with one commit and the current selection. It is pure: no I/O.
package plan

import (
	"slices"
	"sort"
	"strings"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/repo"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/state"
)

// Action is what Apply will do to one Stack.
type Action string

const (
	Deploy      Action = "deploy"
	Redeploy    Action = "redeploy"
	Teardown    Action = "teardown"
	Unchanged   Action = "unchanged"
	NotSelected Action = "not selected"
	// Invalid Stacks are skipped: not deployed, and if already deployed, left as they are.
	Invalid Action = "invalid"
)

// Changes reports whether the action changes the host.
func (a Action) Changes() bool { return a == Deploy || a == Redeploy || a == Teardown }

// Entry is the planned action for one Stack.
type Entry struct {
	Stack  string
	Kind   stacks.Kind
	Action Action
	Reason string
	// Tree is the Stack's tree id at the planned commit (empty for teardowns).
	Tree string
	// Warning is a non-fatal note about the Stack.
	Warning string
	// Secrets is the fingerprint of the Stack's secrets.env at planning time.
	Secrets string
}

// Plan is a Sync Plan bound to one commit and one selection.
type Plan struct {
	// From is the clone's current commit; To is the commit Apply will move to.
	From, To string
	// Commits lists From..To, newest first.
	Commits []repo.Commit
	Entries []Entry
	// Selection identifies the selection the plan was computed for.
	Selection string
	// Blocked, when set, is why this plan can't be applied at all.
	Blocked string
}

// HasChanges reports whether applying the plan would change anything on the host.
func (p *Plan) HasChanges() bool {
	return p.From != p.To || slices.ContainsFunc(p.Entries, func(e Entry) bool { return e.Action.Changes() })
}

// Input is everything the planner looks at.
type Input struct {
	// Stacks are the Stacks discovered at the planned commit.
	Stacks []stacks.Stack
	// Trees maps Stack name to its tree id at the planned commit.
	Trees    map[string]string
	Selected []string
	Deployed map[string]state.DeployedStack
	// Secrets maps Stack name to the fingerprint of its secrets.env on the host
	// (absent or empty when there is none).
	Secrets map[string]string
}

// SelectionKey identifies a selection, so a plan can tell when it has gone stale.
func SelectionKey(selected []string) string {
	s := slices.Clone(selected)
	sort.Strings(s)
	return strings.Join(s, "\n")
}

// Entries computes the per-Stack actions. Deploys and redeploys come first, then
// teardowns, then Stacks left alone; each group alphabetical. This is the order Apply
// runs them in.
func Entries(in Input) []Entry {
	var out []Entry
	inRepo := map[string]bool{}
	for _, s := range in.Stacks {
		inRepo[s.Name] = true
		tree := in.Trees[s.Name]
		dep, deployed := in.Deployed[s.Name]
		selected := slices.Contains(in.Selected, s.Name)
		secrets := in.Secrets[s.Name]
		e := Entry{Stack: s.Name, Kind: s.Kind, Tree: tree, Warning: s.Warning, Secrets: secrets}
		switch {
		case s.Invalid() && deployed:
			e.Action, e.Reason = Invalid, s.Problem+"; the deployed Stack is left as it is"
		case s.Invalid():
			e.Action, e.Reason = Invalid, s.Problem
		case selected && !deployed:
			e.Action, e.Reason = Deploy, "selected and not deployed yet"
		case selected && dep.Failed:
			e.Action, e.Reason = Redeploy, "retry: the last Apply failed"
		case selected && dep.Tree != tree:
			e.Action, e.Reason = Redeploy, "files changed"
		case selected && dep.Secrets != secrets:
			e.Action, e.Reason = Redeploy, "secrets changed"
		case selected:
			e.Action, e.Reason = Unchanged, "no changes"
		case deployed:
			e.Action, e.Reason, e.Tree = Teardown, "deselected", ""
		default:
			e.Action, e.Reason = NotSelected, "not selected on this host"
		}
		out = append(out, e)
	}
	for name, dep := range in.Deployed {
		if !inRepo[name] {
			out = append(out, Entry{Stack: name, Kind: dep.Kind, Action: Teardown, Reason: "removed from the Deployment Repo"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i].Action), rank(out[j].Action); ri != rj {
			return ri < rj
		}
		return out[i].Stack < out[j].Stack
	})
	return out
}

func rank(a Action) int {
	switch a {
	case Deploy, Redeploy:
		return 0
	case Teardown:
		return 1
	case Invalid:
		return 2
	}
	return 3
}

// Invalidate turns a planned deploy or redeploy into an Invalid entry.
func (e *Entry) Invalidate(problem string) {
	e.Action, e.Reason = Invalid, problem
}
