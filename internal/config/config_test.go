package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
)

func write(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load(write(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":1234" {
		t.Errorf("Listen = %q, want :1234", cfg.Listen)
	}
	if cfg.Runtime != "" {
		t.Errorf("Runtime = %q, want auto-detect", cfg.Runtime)
	}
}

func TestLoadValues(t *testing.T) {
	cfg, err := config.Load(write(t, `
listen = "127.0.0.1:9000"
runtime = "podman"
socket = "/run/user/1000/podman/podman.sock"
`))
	if err != nil {
		t.Fatal(err)
	}
	want := config.Config{Listen: "127.0.0.1:9000", Runtime: config.Podman, Socket: "/run/user/1000/podman/podman.sock"}
	if cfg != want {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadRejectsUnknownRuntime(t *testing.T) {
	if _, err := config.Load(write(t, `runtime = "lxc"`)); err == nil {
		t.Fatal("want error")
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := config.Load(filepath.Join(t.TempDir(), "nope.toml"))
	if !errors.Is(err, config.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestRepoPathDefaultsToDataDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/data")
	cfg, err := config.Load(write(t, "[repo]\nurl = \"https://github.com/o/r.git\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo.Path != "/data/scd/repo" {
		t.Errorf("Repo.Path = %q, want /data/scd/repo", cfg.Repo.Path)
	}
}
