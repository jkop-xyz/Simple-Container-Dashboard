package setup_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/setup"
)

const (
	ubuntu = "NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n"
	fedora = "NAME=\"Fedora Linux\"\nID=fedora\nVERSION_ID=41\n"
	debian = "ID=debian\nVERSION_ID=\"12\"\n"
	rocky  = "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n"
	arch   = "NAME=\"CachyOS Linux\"\nID=cachyos\nID_LIKE=arch\n"
)

// answers scripts the operator: each question is matched by a substring.
type answers struct {
	t        *testing.T
	confirms map[string]bool
	choices  map[string]int
	inputs   map[string]string
	// pick chooses, from the picker's items, the first containing this text ("" = URL instead).
	pick  string
	asked []string
}

func (a *answers) find(q string, keys []string) string {
	a.asked = append(a.asked, q)
	for _, k := range keys {
		if strings.Contains(q, k) {
			return k
		}
	}
	a.t.Fatalf("unexpected question: %q", q)
	return ""
}

func (a *answers) Confirm(q string, _ bool) bool {
	return a.confirms[a.find(q, keys(a.confirms))]
}
func (a *answers) Choose(q string, _ []string) int { return a.choices[a.find(q, keys(a.choices))] }
func (a *answers) Input(q, _ string) string        { return a.inputs[a.find(q, keys(a.inputs))] }
func (a *answers) Pick(q string, items []string) int {
	a.asked = append(a.asked, q)
	for i, it := range items {
		if a.pick != "" && strings.Contains(it, a.pick) {
			return i
		}
	}
	return -1
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// host is a pretend machine: which tools exist, and commands that "install" more.
type host struct {
	mu        sync.Mutex
	installed map[string]bool
	cmds      []string
	noCompose bool
	// loggedOut makes `gh auth status` fail; repos is what `gh repo list` returns.
	loggedOut bool
	repos     string
	// groups is the output of `id -nG`; firewalld/ufw make that firewall active.
	groups    string
	firewalld bool
	ufw       bool
}

func newHost(tools ...string) *host {
	h := &host{installed: map[string]bool{}}
	for _, t := range tools {
		h.installed[t] = true
	}
	return h
}

func (h *host) LookPath(f string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.installed[f] {
		return "/usr/bin/" + f, nil
	}
	return "", errors.New("not found")
}

func (h *host) Run(_ context.Context, c runner.Cmd) (runner.Result, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	line := c.String()
	h.cmds = append(h.cmds, line)
	switch {
	case line == "docker compose version":
		if h.noCompose {
			return runner.Result{ExitCode: 1}, &runner.ExitError{Cmd: c, Result: runner.Result{ExitCode: 1}}
		}
	case strings.HasPrefix(line, "sudo sh ") && strings.HasSuffix(line, "get-docker.sh"):
		h.installed["docker"], h.noCompose = true, false
	case strings.HasPrefix(line, "sudo dnf install -y "), strings.HasPrefix(line, "sudo apt-get install -y "):
		for _, p := range strings.Fields(line)[4:] {
			h.installed[p] = true
		}
	case strings.HasPrefix(line, "id -nG "):
		return runner.Result{Output: []byte(h.groups + "\n")}, nil
	case line == "systemctl is-active firewalld":
		if h.firewalld {
			return runner.Result{Output: []byte("active\n")}, nil
		}
		return runner.Result{ExitCode: 3, Output: []byte("inactive\n")}, &runner.ExitError{Cmd: c, Result: runner.Result{ExitCode: 3}}
	case line == "sudo ufw status":
		if h.ufw {
			return runner.Result{Output: []byte("Status: active\n")}, nil
		}
		return runner.Result{Output: []byte("Status: inactive\n")}, nil
	case line == "gh auth status" && h.loggedOut:
		return runner.Result{ExitCode: 1}, &runner.ExitError{Cmd: c, Result: runner.Result{ExitCode: 1, Output: []byte("You are not logged into any GitHub hosts.")}}
	case strings.HasPrefix(line, "gh auth login"):
		h.loggedOut = false
	case strings.HasPrefix(line, "gh repo list"):
		repos := h.repos
		if repos == "" {
			repos = "[]"
		}
		return runner.Result{Output: []byte(repos)}, nil
	case c.Name == "git":
		if strings.Contains(line, "://") {
			return runner.Result{}, errors.New("tests never reach the network: " + line)
		}
		h.mu.Unlock()
		defer h.mu.Lock()
		return runner.Exec{}.Run(context.Background(), c) // real git, against a local bare repo
	}
	return runner.Result{}, nil
}

// installCmds keeps only the package/tool install steps (and the compose check).
func installCmds(cmds []string) []string {
	var out []string
	for _, c := range cmds {
		for _, p := range []string{"curl ", "sudo sh ", "sudo dnf install", "sudo apt-get install", "docker compose version"} {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

type run struct {
	err        error
	out        string
	cmds       []string
	configPath string
	unitPath   string
	dataDir    string
}

func runSetup(t *testing.T, osRelease string, h *host, a *answers) run {
	t.Helper()
	a.t = t
	dir := t.TempDir()
	var out bytes.Buffer
	env := setup.Env{
		Runner: h, Prompt: a, Out: &out, Stdin: strings.NewReader(""), LookPath: h.LookPath,
		OSRelease:  osRelease,
		ConfigPath: filepath.Join(dir, "config", "scd", "config.toml"),
		UnitDir:    filepath.Join(dir, "config", "systemd", "user"),
		DataDir:    filepath.Join(dir, "data", "scd"),
		Binary:     "/home/op/.local/bin/scd",
		TempDir:    filepath.Join(dir, "tmp"),
		User:       "op",
	}
	err := setup.Run(context.Background(), env)
	return run{err: err, out: out.String(), cmds: h.cmds, configPath: env.ConfigPath,
		unitPath: filepath.Join(env.UnitDir, "scd.service"), dataDir: env.DataDir}
}

// localRepo is a bare git repo standing in for the operator's GitHub repo.
var localRepo string

func repoAnswer() map[string]string {
	return map[string]string{"Deployment Repo URL": localRepo}
}

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "scd-setup-test-")
	defer os.RemoveAll(dir)
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.invalid", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.invalid"} {
		os.Setenv(k, v)
	}
	localRepo = filepath.Join(dir, "homelab.git")
	work := filepath.Join(dir, "work")
	for _, args := range [][]string{
		{"init", "--quiet", "--bare", "--initial-branch=main", localRepo},
		{"clone", "--quiet", localRepo, work},
		{"-C", work, "checkout", "--quiet", "-B", "main"},
		{"-C", work, "commit", "--quiet", "--allow-empty", "-m", "init"},
		{"-C", work, "push", "--quiet", "origin", "main"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			panic(fmt.Sprintf("git %v: %v\n%s", args, err, out))
		}
	}
	os.Exit(m.Run())
}

func assertCmds(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestDockerOnUbuntuUsesTheConvenienceScript(t *testing.T) {
	h := newHost("git", "gh")
	r := runSetup(t, ubuntu, h, &answers{
		choices:  map[string]int{"Neither Docker nor Podman": 0},
		confirms: map[string]bool{"Download and run it?": true},
		inputs:   repoAnswer(),
	})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	script := filepath.Join(filepath.Dir(r.dataDir), "..", "tmp", "get-docker.sh")
	script = filepath.Clean(script)
	assertCmds(t, installCmds(r.cmds), []string{
		"curl -fsSL https://get.docker.com -o " + script,
		"sudo sh " + script,
		"docker compose version",
	})
	if !strings.Contains(r.out, "curl -fsSL https://get.docker.com") || !strings.Contains(r.out, "rather than production") {
		t.Errorf("setup must show what it will run before asking:\n%s", r.out)
	}
}

func TestDeclinedDockerScriptRunsNothing(t *testing.T) {
	h := newHost("git", "gh")
	r := runSetup(t, ubuntu, h, &answers{
		choices:  map[string]int{"Neither": 0},
		confirms: map[string]bool{"Download and run it?": false},
	})
	if r.err == nil {
		t.Fatal("want an error when Docker isn't installed")
	}
	assertCmds(t, installCmds(r.cmds), nil)
	if _, err := os.Stat(r.configPath); err == nil {
		t.Error("config written although setup stopped")
	}
}

func TestDockerOnFedoraInstallsMissingPackagesWithDnf(t *testing.T) {
	h := newHost("docker")
	r := runSetup(t, fedora, h, &answers{
		confirms: map[string]bool{"Install git, gh with `sudo dnf install -y git gh`?": true},
		inputs:   repoAnswer(),
	})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	assertCmds(t, installCmds(r.cmds), []string{"docker compose version", "sudo dnf install -y git gh", "docker compose version"})
}

func TestMissingComposePluginInstallsDockerViaTheScript(t *testing.T) {
	h := newHost("git", "gh", "docker")
	h.noCompose = true
	r := runSetup(t, debian, h, &answers{confirms: map[string]bool{"Download and run it?": true}, inputs: repoAnswer()})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "Missing: docker compose") {
		t.Errorf("output:\n%s", r.out)
	}
}

func TestPodmanOnDebianUsesApt(t *testing.T) {
	h := newHost("git")
	r := runSetup(t, debian, h, &answers{
		choices:  map[string]int{"Neither": 1},
		confirms: map[string]bool{"Install gh, podman with `sudo apt-get install -y gh podman`?": true},
		inputs:   repoAnswer(),
	})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	assertCmds(t, installCmds(r.cmds), []string{"sudo apt-get install -y gh podman"})
}

func TestPodmanOnRockyUsesDnf(t *testing.T) {
	h := newHost("git", "gh")
	r := runSetup(t, rocky, h, &answers{
		choices:  map[string]int{"Neither": 1},
		confirms: map[string]bool{"Install podman with `sudo dnf install -y podman`?": true},
		inputs:   repoAnswer(),
	})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	assertCmds(t, installCmds(r.cmds), []string{"sudo dnf install -y podman"})
}

func TestUnsupportedDistroOnlyPrintsInstructions(t *testing.T) {
	h := newHost("git", "docker")
	r := runSetup(t, arch, h, &answers{})
	if r.err == nil {
		t.Fatal("want an error listing what's missing")
	}
	assertCmds(t, installCmds(r.cmds), []string{"docker compose version"})
	for _, want := range []string{"dnf- and apt-based", `"cachyos"`, "gh: https://cli.github.com", "run `scd setup` again"} {
		if !strings.Contains(r.out, want) {
			t.Errorf("output missing %q:\n%s", want, r.out)
		}
	}
}

func TestBothRuntimesInstalledAsksWhichOne(t *testing.T) {
	h := newHost("git", "gh", "docker", "podman")
	a := &answers{choices: map[string]int{"Both Docker and Podman are installed": 1}, inputs: repoAnswer()}
	r := runSetup(t, fedora, h, a)
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	cfg, err := config.Load(r.configPath)
	if err != nil || cfg.Runtime != config.Podman {
		t.Errorf("runtime %q, err %v", cfg.Runtime, err)
	}
}

func TestConfigAndUnitAreWrittenAndNextStepsPrinted(t *testing.T) {
	h := newHost("git", "gh", "docker")
	r := runSetup(t, ubuntu, h, &answers{inputs: repoAnswer()})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	cfg, err := config.Load(r.configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := config.Config{Listen: ":1234", Runtime: config.Docker,
		Repo: config.Repo{URL: localRepo, Path: filepath.Join(r.dataDir, "repo")}}
	if cfg != want {
		t.Errorf("config %+v\nwant   %+v", cfg, want)
	}
	if fi, _ := os.Stat(r.configPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v, want 0600", fi.Mode().Perm())
	}
	unit, err := os.ReadFile(r.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"ExecStart=/home/op/.local/bin/scd serve", "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(string(unit), w) {
			t.Errorf("unit missing %q:\n%s", w, unit)
		}
	}
	for _, w := range []string{"systemctl --user daemon-reload", "systemctl --user enable --now scd.service", ":1234"} {
		if !strings.Contains(r.out, w) {
			t.Errorf("next steps missing %q", w)
		}
	}
	for _, c := range r.cmds {
		if strings.Contains(c, "scd.service") {
			t.Errorf("setup started the service itself: %q", c)
		}
	}
}

func TestPodmanUnitStartsAfterThePodmanSocket(t *testing.T) {
	if u := setup.Unit("/bin/scd", config.Podman); !strings.Contains(u, "After=network-online.target podman.socket") {
		t.Errorf("unit:\n%s", u)
	}
}

func TestExistingConfigIsKeptUnlessReplaced(t *testing.T) {
	h := newHost("git", "gh", "docker")
	a := &answers{inputs: repoAnswer(), confirms: map[string]bool{"Replace it?": false}}
	a.t = t
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte("listen = \":9999\"\n"), 0o600)
	var out bytes.Buffer
	err := setup.Run(context.Background(), setup.Env{
		Runner: h, Prompt: a, Out: &out, Stdin: strings.NewReader(""), LookPath: h.LookPath, OSRelease: ubuntu,
		ConfigPath: cfgPath, UnitDir: filepath.Join(dir, "units"), DataDir: filepath.Join(dir, "data"),
		Binary: "/bin/scd", TempDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cfgPath); string(b) != "listen = \":9999\"\n" {
		t.Errorf("existing config replaced: %q", b)
	}
}

func TestDetectDistro(t *testing.T) {
	for _, tc := range []struct {
		release, manager string
		script           bool
	}{
		{ubuntu, "apt", true}, {debian, "apt", true}, {fedora, "dnf", true}, {rocky, "dnf", false}, {arch, "", false},
	} {
		d := setup.DetectDistro(tc.release)
		if d.Manager != tc.manager || d.DockerScript != tc.script {
			t.Errorf("%s: %+v", d.ID, d)
		}
	}
}
