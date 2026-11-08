package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
)

// MinPodman is the oldest Podman whose Quadlet reads per-Stack subdirectories and
// .container.d drop-ins, both of which the Quadlet deployer relies on.
var MinPodman = [2]int{4, 8}

// DropInName is the drop-in scd generates to hand a Stack's secrets.env to its containers.
const DropInName = "scd-secrets.conf"

// GeneratorCandidates are where distributions install Podman's quadlet generator.
var GeneratorCandidates = []string{
	"/usr/libexec/podman/quadlet",
	"/usr/lib/podman/quadlet",
	"/usr/lib/systemd/system-generators/podman-system-generator",
}

// FindGenerator returns the first quadlet generator that exists, or "".
func FindGenerator() string {
	for _, p := range GeneratorCandidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// Files lists a Stack's tracked files (relative to its directory) in the clone.
type Files func(ctx context.Context, stack string) ([]string, error)

// Quadlet deploys Quadlet Stacks for the rootless user: each Stack's files go into its
// own subdirectory of the user's Quadlet directory, and its units are run by
// `systemctl --user`. Only that subdirectory is ever written; volumes are never removed.
type Quadlet struct {
	Runner runner.Runner
	// RepoPath is the Deployment Repo clone; QuadletDir is the user's Quadlet directory
	// (~/.config/containers/systemd).
	RepoPath, QuadletDir string
	// Generator is the quadlet generator binary, used for validation.
	Generator string
	// TrackedFiles lists a Stack's files in the clone after the fast-forward.
	TrackedFiles Files

	versionOnce sync.Once
	versionErr  error
}

func (q *Quadlet) stackDir(s stacks.Stack) string { return filepath.Join(q.QuadletDir, s.Name) }

// Validate checks the Podman version and runs the quadlet generator in dry-run mode over
// the Stack's files at the planned commit, in a throwaway directory.
func (q *Quadlet) Validate(ctx context.Context, s stacks.Stack, files map[string][]byte) error {
	if err := q.checkVersion(ctx); err != nil {
		return err
	}
	if q.Generator == "" {
		return errors.New("Podman's quadlet generator wasn't found (looked in " + strings.Join(GeneratorCandidates, ", ") + ")")
	}
	dir, err := os.MkdirTemp("", "scd-quadlet-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for name, content := range files {
		if filepath.Base(name) == stacks.SecretsFile {
			continue
		}
		p := filepath.Join(dir, s.Name, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			return err
		}
	}
	_, err = q.Runner.Run(ctx, runner.Cmd{
		Name: q.Generator,
		Args: []string{"--user", "--dryrun"},
		Env:  []string{"QUADLET_UNIT_DIRS=" + filepath.Join(dir, s.Name)},
	})
	var exit *runner.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("quadlet check failed: %s", strings.TrimSpace(string(exit.Result.Output)))
	}
	return err
}

// Deploy copies the Stack into its Quadlet subdirectory (everything tracked except
// secrets.env), stops units whose files were removed, writes the secrets drop-ins,
// reloads systemd and (re)starts the Stack's units.
func (q *Quadlet) Deploy(ctx context.Context, s stacks.Stack, out io.Writer) error {
	if err := q.checkVersion(ctx); err != nil {
		return err
	}
	files, err := q.TrackedFiles(ctx, s.Name)
	if err != nil {
		return err
	}
	var keep []string
	for _, f := range files {
		if filepath.Base(f) != stacks.SecretsFile {
			keep = append(keep, f)
		}
	}
	s.Files = keep
	dest := q.stackDir(s)

	// Units deployed before but gone now: stop them before their files disappear.
	old := q.deployedUnits(dest)
	var gone []string
	for _, f := range old {
		if !contains(s.QuadletUnits(), f) {
			gone = append(gone, stacks.ServiceName(f))
		}
	}
	if len(gone) > 0 {
		if err := q.systemctl(ctx, out, append([]string{"stop", "--"}, gone...)...); err != nil {
			return err
		}
	}

	if err := q.writeStack(s, dest); err != nil {
		return err
	}
	if err := q.systemctl(ctx, out, "daemon-reload"); err != nil {
		return err
	}
	services := s.Services()
	if len(services) == 0 {
		return nil
	}
	return q.systemctl(ctx, out, append([]string{"restart", "--"}, services...)...)
}

// Teardown stops the Stack's units, removes its Quadlet subdirectory and reloads
// systemd. Named volumes stay: stopping a .volume unit doesn't delete the volume.
func (q *Quadlet) Teardown(ctx context.Context, s stacks.Stack, out io.Writer) error {
	dest := q.stackDir(s)
	var services []string
	for _, f := range q.deployedUnits(dest) {
		services = append(services, stacks.ServiceName(f))
	}
	if len(services) > 0 {
		if err := q.systemctl(ctx, out, append([]string{"stop", "--"}, services...)...); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return q.systemctl(ctx, out, "daemon-reload")
}

// writeStack replaces dest's contents with the Stack's files plus generated drop-ins.
func (q *Quadlet) writeStack(s stacks.Stack, dest string) error {
	tmp := dest + ".scd-new"
	if err := os.RemoveAll(tmp); err != nil {
		return err
	}
	for _, f := range s.Files {
		src := filepath.Join(q.RepoPath, s.Name, filepath.FromSlash(f))
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		p := filepath.Join(tmp, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	secrets := filepath.Join(q.RepoPath, s.Name, stacks.SecretsFile)
	if _, err := os.Stat(secrets); err == nil {
		for _, f := range s.QuadletUnits() {
			if filepath.Ext(f) != ".container" {
				continue
			}
			dir := filepath.Join(tmp, f+".d")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			conf := "# Generated by scd: the Stack's secrets.env stays in the Deployment Repo clone.\n" +
				"[Container]\nEnvironmentFile=" + secrets + "\n"
			if err := os.WriteFile(filepath.Join(dir, DropInName), []byte(conf), 0o644); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

// deployedUnits lists the Quadlet unit files currently in a Stack's subdirectory.
func (q *Quadlet) deployedUnits(dest string) []string {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && stacks.ServiceName(e.Name()) != "" {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func (q *Quadlet) systemctl(ctx context.Context, out io.Writer, args ...string) error {
	_, err := q.Runner.Run(ctx, runner.Cmd{Name: "systemctl", Args: append([]string{"--user"}, args...), Output: out})
	return err
}

// checkVersion fails when Podman is older than MinPodman. It asks Podman once.
func (q *Quadlet) checkVersion(ctx context.Context) error {
	q.versionOnce.Do(func() {
		res, err := q.Runner.Run(ctx, runner.Cmd{Name: "podman", Args: []string{"version", "--format", "{{.Client.Version}}"}})
		if err != nil {
			q.versionErr = fmt.Errorf("checking the Podman version: %w", err)
			return
		}
		v := strings.TrimSpace(string(res.Output))
		major, minor, ok := parseVersion(v)
		if !ok {
			q.versionErr = fmt.Errorf("couldn't read the Podman version from %q", v)
			return
		}
		if major < MinPodman[0] || (major == MinPodman[0] && minor < MinPodman[1]) {
			q.versionErr = fmt.Errorf("Podman %s is too old: Quadlet Stacks need Podman %d.%d or newer", v, MinPodman[0], MinPodman[1])
		}
	})
	return q.versionErr
}

func parseVersion(v string) (major, minor int, ok bool) {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(strings.TrimLeftFunc(parts[1], func(r rune) bool { return r < '0' || r > '9' }))
	return major, minor, err1 == nil && err2 == nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
