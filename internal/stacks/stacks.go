// Package stacks discovers Stacks in a Deployment Repo tree.
package stacks

import (
	"sort"
	"strings"
)

// Kind is how a Stack is defined.
type Kind string

const (
	Compose Kind = "compose"
)

// ComposeFile is the file that makes a directory a Compose Stack.
const ComposeFile = "compose.yaml"

// Stack is one deployable unit: a top-level directory of the Deployment Repo.
type Stack struct {
	// Name is the directory name.
	Name string
	Kind Kind
}

// ProjectName is the compose project name the runtime uses for this Stack.
func (s Stack) ProjectName() string { return strings.ToLower(s.Name) }

// Discover finds the Stacks in a list of repo file paths (as from `git ls-tree`).
// Files at the repo root and hidden directories never form Stacks.
func Discover(files []string) []Stack {
	byDir := map[string]Stack{}
	for _, f := range files {
		dir, rest, ok := strings.Cut(f, "/")
		if !ok || strings.HasPrefix(dir, ".") {
			continue
		}
		if rest == ComposeFile {
			byDir[dir] = Stack{Name: dir, Kind: Compose}
		}
	}
	out := make([]Stack, 0, len(byDir))
	for _, s := range byDir {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
