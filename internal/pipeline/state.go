// Package pipeline orchestrates the full styx auto <goal> flow:
// research -> intel -> plan -> execute -> test -> review -> ship.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Status values for a run.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusPaused    = "paused"
	StatusFailed    = "failed"
)

// Stage status values.
const (
	StagePending   = "pending"
	StageRunning   = "running"
	StageCompleted = "completed"
	StageFailed    = "failed"
	StageSkipped   = "skipped"
)

// State is the per-run record persisted at <project>/.styx/runs/<run-id>/state.json
type State struct {
	RunID        string    `json:"run_id"`
	Goal         string    `json:"goal"`
	StartedAt    time.Time `json:"started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	Status       string    `json:"status"`
	CurrentStage int       `json:"current_stage"`
	Branch       string    `json:"branch"`
	Stages       []Stage   `json:"stages"`
	Failures     []string  `json:"failures,omitempty"`
}

// Stage tracks one step of the pipeline.
type Stage struct {
	ID            int       `json:"id"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	EndedAt       time.Time `json:"ended_at,omitempty"`
	Artifact      string    `json:"artifact,omitempty"`
	Commits       []string  `json:"commits,omitempty"`
	Attempts      int       `json:"attempts,omitempty"`
	SkippedReason string    `json:"skipped_reason,omitempty"`
}

// NewRunID returns a fresh run id: YYYYMMDD-HHMMSS-<goal-slug>.
func NewRunID(goal string) string {
	stamp := time.Now().UTC().Format("20060102-150405")
	return stamp + "-" + slug(goal)
}

// RunDir is <project>/.styx/runs/<run-id>
func RunDir(projectPath, runID string) string {
	return filepath.Join(projectPath, ".styx", "runs", runID)
}

// SaveState atomically writes state.json to dir.
func SaveState(dir string, s *State) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	s.UpdatedAt = time.Now().UTC()
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "state.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// LoadState reads state.json from dir.
func LoadState(dir string) (*State, error) {
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state: %w", err)
	}
	return &s, nil
}

// NewState returns an empty State with the standard 7-stage scaffold.
func NewState(runID, goal string) *State {
	return &State{
		RunID:     runID,
		Goal:      goal,
		StartedAt: time.Now().UTC(),
		Status:    StatusRunning,
		Branch:    "styx/" + runID,
		Stages: []Stage{
			{ID: 1, Name: "research", Status: StagePending},
			{ID: 2, Name: "intel", Status: StagePending},
			{ID: 3, Name: "plan", Status: StagePending},
			{ID: 4, Name: "execute", Status: StagePending},
			{ID: 5, Name: "test", Status: StagePending},
			{ID: 6, Name: "review", Status: StagePending},
			{ID: 7, Name: "ship", Status: StagePending},
		},
		CurrentStage: 1,
	}
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = strings.ToLower(s)
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" {
		s = "run"
	}
	return s
}
