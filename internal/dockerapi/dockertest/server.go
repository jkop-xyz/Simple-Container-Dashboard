// Package dockertest provides a fake Docker-compatible API server on a Unix socket,
// so tests can exercise scd without a real Docker or Podman.
package dockertest

import (
	"encoding/binary"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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
	// Logs is the container's output so far; Tty makes the fake send it unframed.
	Logs []string
	Tty  bool
	// ExitCode, OOMKilled and FinishedAt describe the last stop; HealthOutput is the
	// latest failing healthcheck's output.
	ExitCode     int
	OOMKilled    bool
	FinishedAt   time.Time
	HealthOutput string
}

// Host is what the fake's /info reports.
var Host = struct {
	MemTotal uint64
	NCPU     int
}{MemTotal: 16 << 30, NCPU: 4}

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
	feeds      map[string]*feed
	close      func()
}

// feed is one container's log output, which followers wait on.
type feed struct {
	lines   []string
	ended   bool
	changed chan struct{}
}

// AppendLogs adds output to a container's log, waking anyone following it.
func (s *Server) AppendLogs(id string, lines ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.feed(id)
	f.lines = append(f.lines, lines...)
	close(f.changed)
	f.changed = make(chan struct{})
}

// EndLogs makes followers of a container's log see it stop, as when it exits.
func (s *Server) EndLogs(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.feed(id)
	f.ended = true
	close(f.changed)
	f.changed = make(chan struct{})
}

// feed returns the container's log feed. Call with s.mu held.
func (s *Server) feed(id string) *feed {
	if s.feeds == nil {
		s.feeds = map[string]*feed{}
	}
	f := s.feeds[id]
	if f == nil {
		f = &feed{changed: make(chan struct{})}
		s.feeds[id] = f
	}
	return f
}

// versionPrefix matches the optional API version segment, e.g. "/v1.41".
var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

// New starts a fake API server and stops it when the test ends.
func New(t testing.TB) *Server {
	t.Helper()
	s, err := Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// Start starts a fake API server outside of a test (e.g. for demo mode). Call Close to
// stop it.
func Start() (*Server, error) {
	// Unix socket paths are limited to ~108 bytes, so use a short temp dir.
	dir, err := os.MkdirTemp("", "scd-dockertest-")
	if err != nil {
		return nil, err
	}
	s := &Server{Socket: filepath.Join(dir, "api.sock")}
	ln, err := net.Listen("unix", s.Socket)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.serve)}
	go srv.Serve(ln)
	s.close = func() {
		srv.Close()
		os.RemoveAll(dir)
	}
	return s, nil
}

// Close stops the server and removes its socket.
func (s *Server) Close() {
	if s.close != nil {
		s.close()
	}
}

// Update changes the containers the fake reports, under its lock.
func (s *Server) Update(fn func([]Container) []Container) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.containers = fn(append([]Container(nil), s.containers...))
}

// Containers returns a copy of the containers the fake reports.
func (s *Server) Containers() []Container {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Container(nil), s.containers...)
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
	for _, c := range cs {
		if len(c.Logs) > 0 {
			s.feed(c.ID).lines = append([]string(nil), c.Logs...)
		}
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	id, sub, _ := strings.Cut(strings.TrimPrefix(path, "/containers/"), "/")
	switch {
	case r.Method == http.MethodGet && path == "/info":
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"MemTotal": Host.MemTotal, "NCPU": Host.NCPU})
	case r.Method == http.MethodGet && path == "/containers/json":
		s.listContainers(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && sub == "json":
		s.withContainer(w, id, s.inspect)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && sub == "stats":
		s.withContainer(w, id, s.stats)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && sub == "logs":
		s.withContainer(w, id, func(w http.ResponseWriter, c Container) { s.logs(w, r, c) })
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
	st := map[string]any{"Status": c.State, "ExitCode": c.ExitCode, "OOMKilled": c.OOMKilled,
		"FinishedAt": "0001-01-01T00:00:00Z"}
	if !c.FinishedAt.IsZero() {
		st["FinishedAt"] = c.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	if c.Health != "" {
		h := map[string]any{"Status": c.Health}
		if c.HealthOutput != "" {
			h["Log"] = []map[string]any{{"ExitCode": 1, "Output": c.HealthOutput}}
		}
		st["Health"] = h
	}
	json.NewEncoder(w).Encode(map[string]any{"Id": c.ID, "RestartCount": c.RestartCount, "State": st,
		"Config": map[string]any{"Tty": c.Tty}})
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

// logs serves tail lines, then (with follow) new lines until EndLogs or the client goes
// away. Non-TTY output is framed like the real API, each line split over two frames.
func (s *Server) logs(w http.ResponseWriter, r *http.Request, c Container) {
	w.Header().Set("Content-Type", "application/vnd.docker.raw-stream")
	flusher, _ := w.(http.Flusher)
	stamp := r.URL.Query().Get("timestamps") == "true"
	write := func(line string) {
		if stamp {
			line = "2026-11-09T12:00:00.000000000Z " + line
		}
		data := []byte(line + "\n")
		if c.Tty {
			w.Write(data)
			return
		}
		half := len(data) / 2
		for _, chunk := range [][]byte{data[:half], data[half:]} {
			var hdr [8]byte
			hdr[0] = 1
			binary.BigEndian.PutUint32(hdr[4:], uint32(len(chunk)))
			w.Write(hdr[:])
			w.Write(chunk)
		}
	}

	s.mu.Lock()
	f := s.feed(c.ID)
	lines := f.lines
	if n, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && n < len(lines) {
		lines = lines[len(lines)-n:]
	}
	sent := len(f.lines)
	s.mu.Unlock()
	for _, l := range lines {
		write(l)
	}
	if flusher != nil {
		flusher.Flush()
	}
	if r.URL.Query().Get("follow") != "true" {
		return
	}
	for {
		s.mu.Lock()
		f := s.feed(c.ID)
		next, ended, ch := f.lines[sent:], f.ended, f.changed
		sent = len(f.lines)
		s.mu.Unlock()
		for _, l := range next {
			write(l)
		}
		if flusher != nil {
			flusher.Flush()
		}
		if ended {
			return
		}
		select {
		case <-ch:
		case <-r.Context().Done():
			return
		}
	}
}
