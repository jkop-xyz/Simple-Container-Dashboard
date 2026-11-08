package engine

import (
	"context"
	"strings"
	"sync"
)

// Log is an append-only, line-oriented output buffer that several readers can follow
// while it is being written. It implements io.Writer.
type Log struct {
	mu      sync.Mutex
	lines   []string
	partial string
	closed  bool
	changed chan struct{}
}

func newLog() *Log { return &Log{changed: make(chan struct{})} }

// Write appends output, splitting it into lines.
func (l *Log) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	text := l.partial + string(p)
	parts := strings.Split(text, "\n")
	l.partial = parts[len(parts)-1]
	if len(parts) > 1 {
		l.lines = append(l.lines, parts[:len(parts)-1]...)
		l.notify()
	}
	return len(p), nil
}

// Printf-style convenience for engine messages.
func (l *Log) line(s string) { l.Write([]byte(s + "\n")) }

// Close flushes any partial line and wakes followers for the last time.
func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.partial != "" {
		l.lines = append(l.lines, l.partial)
		l.partial = ""
	}
	l.closed = true
	l.notify()
}

func (l *Log) notify() {
	close(l.changed)
	l.changed = make(chan struct{})
}

// Lines returns all complete lines so far.
func (l *Log) Lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// Next blocks until there are lines beyond offset or the log is closed, and returns
// them. done reports that the log is closed and fully read.
func (l *Log) Next(ctx context.Context, offset int) (lines []string, done bool, err error) {
	for {
		l.mu.Lock()
		if offset < len(l.lines) {
			out := append([]string(nil), l.lines[offset:]...)
			l.mu.Unlock()
			return out, false, nil
		}
		if l.closed {
			l.mu.Unlock()
			return nil, true, nil
		}
		ch := l.changed
		l.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}
}
