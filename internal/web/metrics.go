package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/view"
)

// History sizes: the host charts keep 30 minutes at the default 15 s cadence; each
// container's sparkline keeps its last 24 readings.
const (
	hostPoints  = 120
	sparkPoints = 24
)

// Point is one reading of a host total.
type Point struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// metrics holds the sampled history behind the charts. Safe for concurrent use.
type metrics struct {
	mu   sync.Mutex
	cpu  []Point // host CPU, in cores
	mem  []Point // host memory, in bytes
	cpuC map[string][]float64
	memC map[string]uint64
}

// record adds one sample of every running container.
func (m *metrics) record(now time.Time, cs []view.Container) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cpuC == nil {
		m.cpuC, m.memC = map[string][]float64{}, map[string]uint64{}
	}
	var cores, mem float64
	seen := map[string]bool{}
	for _, c := range cs {
		if c.State != "running" {
			continue
		}
		seen[c.ID] = true
		m.memC[c.ID] = c.Memory
		mem += float64(c.Memory)
		if c.HasCPU {
			cores += c.CPUPercent / 100
			m.cpuC[c.ID] = appendCapped(m.cpuC[c.ID], c.CPUPercent/100, sparkPoints)
		}
	}
	for id := range m.cpuC {
		if !seen[id] {
			delete(m.cpuC, id)
			delete(m.memC, id)
		}
	}
	m.cpu = appendCapped(m.cpu, Point{now, cores}, hostPoints)
	m.mem = appendCapped(m.mem, Point{now, mem}, hostPoints)
}

func appendCapped[T any](s []T, v T, n int) []T {
	s = append(s, v)
	if len(s) > n {
		s = append(s[:0:0], s[len(s)-n:]...)
	}
	return s
}

// spark returns a container's recent CPU readings, in cores.
func (m *metrics) spark(id string) []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]float64(nil), m.cpuC[id]...)
}

// host returns copies of the host series.
func (m *metrics) host() (cpu, mem []Point) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Point(nil), m.cpu...), append([]Point(nil), m.mem...)
}

// Sample keeps the charts' history: it reads every container's usage every `every`
// until ctx ends. It is opt-in (see Deps.SampleEvery) so tests stay quiet.
func (s *Server) Sample(ctx context.Context, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, every)
		cs, err := s.containers(cctx)
		cancel()
		if err == nil {
			s.metrics.record(time.Now(), cs)
		} else if ctx.Err() == nil {
			slog.Debug("sampling metrics", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// metricsJSON serves the host charts' series.
func (s *Server) metricsJSON(w http.ResponseWriter, r *http.Request) {
	cpu, mem := s.metrics.host()
	if cpu == nil {
		cpu, mem = []Point{}, []Point{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]any{"cpu": cpu, "memory": mem})
}

// sparkPath draws a container's CPU history as an SVG area path in a 60×18 box, scaled
// to its own peak (at least 0.05 cores, so idle containers stay flat).
func sparkPath(vals []float64) string {
	if len(vals) < 2 {
		return ""
	}
	const w, h = 60.0, 18.0
	peak := 0.05
	for _, v := range vals {
		peak = max(peak, v)
	}
	var b strings.Builder
	step := w / float64(sparkPoints-1)
	x0 := w - step*float64(len(vals)-1)
	fmt.Fprintf(&b, "M%.1f %.1f", x0, h)
	for i, v := range vals {
		fmt.Fprintf(&b, " L%.1f %.1f", x0+step*float64(i), h-(v/peak)*(h-1))
	}
	fmt.Fprintf(&b, " L%.1f %.1f Z", w, h)
	return b.String()
}
