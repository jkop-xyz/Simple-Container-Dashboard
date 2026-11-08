package setup_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/setup"
)

func ghCmds(cmds []string) []string {
	var out []string
	for _, c := range cmds {
		if strings.HasPrefix(c, "gh ") {
			out = append(out, c)
		}
	}
	return out
}

func reposJSON() string {
	url := strings.TrimSuffix(localRepo, ".git") // gh reports URLs without .git
	return `[
	  {"nameWithOwner":"op/zzz-notes","url":"https://github.com/op/zzz-notes","isPrivate":false,"defaultBranchRef":{"name":"master"}},
	  {"nameWithOwner":"op/homelab","url":"` + url + `","isPrivate":true,"defaultBranchRef":{"name":"main"}}
	]`
}

func TestLoggedOutOperatorIsLoggedInAndGitIsWiredToGh(t *testing.T) {
	h := newHost("git", "gh", "docker")
	h.loggedOut = true
	r := runSetup(t, ubuntu, h, &answers{inputs: repoAnswer()})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	assertCmds(t, ghCmds(r.cmds), []string{
		"gh auth status",
		"gh auth login --hostname github.com --git-protocol https",
		"gh auth setup-git",
		"gh repo list --limit 1000 --json nameWithOwner,url,isPrivate,defaultBranchRef",
	})
}

func TestLoggedInOperatorSkipsLogin(t *testing.T) {
	h := newHost("git", "gh", "docker")
	r := runSetup(t, ubuntu, h, &answers{inputs: repoAnswer()})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	for _, c := range r.cmds {
		if strings.HasPrefix(c, "gh auth login") {
			t.Errorf("logged in already, but ran %q", c)
		}
	}
	if !strings.Contains(r.out, "Already logged in to GitHub") {
		t.Error("should say it's already logged in")
	}
}

func TestPickingARepoUsesItsDefaultBranchAndClonesIt(t *testing.T) {
	h := newHost("git", "gh", "docker")
	h.repos = reposJSON()
	a := &answers{pick: "op/homelab"}
	r := runSetup(t, ubuntu, h, a)
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	cfg, err := config.Load(r.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo.URL != localRepo || cfg.Repo.Branch != "main" {
		t.Errorf("repo %+v", cfg.Repo)
	}
	if _, err := os.Stat(filepath.Join(cfg.Repo.Path, ".git")); err != nil {
		t.Errorf("not cloned during setup: %v", err)
	}
	for _, q := range a.asked {
		if strings.Contains(q, "branch") {
			t.Errorf("asked about the branch: %q", q)
		}
	}
}

func TestPickerFallsBackToAURL(t *testing.T) {
	h := newHost("git", "gh", "docker")
	h.repos = reposJSON()
	a := &answers{pick: "", inputs: repoAnswer()}
	r := runSetup(t, ubuntu, h, a)
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	cfg, _ := config.Load(r.configPath)
	if cfg.Repo.URL != localRepo || cfg.Repo.Branch != "" {
		t.Errorf("repo %+v", cfg.Repo)
	}
}

func TestCloneFailureIsExplainedAndStopsSetup(t *testing.T) {
	h := newHost("git", "gh", "docker")
	r := runSetup(t, ubuntu, h, &answers{inputs: map[string]string{"Deployment Repo URL": "/no/such/repo.git"}})
	if r.err == nil || !strings.Contains(r.err.Error(), "gh auth status") {
		t.Fatalf("err %v", r.err)
	}
	if _, err := os.Stat(r.configPath); err == nil {
		t.Error("config written although the clone failed")
	}
}

func TestExistingCloneIsReused(t *testing.T) {
	h := newHost("git", "gh", "docker")
	dir := t.TempDir()
	clone := filepath.Join(dir, "data", "repo")
	if out, err := exec.Command("git", "clone", "--quiet", localRepo, clone).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var out bytes.Buffer
	a := &answers{t: t, inputs: repoAnswer()}
	err := setup.Run(t.Context(), setup.Env{
		Runner: h, Prompt: a, Out: &out, Stdin: strings.NewReader(""), LookPath: h.LookPath, OSRelease: ubuntu,
		ConfigPath: filepath.Join(dir, "config.toml"), UnitDir: filepath.Join(dir, "units"),
		DataDir: filepath.Join(dir, "data"), Binary: "/bin/scd", TempDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Using the existing clone") {
		t.Errorf("output:\n%s", out.String())
	}
	for _, c := range h.cmds {
		if strings.HasPrefix(c, "git clone") {
			t.Errorf("cloned again: %q", c)
		}
	}
}

func TestTerminalPickerFiltersByTyping(t *testing.T) {
	items := []string{"op/homelab (private)", "op/dotfiles", "op/homelab-old"}
	var out bytes.Buffer
	term := setup.NewTerminal(strings.NewReader("old\n1\n"), &out)
	if got := term.Pick("Which repo?", items); got != 2 {
		t.Errorf("picked %d, want 2 (op/homelab-old)", got)
	}
	if !strings.Contains(out.String(), "1) op/homelab-old") {
		t.Errorf("filtered list not shown:\n%s", out.String())
	}
	if got := setup.NewTerminal(strings.NewReader("0\n"), &out).Pick("Which repo?", items); got != -1 {
		t.Errorf("0 should mean 'enter a URL instead', got %d", got)
	}
}
