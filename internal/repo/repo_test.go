package repo_test

import (
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/repo"
)

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/jkop-xyz/homelab.git": "jkop-xyz/homelab",
		"https://github.com/jkop-xyz/homelab":     "jkop-xyz/homelab",
		"git@github.com:jkop-xyz/homelab.git":     "jkop-xyz/homelab",
		"ssh://git@github.com/jkop-xyz/homelab":   "jkop-xyz/homelab",
		"/srv/git/homelab.git":                    "/srv/git/homelab.git",
	} {
		if got := repo.DisplayName(in); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", in, got, want)
		}
	}
}
