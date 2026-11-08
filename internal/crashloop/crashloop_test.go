package crashloop_test

import (
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/crashloop"
)

func TestRisingRestartCountIsACrashLoopForAWhile(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	tr := &crashloop.Tracker{Now: func() time.Time { return now }}

	if tr.Observe("c", 3) {
		t.Error("a first reading can't show a loop")
	}
	now = now.Add(5 * time.Second)
	if tr.Observe("c", 3) {
		t.Error("an unchanged count isn't a loop")
	}
	now = now.Add(5 * time.Second)
	if !tr.Observe("c", 4) {
		t.Error("a rising count is a loop")
	}
	now = now.Add(4 * time.Minute)
	if !tr.Observe("c", 4) {
		t.Error("still within the window")
	}
	now = now.Add(2 * time.Minute)
	if tr.Observe("c", 4) {
		t.Error("no restart for over 5 minutes: settled")
	}
}
