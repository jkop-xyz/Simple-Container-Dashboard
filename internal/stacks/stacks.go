// Package stacks discovers and structurally validates Stacks in a Deployment Repo tree.
package stacks

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
)

// Kind is how a Stack is defined.
type Kind string

const (
	Compose Kind = "compose"
	Quadlet Kind = "quadlet"
	// Unknown is the kind of an Invalid Stack whose definition can't be determined.
	Unknown Kind = "unknown"
)

// ComposeFile is the file that makes a directory a Compose Stack.
const ComposeFile = "compose.yaml"

// SecretsFile is the host-local, git-ignored Secrets File in a Stack's directory.
const SecretsFile = "secrets.env"

// QuadletExtensions are the Podman Quadlet unit file types.
var QuadletExtensions = []string{".container", ".pod", ".network", ".volume", ".kube", ".image", ".build"}

// Stack is one deployable unit: a top-level directory of the Deployment Repo.
type Stack struct {
	// Name is the directory name.
	Name string
	Kind Kind
	// Files are the Stack's file paths relative to its directory.
	Files []string
	// Problem is why the Stack is Invalid, or empty for a valid Stack.
	Problem string
	// Warning is a non-fatal note (e.g. the name was lowercased).
	Warning string
}

// Invalid reports whether the Stack failed validation.
func (s Stack) Invalid() bool { return s.Problem != "" }

// ProjectName is the compose project name the runtime uses for this Stack.
func (s Stack) ProjectName() string { return strings.ToLower(s.Name) }

// QuadletUnits lists the Stack's Quadlet unit files.
func (s Stack) QuadletUnits() []string {
	var out []string
	for _, f := range s.Files {
		if isQuadlet(f) {
			out = append(out, f)
		}
	}
	return out
}

func isQuadlet(f string) bool {
	if strings.Contains(f, "/") {
		return false
	}
	for _, ext := range QuadletExtensions {
		if strings.HasSuffix(f, ext) {
			return true
		}
	}
	return false
}

// projectName is what compose accepts as a project name.
var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Discover finds the Stacks in a list of repo file paths (as from `git ls-tree`) and
// checks their structure for the host's runtime. Every top-level directory that isn't
// hidden is a Stack, except one that holds nothing but a Secrets File; files at the repo
// root never are.
func Discover(files []string, runtime config.Runtime) []Stack {
	byDir := map[string]*Stack{}
	var order []string
	for _, f := range files {
		dir, rest, ok := strings.Cut(f, "/")
		if !ok || strings.HasPrefix(dir, ".") {
			continue
		}
		s := byDir[dir]
		if s == nil {
			s = &Stack{Name: dir}
			byDir[dir] = s
			order = append(order, dir)
		}
		s.Files = append(s.Files, rest)
	}

	var out []Stack
	for _, dir := range order {
		s := byDir[dir]
		if len(s.Files) == 1 && s.Files[0] == SecretsFile {
			continue
		}
		classify(s, runtime)
		out = append(out, *s)
	}
	checkNames(out)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func classify(s *Stack, runtime config.Runtime) {
	hasCompose := false
	hasQuadlet := false
	for _, f := range s.Files {
		hasCompose = hasCompose || f == ComposeFile
		hasQuadlet = hasQuadlet || isQuadlet(f)
	}
	switch {
	case hasCompose && hasQuadlet:
		s.Kind, s.Problem = Unknown, "has both a compose.yaml and Quadlet files; a Stack must be one or the other"
	case hasCompose:
		s.Kind = Compose
	case hasQuadlet:
		s.Kind = Quadlet
	default:
		s.Kind, s.Problem = Unknown, "has no compose.yaml and no Quadlet files"
	}
	if s.Problem != "" {
		return
	}
	for _, f := range s.Files {
		if path.Base(f) == SecretsFile {
			s.Problem = "has a secrets.env committed to git (" + s.Path(f) + "); remove it from the repo and keep it only on the host"
			return
		}
	}
	switch {
	case s.Kind == Compose && runtime == config.Podman:
		s.Problem = "is a Compose Stack, but this is a Podman host (Podman hosts run Quadlet Stacks)"
	case s.Kind == Quadlet && runtime == config.Docker:
		s.Problem = "is a Quadlet Stack, but this is a Docker host (Docker hosts run Compose Stacks)"
	}
}

// checkNames lowercases names into project names, and invalidates Stacks whose names
// can't be project names or collide after lowercasing.
func checkNames(ss []Stack) {
	byProject := map[string][]int{}
	for i := range ss {
		s := &ss[i]
		p := s.ProjectName()
		byProject[p] = append(byProject[p], i)
		if s.Problem != "" {
			continue
		}
		if !projectName.MatchString(p) {
			s.Problem = fmt.Sprintf("name %q can't be used as a project name (use letters, digits, - and _)", s.Name)
		} else if p != s.Name {
			s.Warning = fmt.Sprintf("runs as project %q (names are lowercased)", p)
		}
	}
	for p, idx := range byProject {
		if len(idx) < 2 {
			continue
		}
		var names []string
		for _, i := range idx {
			names = append(names, ss[i].Name)
		}
		sort.Strings(names)
		for _, i := range idx {
			ss[i].Problem = fmt.Sprintf("collides with %s: all become project %q", strings.Join(others(names, ss[i].Name), ", "), p)
		}
	}
}

func others(names []string, self string) []string {
	var out []string
	for _, n := range names {
		if n != self {
			out = append(out, n)
		}
	}
	return out
}

// Path joins a Stack-relative file to the Stack's directory.
func (s Stack) Path(file string) string { return path.Join(s.Name, file) }

// TracksSecrets returns the tracked paths named secrets.env in a repo file list.
func TracksSecrets(files []string) []string {
	var out []string
	for _, f := range files {
		if path.Base(f) == SecretsFile {
			out = append(out, f)
		}
	}
	return out
}

// IgnoresSecrets reports whether a root .gitignore excludes secrets.env in every Stack
// directory.
func IgnoresSecrets(gitignore string) bool {
	for _, line := range strings.Split(gitignore, "\n") {
		switch strings.TrimSpace(line) {
		case SecretsFile, "**/" + SecretsFile, "*/" + SecretsFile, "/*/" + SecretsFile:
			return true
		}
	}
	return false
}
