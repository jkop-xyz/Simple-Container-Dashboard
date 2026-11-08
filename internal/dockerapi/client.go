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
