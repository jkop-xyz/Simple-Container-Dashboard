// Package state persists scd's runtime state (Stack selection, and later the applied
// commit, fingerprints and Apply history) as one JSON file in scd's data directory.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/jkop-xyz/Simple-Container-Dashboard/internal/stacks"
)

// State is the persisted state.
type State struct {
	// Selected lists the names of the Selected Stacks.
	Selected []string `json:"selected"`
	// Deployed records the Stacks Apply has deployed, keyed by Stack name. Only these
	// Stacks are ever redeployed or torn down.
	Deployed map[string]DeployedStack `json:"deployed,omitempty"`
	// History holds the most recent Applies, newest first, at most HistoryLimit.
	History []ApplyRecord `json:"history,omitempty"`
}

// HistoryLimit is how many Applies the history keeps.
const HistoryLimit = 20

// ApplyRecord is one finished Apply.
type ApplyRecord struct {
	From     string    `json:"from"`
	To       string    `json:"to"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	// Error is set when the Apply stopped before deploying anything.
	Error   string        `json:"error,omitempty"`
	Results []StackResult `json:"results"`
}

// StackResult is one Stack's outcome in an Apply.
type StackResult struct {
	Stack  string `json:"stack"`
	Action string `json:"action"`
	// Error is the failure text, or empty on success.
	Error string `json:"error,omitempty"`
}

// OK reports whether the Apply finished without any failure.
func (r ApplyRecord) OK() bool {
	if r.Error != "" {
		return false
	}
	for _, res := range r.Results {
		if res.Error != "" {
			return false
		}
	}
	return true
}

// Failures counts the failed Stacks.
func (r ApplyRecord) Failures() int {
	n := 0
	for _, res := range r.Results {
		if res.Error != "" {
			n++
		}
	}
	return n
}

// AddHistory records an Apply, keeping the newest HistoryLimit entries.
func (st *State) AddHistory(r ApplyRecord) {
	st.History = append([]ApplyRecord{r}, st.History...)
	if len(st.History) > HistoryLimit {
		st.History = st.History[:HistoryLimit]
	}
}

// DeployedStack is what Apply last did for one Stack.
type DeployedStack struct {
	Kind stacks.Kind `json:"kind"`
	// Tree is the git tree id of the Stack's directory at the deployed commit.
	Tree string `json:"tree"`
	// Failed marks a Stack whose last deploy failed; it is retried on the next Apply.
	Failed bool `json:"failed,omitempty"`
	// Secrets is the fingerprint of the Stack's secrets.env when it was deployed
	// (empty when there was none).
	Secrets string `json:"secrets,omitempty"`
}

// Store reads and writes the state file. Safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open returns a store for the state file at path. A missing file is an empty state.
func Open(path string) *Store { return &Store{path: path} }

// Get returns a copy of the current state.
func (s *Store) Get() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Update applies fn to the state and saves it atomically.
func (s *Store) Update(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		return err
	}
	fn(&st)
	return s.save(st)
}

// IsSelected reports whether the named Stack is selected.
func (st State) IsSelected(name string) bool { return slices.Contains(st.Selected, name) }

// SetSelected selects or unselects the named Stack.
func (st *State) SetSelected(name string, selected bool) {
	st.Selected = slices.DeleteFunc(st.Selected, func(s string) bool { return s == name })
	if selected {
		st.Selected = append(st.Selected, name)
		slices.Sort(st.Selected)
	}
}

func (s *Store) load() (State, error) {
	var st State
	b, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("state file %s: %w", s.path, err)
	}
	return st, nil
}

func (s *Store) save(st State) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
