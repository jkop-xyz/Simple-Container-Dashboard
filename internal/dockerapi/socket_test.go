package dockerapi_test

import (
	"testing"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/config"
	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
)

const podmanSock = "/run/user/1000/podman/podman.sock"

func env(vars map[string]string, existing ...string) dockerapi.Env {
	return dockerapi.Env{
		Getenv: func(k string) string { return vars[k] },
		Exists: func(p string) bool {
			for _, e := range existing {
				if e == p {
					return true
				}
			}
			return false
		},
	}
}

func TestDetectSocket(t *testing.T) {
	xdg := map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}
	tests := []struct {
		name        string
		runtime     config.Runtime
		override    string
		env         dockerapi.Env
		wantSocket  string
		wantRuntime config.Runtime
		wantErr     bool
	}{
		{name: "override wins", runtime: config.Podman, override: "/tmp/x.sock", env: env(xdg, dockerapi.DockerSocket),
			wantSocket: "/tmp/x.sock", wantRuntime: config.Podman},
		{name: "DOCKER_HOST unix", env: env(map[string]string{"DOCKER_HOST": "unix:///srv/d.sock"}),
			wantSocket: "/srv/d.sock"},
		{name: "DOCKER_HOST tcp rejected", env: env(map[string]string{"DOCKER_HOST": "tcp://1.2.3.4:2375"}), wantErr: true},
		{name: "configured docker", runtime: config.Docker, env: env(xdg),
			wantSocket: dockerapi.DockerSocket, wantRuntime: config.Docker},
		{name: "configured podman", runtime: config.Podman, env: env(xdg),
			wantSocket: podmanSock, wantRuntime: config.Podman},
		{name: "configured podman without XDG_RUNTIME_DIR", runtime: config.Podman, env: env(nil), wantErr: true},
		{name: "auto: only docker", env: env(xdg, dockerapi.DockerSocket),
			wantSocket: dockerapi.DockerSocket, wantRuntime: config.Docker},
		{name: "auto: only podman", env: env(xdg, podmanSock),
			wantSocket: podmanSock, wantRuntime: config.Podman},
		{name: "auto: both", env: env(xdg, dockerapi.DockerSocket, podmanSock), wantErr: true},
		{name: "auto: neither", env: env(xdg), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sock, rt, err := dockerapi.DetectSocket(tt.runtime, tt.override, tt.env)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q %q", sock, rt)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if sock != tt.wantSocket || rt != tt.wantRuntime {
				t.Errorf("got (%q, %q), want (%q, %q)", sock, rt, tt.wantSocket, tt.wantRuntime)
			}
		})
	}
}
