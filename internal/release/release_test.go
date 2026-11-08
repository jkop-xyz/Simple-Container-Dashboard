package release_test

import (
	"context"
	"testing"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/release"
)

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v1.2.0", "v1.1.9", 1}, {"v1.10.0", "v1.9.0", 1}, {"1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v2.0.0", -1}, {"v1.0.0-rc1", "v1.0.0", 0}, {"dev", "v1.0.0", 0},
	} {
		if got := release.Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCheckerAsksAtMostOncePerDay(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	c := &release.Checker{
		Current: "v1.0.0",
		Latest:  func(context.Context) (string, error) { calls++; return "v1.1.0", nil },
		Now:     func() time.Time { return now },
	}
	if got := c.CheckNow(); got != "v1.1.0" || calls != 1 {
		t.Fatalf("CheckNow = %q after %d calls", got, calls)
	}
	now = now.Add(23 * time.Hour)
	if got := c.Newer(); got != "v1.1.0" || calls != 1 {
		t.Errorf("within a day: %q after %d calls", got, calls)
	}
}
