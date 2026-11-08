// Package crashloop spots containers that keep restarting, by remembering each
// container's restart count between readings.
package crashloop

import (
	"sync"
	"time"
)

// Window is how recently a restart must have been seen for a container to count as
// crash-looping.
const Window = 5 * time.Minute

type seen struct {
	count       int
	lastRestart time.Time
}

// Tracker remembers restart counts. Safe for concurrent use.
type Tracker struct {
	Now func() time.Time

	mu  sync.Mutex
	ids map[string]seen
}

// Observe records a container's restart count and reports whether it is crash-looping:
// its count went up within Window. The first reading of a container can't tell.
func (t *Tracker) Observe(id string, restarts int) bool {
	now := time.Now()
	if t.Now != nil {
		now = t.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ids == nil {
		t.ids = map[string]seen{}
	}
	s, ok := t.ids[id]
	if ok && restarts > s.count {
		s.lastRestart = now
	}
	s.count = restarts
	t.ids[id] = s
	return !s.lastRestart.IsZero() && now.Sub(s.lastRestart) <= Window
}

// Forget drops containers not in keep.
func (t *Tracker) Forget(keep map[string]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.ids {
		if !keep[id] {
			delete(t.ids, id)
		}
	}
}
