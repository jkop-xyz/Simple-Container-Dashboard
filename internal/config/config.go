// Package config loads scd's TOML configuration file.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Runtime names the container runtime a host uses.
type Runtime string

const (
	Docker Runtime = "docker"
	Podman Runtime = "podman"
)

// DefaultListen is the address the dashboard listens on when the config doesn't say.
const DefaultListen = ":1234"

// Config is the contents of scd's config file.
type Config struct {
	// Listen is the address the dashboard listens on, e.g. ":1234".
	Listen string `toml:"listen"`
	// Runtime is "docker" or "podman". Empty means auto-detect from the available sockets.
	Runtime Runtime `toml:"runtime,omitempty"`
	// Socket overrides the auto-detected runtime socket path.
	Socket string `toml:"socket,omitempty"`
	// Repo is the host's Deployment Repo. Optional: without it the dashboard only lists containers.
	Repo Repo `toml:"repo"`
}

// Repo configures the Deployment Repo.
type Repo struct {
	// URL is the git remote, e.g. "https://github.com/owner/name.git".
	URL string `toml:"url"`
	// Branch to track. Empty means the remote's default branch.
	Branch string `toml:"branch,omitempty"`
	// Path is where the clone lives on the host. Defaults to $XDG_DATA_HOME/scd/repo.
	Path string `toml:"path"`
}

// ErrNotFound is returned by Load when the config file doesn't exist.
var ErrNotFound = errors.New("config file not found")

// DefaultPath returns the config file location: $XDG_CONFIG_HOME/scd/config.toml,
// falling back to ~/.config/scd/config.toml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "scd", "config.toml"), nil
}

// DataDir returns scd's data directory: $XDG_DATA_HOME/scd, falling back to
// ~/.local/share/scd.
func DataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "scd"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "scd"), nil
}

// Load reads the config file at path and fills in defaults.
func Load(path string) (Config, error) {
	var cfg Config
	_, err := toml.DecodeFile(path, &cfg)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("reading config %s: %w", path, err)
	}
	if cfg.Listen == "" {
		cfg.Listen = DefaultListen
	}
	switch cfg.Runtime {
	case "", Docker, Podman:
	default:
		return Config{}, fmt.Errorf("config %s: runtime must be %q or %q, got %q", path, Docker, Podman, cfg.Runtime)
	}
	if cfg.Repo.URL != "" && cfg.Repo.Path == "" {
		dir, err := DataDir()
		if err != nil {
			return Config{}, err
		}
		cfg.Repo.Path = filepath.Join(dir, "repo")
	}
	return cfg, nil
}
