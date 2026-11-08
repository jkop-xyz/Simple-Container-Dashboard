// Package usage turns successive runtime samples into CPU % and memory figures.
package usage

import (
	"sync"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/dockerapi"
)

// Usage is one container's current resource use.
type Usage struct {
	// CPUPercent is meaningful only when HasCPU is set (it needs two readings).
	CPUPercent float64
	HasCPU     bool
	Memory     uint64
}

// Collector remembers each container's previous sample. Safe for concurrent use.
type Collector struct {
	mu   sync.Mutex
	prev map[string]dockerapi.Sample
}

// Update records a new sample and returns the usage since the previous one.
func (c *Collector) Update(id string, s dockerapi.Sample) Usage {
	c.mu.Lock()
	if c.prev == nil {
		c.prev = map[string]dockerapi.Sample{}
	}
	prev, ok := c.prev[id]
	c.prev[id] = s
	c.mu.Unlock()

	u := Usage{Memory: s.Memory}
	prevTotal, prevSystem := s.PreCPUTotal, s.PreSystemCPU
	if ok && s.SystemCPU > prev.SystemCPU {
		prevTotal, prevSystem = prev.CPUTotal, prev.SystemCPU
	}
	if prevSystem > 0 && s.SystemCPU > prevSystem && s.CPUTotal >= prevTotal {
		cpus := float64(s.OnlineCPUs)
		if cpus == 0 {
			cpus = 1
		}
		u.CPUPercent = float64(s.CPUTotal-prevTotal) / float64(s.SystemCPU-prevSystem) * cpus * 100
		u.HasCPU = true
	}
	return u
}

// Forget drops samples for containers not in keep, so the collector doesn't grow.
func (c *Collector) Forget(keep map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.prev {
		if !keep[id] {
			delete(c.prev, id)
		}
	}
}
