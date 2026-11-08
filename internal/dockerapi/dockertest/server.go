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
	"strings"
	"sync"
	"testing"
)

// Container is a container as the fake API reports it.
type Container struct {
	ID     string
	Name   string
	Image  string
	State  string
	Status string
	Labels map[string]string
	// Health is the healthcheck status ("" means no healthcheck).
	Health       string
	RestartCount int
	Stats        Stats
	Ports        []Port
}

// Port is a port binding in the API's shape.
type Port struct {
	IP          string
	PrivatePort int
	PublicPort  int
	Type        string
}

// Stats is the resource-usage reading the fake reports for a container.
type Stats struct {
	CPUTotal, SystemCPU       uint64
	PreCPUTotal, PreSystemCPU uint64
	OnlineCPUs                uint32
	MemoryUsage               uint64
	InactiveFile              uint64
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

// SetStats replaces one container's stats reading.
func (s *Server) SetStats(id string, st Stats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.containers {
		if s.containers[i].ID == id {
			s.containers[i].Stats = st
		}
	}
}

// SetContainers replaces the containers the fake reports.
func (s *Server) SetContainers(cs ...Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.containers = append([]Container(nil), cs...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	id, sub, _ := strings.Cut(strings.TrimPrefix(path, "/containers/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/containers/json":
		s.listContainers(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && sub == "json":
		s.withContainer(w, id, s.inspect)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && sub == "stats":
		s.withContainer(w, id, s.stats)
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
		Ports  []Port            `json:"Ports"`
	}
	out := []wire{}
	for _, c := range s.containers {
		if c.State != "running" && all != "true" && all != "1" {
			continue
		}
		out = append(out, wire{
			ID: c.ID, Names: []string{"/" + c.Name}, Image: c.Image,
			State: c.State, Status: c.Status, Labels: c.Labels, Ports: append([]Port{}, c.Ports...),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) withContainer(w http.ResponseWriter, id string, fn func(http.ResponseWriter, Container)) {
	s.mu.Lock()
	var found *Container
	for i := range s.containers {
		if s.containers[i].ID == id {
			c := s.containers[i]
			found = &c
		}
	}
	s.mu.Unlock()
	if found == nil {
		http.Error(w, `{"message":"No such container: `+id+`"}`, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fn(w, *found)
}

func (s *Server) inspect(w http.ResponseWriter, c Container) {
	out := map[string]any{"Id": c.ID, "RestartCount": c.RestartCount, "State": map[string]any{"Status": c.State}}
	if c.Health != "" {
		out["State"].(map[string]any)["Health"] = map[string]any{"Status": c.Health}
	}
	json.NewEncoder(w).Encode(out)
}

func (s *Server) stats(w http.ResponseWriter, c Container) {
	st := c.Stats
	cpu := func(total, system uint64) map[string]any {
		return map[string]any{
			"cpu_usage":        map[string]any{"total_usage": total},
			"system_cpu_usage": system,
			"online_cpus":      st.OnlineCPUs,
		}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"cpu_stats":    cpu(st.CPUTotal, st.SystemCPU),
		"precpu_stats": cpu(st.PreCPUTotal, st.PreSystemCPU),
		"memory_stats": map[string]any{
			"usage": st.MemoryUsage,
			"stats": map[string]any{"inactive_file": st.InactiveFile},
		},
	})
}
