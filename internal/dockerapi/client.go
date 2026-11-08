// Package dockerapi is a small client for the Docker-compatible REST API, which both
// Docker and Podman serve on their sockets (see ADR 0001).
package dockerapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Container is one container as reported by the runtime.
type Container struct {
	ID     string
	Name   string
	Image  string
	State  string // e.g. "running", "exited", "created"
	Status string // human-readable, e.g. "Exited (1) 3 hours ago"
	Labels map[string]string
}

// Client talks to a runtime over its Unix socket.
type Client struct {
	socket string
	http   *http.Client
}

// New returns a client for the Docker-compatible API on the given Unix socket path.
func New(socket string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	return &Client{socket: socket, http: &http.Client{Transport: transport}}
}

// Socket returns the socket path the client talks to.
func (c *Client) Socket() string { return c.socket }

// ListContainers returns every container on the host, including stopped ones.
func (c *Client) ListContainers(ctx context.Context) ([]Container, error) {
	var wire []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Status string            `json:"Status"`
		Labels map[string]string `json:"Labels"`
	}
	if err := c.getJSON(ctx, "/containers/json?all=true", &wire); err != nil {
		return nil, err
	}
	out := make([]Container, 0, len(wire))
	for _, w := range wire {
		name := w.ID
		if len(w.Names) > 0 {
			name = strings.TrimPrefix(w.Names[0], "/")
		}
		out = append(out, Container{
			ID:     w.ID,
			Name:   name,
			Image:  w.Image,
			State:  w.State,
			Status: w.Status,
			Labels: w.Labels,
		})
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	// The host part is ignored: the transport always dials the Unix socket.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://runtime"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("runtime API at %s: %w", c.socket, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("runtime API %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		return fmt.Errorf("runtime API %s: decoding response: %w", path, err)
	}
	return nil
}

// Details are the per-container fields only inspect provides.
type Details struct {
	RestartCount int
	// Health is "healthy", "unhealthy", "starting", or "" when there's no healthcheck.
	Health string
}

// Inspect returns a container's restart count and health.
func (c *Client) Inspect(ctx context.Context, id string) (Details, error) {
	var wire struct {
		RestartCount int `json:"RestartCount"`
		State        struct {
			Health *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := c.getJSON(ctx, "/containers/"+id+"/json", &wire); err != nil {
		return Details{}, err
	}
	d := Details{RestartCount: wire.RestartCount}
	if wire.State.Health != nil {
		d.Health = wire.State.Health.Status
	}
	return d, nil
}

// Sample is one resource-usage reading of a container.
type Sample struct {
	// CPUTotal and SystemCPU are cumulative CPU times in nanoseconds; CPU % comes from the
	// difference between two samples.
	CPUTotal, SystemCPU uint64
	// PreCPUTotal and PreSystemCPU are the runtime's own previous reading, if it gave one.
	PreCPUTotal, PreSystemCPU uint64
	OnlineCPUs                uint32
	// Memory is usage minus reclaimable page cache, in bytes.
	Memory uint64
}

// Stats returns one resource-usage sample without waiting for a second reading.
func (c *Client) Stats(ctx context.Context, id string) (Sample, error) {
	type cpu struct {
		Usage struct {
			Total  uint64   `json:"total_usage"`
			PerCPU []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		System uint64 `json:"system_cpu_usage"`
		Online uint32 `json:"online_cpus"`
	}
	var wire struct {
		CPU    cpu `json:"cpu_stats"`
		PreCPU cpu `json:"precpu_stats"`
		Memory struct {
			Usage uint64            `json:"usage"`
			Stats map[string]uint64 `json:"stats"`
		} `json:"memory_stats"`
	}
	if err := c.getJSON(ctx, "/containers/"+id+"/stats?stream=false&one-shot=true", &wire); err != nil {
		return Sample{}, err
	}
	s := Sample{
		CPUTotal: wire.CPU.Usage.Total, SystemCPU: wire.CPU.System,
		PreCPUTotal: wire.PreCPU.Usage.Total, PreSystemCPU: wire.PreCPU.System,
		OnlineCPUs: wire.CPU.Online,
	}
	if s.OnlineCPUs == 0 {
		s.OnlineCPUs = uint32(len(wire.CPU.Usage.PerCPU))
	}
	cache := wire.Memory.Stats["inactive_file"] // cgroup v2
	if cache == 0 {
		cache = wire.Memory.Stats["total_inactive_file"] // cgroup v1
	}
	if cache <= wire.Memory.Usage {
		s.Memory = wire.Memory.Usage - cache
	}
	return s, nil
}
