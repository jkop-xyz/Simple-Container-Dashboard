// Package setup is `scd setup`: the interactive first run that prepares a fresh host.
// Every command runs through the command runner and every question through the
// Prompter, so the whole flow can be tested without touching a real system.
package setup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
)

// DockerScriptURL is Docker's convenience install script.
const DockerScriptURL = "https://get.docker.com"

// Env is everything setup touches.
type Env struct {
	Runner runner.Runner
	Prompt Prompter
	Out    io.Writer
	// Stdin is handed to interactive commands (sudo, gh auth login).
	Stdin    io.Reader
	LookPath func(string) (string, error)
	// OSRelease is the content of /etc/os-release.
	OSRelease string
	// ConfigPath is where the config file goes; UnitDir is the user's systemd unit
	// directory; DataDir is scd's data directory.
	ConfigPath, UnitDir, DataDir string
	// Binary is the scd executable the service unit runs.
	Binary string
	// TempDir holds downloaded install scripts.
	TempDir string
}

// Distro is the host's package-manager family.
type Distro struct {
	ID string
	// Manager is "dnf", "apt", or "" when unsupported.
	Manager string
	// DockerScript reports whether get.docker.com supports this distro.
	DockerScript bool
}

// DetectDistro reads /etc/os-release.
func DetectDistro(osRelease string) Distro {
	fields := map[string]string{}
	for _, line := range strings.Split(osRelease, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			fields[k] = strings.Trim(v, `"'`)
		}
	}
	d := Distro{ID: fields["ID"]}
	ids := append([]string{fields["ID"]}, strings.Fields(fields["ID_LIKE"])...)
	for _, id := range ids {
		switch id {
		case "fedora", "rhel", "centos", "rocky", "almalinux":
			d.Manager = "dnf"
		case "debian", "ubuntu", "raspbian":
			d.Manager = "apt"
		}
		if d.Manager != "" {
			break
		}
	}
	switch d.ID {
	case "debian", "ubuntu", "raspbian", "fedora", "rhel", "centos":
		d.DockerScript = true
	}
	return d
}

// state is what setup has learned so far.
type state struct {
	runtime config.Runtime
	distro  Distro
	repo    config.Repo
}

// Run runs the whole setup flow.
func Run(ctx context.Context, env Env) error {
	s := &state{distro: DetectDistro(env.OSRelease)}
	env.say("scd setup — this prepares the host to run the Simple Container Dashboard.\n")

	if err := s.chooseRuntime(env); err != nil {
		return err
	}
	if err := s.installTools(ctx, env); err != nil {
		return err
	}
	if err := s.chooseRepo(ctx, env); err != nil {
		return err
	}
	if err := s.writeConfig(env); err != nil {
		return err
	}
	if err := s.writeUnit(env); err != nil {
		return err
	}
	s.nextSteps(env)
	return nil
}

func (env Env) say(format string, args ...any) { fmt.Fprintf(env.Out, format+"\n", args...) }

func (env Env) has(tool string) bool {
	_, err := env.LookPath(tool)
	return err == nil
}

// chooseRuntime picks Docker or Podman, asking when both or neither are installed.
func (s *state) chooseRuntime(env Env) error {
	docker, podman := env.has("docker"), env.has("podman")
	switch {
	case docker && !podman:
		s.runtime = config.Docker
	case podman && !docker:
		s.runtime = config.Podman
	default:
		q := "Neither Docker nor Podman is installed. Which runtime should this host use?"
		if docker && podman {
			q = "Both Docker and Podman are installed. Which runtime should scd manage?"
		}
		if env.Prompt.Choose(q, []string{"Docker (Compose Stacks)", "Podman, rootless (Quadlet Stacks)"}) == 0 {
			s.runtime = config.Docker
		} else {
			s.runtime = config.Podman
		}
	}
	env.say("Runtime: %s", s.runtime)
	return nil
}

// missingTools lists what the chosen runtime needs and isn't installed.
func (s *state) missingTools(ctx context.Context, env Env) []string {
	var missing []string
	for _, t := range []string{"git", "gh"} {
		if !env.has(t) {
			missing = append(missing, t)
		}
	}
	switch s.runtime {
	case config.Docker:
		if !env.has("docker") {
			missing = append(missing, "docker")
		} else if _, err := env.Runner.Run(ctx, runner.Cmd{Name: "docker", Args: []string{"compose", "version"}}); err != nil {
			missing = append(missing, "docker compose")
		}
	case config.Podman:
		if !env.has("podman") {
			missing = append(missing, "podman")
		}
	}
	return missing
}

// installTools offers to install what's missing with dnf or apt (Docker via its
// convenience script), or prints instructions on other distros.
func (s *state) installTools(ctx context.Context, env Env) error {
	missing := s.missingTools(ctx, env)
	if len(missing) == 0 {
		env.say("All required tools are installed.")
		return nil
	}
	env.say("Missing: %s", strings.Join(missing, ", "))
	if s.distro.Manager == "" {
		env.say("\nscd setup can only install packages on dnf- and apt-based distributions (this is %q).", s.distro.ID)
		env.say("Install these yourself, then run `scd setup` again:")
		for _, m := range missing {
			env.say("  - %s: %s", m, instructions[m])
		}
		return errors.New("missing tools: " + strings.Join(missing, ", "))
	}

	var pkgs []string
	docker := false
	for _, m := range missing {
		switch m {
		case "docker", "docker compose":
			docker = true
		default:
			pkgs = append(pkgs, m)
		}
	}
	if len(pkgs) > 0 {
		cmd := s.installCmd(pkgs)
		if !env.Prompt.Confirm(fmt.Sprintf("Install %s with `%s`?", strings.Join(pkgs, ", "), strings.Join(cmd, " ")), true) {
			return errors.New("setup needs " + strings.Join(pkgs, ", "))
		}
		if err := env.run(ctx, cmd...); err != nil {
			return err
		}
	}
	if docker {
		if err := s.installDocker(ctx, env); err != nil {
			return err
		}
	}
	if still := s.missingTools(ctx, env); len(still) > 0 {
		return errors.New("still missing after install: " + strings.Join(still, ", "))
	}
	return nil
}

func (s *state) installCmd(pkgs []string) []string {
	if s.distro.Manager == "dnf" {
		return append([]string{"sudo", "dnf", "install", "-y"}, pkgs...)
	}
	return append([]string{"sudo", "apt-get", "install", "-y"}, pkgs...)
}

// installDocker runs Docker's convenience script after showing it and asking.
func (s *state) installDocker(ctx context.Context, env Env) error {
	if !s.distro.DockerScript {
		env.say("Docker's convenience script doesn't support %q; install Docker yourself: %s", s.distro.ID, instructions["docker"])
		return errors.New("docker is not installed")
	}
	script := filepath.Join(env.TempDir, "get-docker.sh")
	env.say("\nDocker will be installed with Docker's convenience script (%s).", DockerScriptURL)
	env.say("Docker notes that the script is meant for quick setups rather than production. It will run:")
	env.say("  curl -fsSL %s -o %s", DockerScriptURL, script)
	env.say("  sudo sh %s", script)
	if !env.Prompt.Confirm("Download and run it?", false) {
		return errors.New("docker is not installed")
	}
	if err := env.run(ctx, "curl", "-fsSL", DockerScriptURL, "-o", script); err != nil {
		return err
	}
	return env.run(ctx, "sudo", "sh", script)
}

// run runs an interactive command, showing its output.
func (env Env) run(ctx context.Context, args ...string) error {
	env.say("$ %s", strings.Join(args, " "))
	_, err := env.Runner.Run(ctx, runner.Cmd{Name: args[0], Args: args[1:], Stdin: env.Stdin, Output: env.Out})
	return err
}

// chooseRepo asks for the Deployment Repo.
func (s *state) chooseRepo(ctx context.Context, env Env) error {
	url := env.Prompt.Input("Deployment Repo URL (e.g. https://github.com/you/homelab.git)", "")
	if url == "" {
		return errors.New("a Deployment Repo URL is required")
	}
	s.repo = config.Repo{URL: url, Path: filepath.Join(env.DataDir, "repo")}
	return nil
}

func (s *state) writeConfig(env Env) error {
	if _, err := os.Stat(env.ConfigPath); err == nil {
		if !env.Prompt.Confirm(fmt.Sprintf("A config already exists at %s. Replace it?", env.ConfigPath), false) {
			env.say("Keeping the existing config.")
			return nil
		}
	}
	cfg := config.Config{Listen: config.DefaultListen, Runtime: s.runtime, Repo: s.repo}
	if err := os.MkdirAll(filepath.Dir(env.ConfigPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(env.ConfigPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "# scd configuration, written by `scd setup`.")
	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return err
	}
	env.say("Wrote %s", env.ConfigPath)
	return nil
}

// UnitName is the systemd user unit scd runs as.
const UnitName = "scd.service"

// Unit renders the systemd user unit.
func Unit(binary string, rt config.Runtime) string {
	after := "network-online.target"
	if rt == config.Podman {
		after += " podman.socket"
	}
	return fmt.Sprintf(`[Unit]
Description=Simple Container Dashboard
Wants=network-online.target
After=%s

[Service]
ExecStart=%s serve
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`, after, binary)
}

func (s *state) writeUnit(env Env) error {
	if err := os.MkdirAll(env.UnitDir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(env.UnitDir, UnitName)
	if err := os.WriteFile(p, []byte(Unit(env.Binary, s.runtime)), 0o644); err != nil {
		return err
	}
	env.say("Wrote %s", p)
	return nil
}

// nextSteps prints, but doesn't run, how to start the service.
func (s *state) nextSteps(env Env) {
	env.say("\nSetup is done. To start scd:")
	env.say("  systemctl --user daemon-reload")
	env.say("  systemctl --user enable --now %s", UnitName)
	env.say("Then open http://<this host>:1234")
}

// instructions are shown on distros setup can't install packages on.
var instructions = map[string]string{
	"git":            "your distribution's git package",
	"gh":             "https://cli.github.com",
	"docker":         "https://docs.docker.com/engine/install/",
	"docker compose": "https://docs.docker.com/compose/install/linux/",
	"podman":         "https://podman.io/docs/installation",
}
