// Package systemd reads the user's systemd units and journal through the command
// runner (`systemctl --user`, `journalctl --user`). It is read-only.
package systemd

import (
	"context"
	"io"
	"strconv"
	"strings"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/runner"
)

// UnitState is a unit's systemd state.
type UnitState struct {
	Name string
	// LoadState is e.g. "loaded" or "not-found".
	LoadState string
	// ActiveState is e.g. "active", "inactive", "failed", "activating".
	ActiveState string
	SubState    string
}

// User reads the calling user's systemd instance.
type User struct {
	Runner runner.Runner
}

// States returns the state of each unit, keyed by name.
func (u User) States(ctx context.Context, units []string) (map[string]UnitState, error) {
	out := map[string]UnitState{}
	if len(units) == 0 {
		return out, nil
	}
	args := append([]string{"--user", "show", "--property=Id,LoadState,ActiveState,SubState", "--"}, units...)
	res, err := u.Runner.Run(ctx, runner.Cmd{Name: "systemctl", Args: args})
	if err != nil {
		return nil, err
	}
	// One block of KEY=value lines per unit, separated by blank lines, in argument order.
	blocks := strings.Split(strings.TrimSpace(string(res.Output)), "\n\n")
	for i, block := range blocks {
		st := UnitState{}
		for _, line := range strings.Split(block, "\n") {
			k, v, _ := strings.Cut(line, "=")
			switch k {
			case "Id":
				st.Name = v
			case "LoadState":
				st.LoadState = v
			case "ActiveState":
				st.ActiveState = v
			case "SubState":
				st.SubState = v
			}
		}
		if i < len(units) {
			// A unit that doesn't exist reports Id as the name we asked for anyway, but
			// key by the requested name to be safe.
			st.Name = units[i]
		}
		out[st.Name] = st
	}
	return out, nil
}

// IsActive reports a unit's active state (e.g. "active", "inactive").
func (u User) IsActive(ctx context.Context, unit string) string {
	res, _ := u.Runner.Run(ctx, runner.Cmd{Name: "systemctl", Args: []string{"--user", "is-active", "--", unit}})
	return strings.TrimSpace(string(res.Output))
}

// Journal writes the unit's last tail journal lines to out and, with follow, keeps
// writing new ones until ctx is cancelled.
func (u User) Journal(ctx context.Context, unit string, tail int, follow bool, out io.Writer) error {
	args := []string{"--user", "--unit", unit, "--lines", strconv.Itoa(tail), "--no-pager", "--output", "short-iso"}
	if follow {
		args = append(args, "--follow")
	}
	_, err := u.Runner.Run(ctx, runner.Cmd{Name: "journalctl", Args: args, Output: out})
	if ctx.Err() != nil {
		return nil
	}
	return err
}
