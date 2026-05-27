package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &State{
		RunID:        "20260527-abc",
		Goal:         "add streaming",
		StartedAt:    time.Now().UTC().Truncate(time.Second),
		Status:       StatusRunning,
		CurrentStage: 3,
		Branch:       "styx/20260527-abc",
		Stages: []Stage{
			{ID: 1, Name: "research", Status: StageCompleted, Artifact: "brief.md"},
			{ID: 2, Name: "intel", Status: StageCompleted, SkippedReason: "fresh"},
			{ID: 3, Name: "plan", Status: StageRunning},
		},
	}
	if err := SaveState(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != s.RunID || got.Goal != s.Goal {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, s)
	}
	if got.CurrentStage != 3 {
		t.Errorf("CurrentStage = %d, want 3", got.CurrentStage)
	}
	if len(got.Stages) != 3 {
		t.Errorf("Stages len = %d, want 3", len(got.Stages))
	}
}

func TestLoadState_MissingFile(t *testing.T) {
	_, err := LoadState(t.TempDir())
	if err == nil {
		t.Error("expected error for missing state.json")
	}
}

func TestNewRunID_IsTimestamped(t *testing.T) {
	id1 := NewRunID("add streaming")
	if len(id1) < 10 {
		t.Errorf("run id too short: %q", id1)
	}
}

func TestRunDir_RootsUnderStyxRuns(t *testing.T) {
	projPath := t.TempDir()
	d := RunDir(projPath, "abc-123")
	want := filepath.Join(projPath, ".styx", "runs", "abc-123")
	if d != want {
		t.Errorf("RunDir = %q, want %q", d, want)
	}
	// Should be safe to create.
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
}
