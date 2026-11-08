// Package release checks GitHub Releases for a newer scd, at most once a day.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Repo is where scd is released.
const Repo = "jkop-xyz/Simple-Container-Dashboard"

// Latest returns the newest release's tag, e.g. "v1.2.0".
type Latest func(ctx context.Context) (string, error)

// GitHubLatest asks the GitHub API for the latest release. It sends a generic
// User-Agent and nothing else about the host.
func GitHubLatest(version string) Latest {
	client := &http.Client{Timeout: 10 * time.Second}
	return func(ctx context.Context) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+Repo+"/releases/latest", nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "scd/"+version)
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("GitHub releases: %s", resp.Status)
		}
		var body struct {
			Tag string `json:"tag_name"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return "", err
		}
		return body.Tag, nil
	}
}

// Checker caches the latest release for a day. Failures are silent: the hint just
// doesn't show.
type Checker struct {
	Current string
	Latest  Latest
	Every   time.Duration
	Now     func() time.Time

	mu      sync.Mutex
	checked time.Time
	newer   string
	running bool
}

// Newer returns the newer release's tag, or "" when scd is up to date, unknown, or a
// development build. It never blocks on the network: a due check runs in the background.
func (c *Checker) Newer() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.running && (c.checked.IsZero() || now.Sub(c.checked) >= c.every()) {
		c.running = true
		c.checked = now
		go c.check()
	}
	return c.newer
}

// CheckNow runs a check synchronously (used at startup and in tests).
func (c *Checker) CheckNow() string {
	c.mu.Lock()
	c.checked = c.now()
	c.mu.Unlock()
	c.check()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.newer
}

func (c *Checker) check() {
	defer func() {
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
	}()
	if c.Latest == nil || !isRelease(c.Current) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tag, err := c.Latest(ctx)
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if Compare(tag, c.Current) > 0 {
		c.newer = tag
	} else {
		c.newer = ""
	}
}

func (c *Checker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Checker) every() time.Duration {
	if c.Every > 0 {
		return c.Every
	}
	return 24 * time.Hour
}

func isRelease(v string) bool {
	_, ok := parse(v)
	return ok
}

// Compare compares two versions like "v1.2.3": -1, 0 or 1. Unparseable versions compare
// as equal, so they never trigger a hint.
func Compare(a, b string) int {
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA || !okB {
		return 0
	}
	for i := range pa {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	v, _, _ = strings.Cut(v, "-") // ignore pre-release suffixes
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
