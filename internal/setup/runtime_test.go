package setup_test

import (
	"strings"
	"testing"
)

// prepCmds keeps the runtime-preparation and firewall steps.
func prepCmds(cmds []string) []string {
	var out []string
	for _, c := range cmds {
		for _, p := range []string{"sudo usermod", "sudo systemctl", "systemctl --user", "loginctl", "sudo firewall-cmd", "sudo ufw allow"} {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func TestDockerHostAddsTheUserToTheDockerGroup(t *testing.T) {
	h := newHost("git", "gh", "docker")
	h.groups = "op wheel"
	r := runSetup(t, fedora, h, &answers{inputs: repoAnswer()})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	assertCmds(t, prepCmds(r.cmds), []string{
		"sudo usermod --append --groups docker op",
		"sudo systemctl enable --now docker.service",
	})
	if !strings.Contains(r.out, "log out and back in") {
		t.Errorf("missing re-login notice:\n%s", r.out)
	}
	if strings.Index(r.out, "log out and back in") < strings.Index(r.out, "Setup is done") {
		t.Error("re-login notice should come with the start instructions")
	}
}

func TestDockerUserAlreadyInTheGroupNeedsNoRelogin(t *testing.T) {
	h := newHost("git", "gh", "docker")
	h.groups = "op wheel docker"
	r := runSetup(t, fedora, h, &answers{inputs: repoAnswer()})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	assertCmds(t, prepCmds(r.cmds), []string{"sudo systemctl enable --now docker.service"})
	if strings.Contains(r.out, "log out and back in") {
		t.Error("no re-login needed")
	}
}

func TestPodmanHostEnablesSocketAutoUpdateAndLinger(t *testing.T) {
	h := newHost("git", "gh", "podman")
	r := runSetup(t, debian, h, &answers{inputs: repoAnswer()})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	assertCmds(t, prepCmds(r.cmds), []string{
		"systemctl --user enable --now podman.socket",
		"systemctl --user enable --now podman-auto-update.timer",
		"loginctl enable-linger op",
	})
}

func TestFirewalldPortIsOpenedWhenAccepted(t *testing.T) {
	h := newHost("git", "gh", "podman")
	h.firewalld = true
	r := runSetup(t, fedora, h, &answers{inputs: repoAnswer(), confirms: map[string]bool{"firewalld is active. Open port 1234/tcp": true}})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	got := prepCmds(r.cmds)
	assertCmds(t, got[len(got)-2:], []string{
		"sudo firewall-cmd --permanent --add-port=1234/tcp",
		"sudo firewall-cmd --reload",
	})
}

func TestFirewallIsLeftAloneWhenDeclined(t *testing.T) {
	h := newHost("git", "gh", "podman")
	h.firewalld = true
	r := runSetup(t, fedora, h, &answers{inputs: repoAnswer(), confirms: map[string]bool{"Open port 1234/tcp": false}})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	for _, c := range r.cmds {
		if strings.HasPrefix(c, "sudo firewall-cmd") {
			t.Errorf("declined, but ran %q", c)
		}
	}
	if !strings.Contains(r.out, "open port 1234/tcp yourself") {
		t.Errorf("output:\n%s", r.out)
	}
}

func TestUfwPortIsOpened(t *testing.T) {
	h := newHost("git", "gh", "docker", "ufw")
	h.groups = "docker"
	h.ufw = true
	r := runSetup(t, ubuntu, h, &answers{inputs: repoAnswer(), confirms: map[string]bool{"ufw is active. Open port 1234/tcp": true}})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	got := prepCmds(r.cmds)
	if got[len(got)-1] != "sudo ufw allow 1234/tcp" {
		t.Errorf("commands %q", got)
	}
}

func TestNoFirewallMeansNothingToOpen(t *testing.T) {
	h := newHost("git", "gh", "docker", "ufw")
	h.groups = "docker"
	r := runSetup(t, ubuntu, h, &answers{inputs: repoAnswer()}) // no firewall question scripted: asking would fail
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	if !strings.Contains(r.out, "No active firewall") {
		t.Errorf("output:\n%s", r.out)
	}
}

func TestFirewallStepOnlyEverTouchesTheDashboardPort(t *testing.T) {
	h := newHost("git", "gh", "podman")
	h.firewalld = true
	r := runSetup(t, fedora, h, &answers{inputs: repoAnswer(), confirms: map[string]bool{"Open port 1234/tcp": true}})
	if r.err != nil {
		t.Fatal(r.err)
	}
	for _, c := range r.cmds {
		if strings.Contains(c, "firewall-cmd") && strings.Contains(c, "port") && !strings.Contains(c, "1234/tcp") {
			t.Errorf("touched another port: %q", c)
		}
	}
}
