// Package dockertest provides a fake Docker-compatible API server on a Unix socket,
// so tests can exercise scd without a real Docker or Podman.
package dockertest

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
)

// Container is a container as the fake API reports it, in the API's own field shape.
type Container struct {
	ID     string
	Name   string
	Image  string
	State  string
	Status string
	Labels map[string]string
}

// Server is a fake runtime API. Safe for concurrent use.
type Server struct {
	// Socket is the Unix socket path to point clients at.
	Socket string

	mu         sync.Mutex
	containers []Container
}

// versionPrefix matches the optional API version segment, e.g. "/v1.41".
var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

// New starts a fake API server and stops it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	// Unix socket paths are limited to ~108 bytes, so use a short temp dir.
	dir, err := os.MkdirTemp("", "scd-dockertest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	s := &Server{Socket: filepath.Join(dir, "api.sock")}
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.serve)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return s
}

// SetContainers replaces the containers the fake reports.
func (s *Server) SetContainers(cs ...Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.containers = append([]Container(nil), cs...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	switch {
	case r.Method == http.MethodGet && path == "/containers/json":
		s.listContainers(w, r)
	default:
		http.Error(w, `{"message":"not implemented by fake"}`, http.StatusNotFound)
	}
}

func (s *Server) listContainers(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := r.URL.Query().Get("all")
	type wire struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		State  string            `json:"State"`
		Status string            `json:"Status"`
		Labels map[string]string `json:"Labels"`
	}
	out := []wire{}
	for _, c := range s.containers {
		if c.State != "running" && all != "true" && all != "1" {
			continue
		}
		out = append(out, wire{
			ID: c.ID, Names: []string{"/" + c.Name}, Image: c.Image,
			State: c.State, Status: c.Status, Labels: c.Labels,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}
