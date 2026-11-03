// Package state keeps what GitHub does not: the agent session, the branch and the time up
// to which the owner's notes have been passed to the agent. One JSON file on the volume.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Status string

// Statuses mirror the issue labels agent:<status>.
const (
	Working Status = "working"
	Review  Status = "pr"
	Blocked Status = "blocked"
	Paused  Status = "paused"
	Failed  Status = "failed"
	Done    Status = "done"
)

type Task struct {
	Repo    string `json:"repo"`
	Issue   int    `json:"issue"`
	Title   string `json:"title"`
	PR      int    `json:"pr,omitempty"`
	Agent   string `json:"agent"`
	Session string `json:"session,omitempty"`
	Branch  string `json:"branch"`
	Status  Status `json:"status"`
	// Updated is the time of the last status change.
	Updated time.Time `json:"updated"`
	// PauseNoted is set once the issue was told about a limit, until a run gets through.
	PauseNoted bool `json:"pause_noted,omitempty"`
	// Since is the creation time of the newest owner note already given to the agent.
	Since time.Time `json:"since"`
}

type Store struct {
	path  string
	Tasks map[string]*Task `json:"tasks"`
	// Paused holds, per agent, the time until which its plan is used up.
	Paused map[string]time.Time `json:"paused,omitempty"`
	// StartDay is the UTC date (2006-01-02) that Started counts new issues for.
	StartDay string `json:"start_day,omitempty"`
	Started  int    `json:"started,omitempty"`
}

func Key(repo string, issue int) string {
	return fmt.Sprintf("%s#%d", repo, issue)
}

// Load reads the file; a missing file is an empty store.
func Load(path string) (*Store, error) {
	s := &Store{path: path, Tasks: map[string]*Task{}, Paused: map[string]time.Time{}}
	raw, err := os.ReadFile(path) //nolint:gosec // the path comes from the operator's config
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("state %s: %w", path, err)
	}
	if s.Paused == nil {
		s.Paused = map[string]time.Time{}
	}
	return s, nil
}

// Save writes through a temporary file so a crash never leaves half a file.
func (s *Store) Save() error {
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o750); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}
