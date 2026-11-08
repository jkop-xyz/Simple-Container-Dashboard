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
	Runtime Runtime `toml:"runtime"`
	// Socket overrides the auto-detected runtime socket path.
	Socket string `toml:"socket"`
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
	return cfg, nil
}
