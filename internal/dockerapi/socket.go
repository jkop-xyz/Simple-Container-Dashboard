package dockerapi

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
)

// DockerSocket is the standard rootful Docker socket.
const DockerSocket = "/var/run/docker.sock"

// Env is the slice of the host environment socket detection needs. Tests supply fakes.
type Env struct {
	Getenv func(string) string
	Exists func(path string) bool
}

// DetectSocket picks the runtime socket. An explicit override wins; then DOCKER_HOST
// (unix:// only); then the standard socket for the configured runtime. With no runtime
// configured, it returns whichever of the Docker or rootless Podman socket exists, and
// fails if both or neither do.
func DetectSocket(runtime config.Runtime, override string, env Env) (string, config.Runtime, error) {
	if override != "" {
		return override, runtime, nil
	}
	if host := env.Getenv("DOCKER_HOST"); host != "" {
		path, ok := strings.CutPrefix(host, "unix://")
		if !ok {
			return "", "", fmt.Errorf("DOCKER_HOST=%q: only unix:// sockets are supported", host)
		}
		return path, runtime, nil
	}

	podman := ""
	if dir := env.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		podman = filepath.Join(dir, "podman", "podman.sock")
	}
	switch runtime {
	case config.Docker:
		return DockerSocket, runtime, nil
	case config.Podman:
		if podman == "" {
			return "", "", errors.New("XDG_RUNTIME_DIR is not set; cannot locate the rootless Podman socket (set socket in the config)")
		}
		return podman, runtime, nil
	}

	hasDocker := env.Exists(DockerSocket)
	hasPodman := podman != "" && env.Exists(podman)
	switch {
	case hasDocker && hasPodman:
		return "", "", fmt.Errorf("both %s and %s exist; set runtime in the config", DockerSocket, podman)
	case hasDocker:
		return DockerSocket, config.Docker, nil
	case hasPodman:
		return podman, config.Podman, nil
	}
	return "", "", errors.New("no Docker or Podman socket found; is the runtime running? (set socket in the config to override)")
}
