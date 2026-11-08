package engine

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MaxOutput caps how much of one Apply's output is kept; the end is kept.
const MaxOutput = 256 << 10

// validID guards output file names.
var validID = regexp.MustCompile(`^[0-9A-Za-z-]{1,64}$`)

// NewApplyID names an Apply by when it started and the commit it went to.
func NewApplyID(started time.Time, to string) string {
	if len(to) > 7 {
		to = to[:7]
	}
	return started.UTC().Format("20060102T150405Z") + "-" + to
}

// Outputs keeps each Apply's output as a file in a directory.
type Outputs struct{ Dir string }

func (o Outputs) path(id string) (string, error) {
	if !validID.MatchString(id) {
		return "", fmt.Errorf("invalid Apply id %q", id)
	}
	return filepath.Join(o.Dir, id+".log"), nil
}

// Save writes an Apply's output, keeping at most MaxOutput bytes from its end.
func (o Outputs) Save(id string, lines []string) error {
	p, err := o.path(id)
	if err != nil {
		return err
	}
	text := strings.Join(lines, "\n") + "\n"
	if len(text) > MaxOutput {
		cut := text[len(text)-MaxOutput:]
		if i := strings.IndexByte(cut, '\n'); i >= 0 {
			cut = cut[i+1:]
		}
		text = "… (earlier output trimmed)\n" + cut
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(text), 0o600)
}

// Load returns an Apply's kept output; ok is false when none was kept.
func (o Outputs) Load(id string) (lines []string, ok bool, err error) {
	p, err := o.path(id)
	if err != nil {
		return nil, false, nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n"), true, nil
}

// Remove deletes an Apply's output, if any.
func (o Outputs) Remove(id string) {
	if p, err := o.path(id); err == nil {
		os.Remove(p)
	}
}
