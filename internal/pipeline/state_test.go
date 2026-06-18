package pipeline

import (
	"context"
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

func TestLock_AcquireAndRelease(t *testing.T) {
	proj := t.TempDir()
	if err := AcquireLock(proj, "run-A"); err != nil {
		t.Fatal(err)
	}
	// Second acquire while held should fail.
	if err := AcquireLock(proj, "run-B"); err == nil {
		t.Error("expected second AcquireLock to fail while lock held")
	}
	if err := ReleaseLock(proj); err != nil {
		t.Fatal(err)
	}
	// After release, can acquire again.
	if err := AcquireLock(proj, "run-C"); err != nil {
		t.Errorf("expected AcquireLock to succeed after release: %v", err)
	}
	_ = ReleaseLock(proj)
}

func TestLock_ReadHolder(t *testing.T) {
	proj := t.TempDir()
	if err := AcquireLock(proj, "run-X"); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLockHolder(proj)
	if err != nil {
		t.Fatal(err)
	}
	if got != "run-X" {
		t.Errorf("holder = %q, want run-X", got)
	}
	_ = ReleaseLock(proj)
}

func newStubRunner(t *testing.T, projectPath, goal string) *Runner {
	t.Helper()
	runID := NewRunID(goal)
	st := NewState(runID, goal)
	return &Runner{
		State:        st,
		StateDir:     RunDir(projectPath, runID),
		ProjectPath:  projectPath,
		Goal:         goal,
		RunResearch:  func(ctx context.Context, r *Runner) (string, error) { return "brief.md", nil },
		EnsureIntel:  func(ctx context.Context, r *Runner) (bool, string, error) { return true, "fresh", nil },
		RunPlan:      func(ctx context.Context, r *Runner) (string, error) { return "plan.md", nil },
		RunExecute:   func(ctx context.Context, r *Runner) ([]string, error) { return []string{"abc1234"}, nil },
		RunTest:      func(ctx context.Context, r *Runner) (bool, string, error) { return true, "", nil },
		RunFixTests:  func(ctx context.Context, r *Runner, out string, n int) error { return nil },
		RunReview:    func(ctx context.Context, r *Runner) (int, int, string, error) { return 0, 0, "clean", nil },
		RunFixReview: func(ctx context.Context, r *Runner, fnd string, n int) error { return nil },
		RunShip: func(ctx context.Context, r *Runner) (string, bool, error) {
			return "https://example.com/pr/1", true, nil
		},
	}
}

func TestRun_HappyPathProgressesAllStages(t *testing.T) {
	proj := t.TempDir()
	r := newStubRunner(t, proj, "add streaming")
	if err := Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.State.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", r.State.Status)
	}
	for _, s := range r.State.Stages {
		if s.Status != StageCompleted {
			t.Errorf("stage %d (%s) status = %q, want completed", s.ID, s.Name, s.Status)
		}
	}
}

func TestRun_FailureHalts(t *testing.T) {
	proj := t.TempDir()
	r := newStubRunner(t, proj, "broken")
	r.RunExecute = func(ctx context.Context, r *Runner) ([]string, error) {
		return nil, errFromString("claude no edits")
	}
	err := Run(context.Background(), r)
	if err == nil {
		t.Fatal("expected error from execute stage")
	}
	if r.State.Status != StatusFailed {
		t.Errorf("Status = %q, want failed", r.State.Status)
	}
}

func TestResume_PicksUpWhereLeftOff(t *testing.T) {
	proj := t.TempDir()
	r := newStubRunner(t, proj, "resume me")
	// Pretend stages 1-3 already completed in a prior run.
	for i := 0; i < 3; i++ {
		r.State.Stages[i].Status = StageCompleted
	}
	r.State.CurrentStage = 4
	if err := SaveState(r.StateDir, r.State); err != nil {
		t.Fatal(err)
	}
	if err := Resume(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.State.Status != StatusCompleted {
		t.Errorf("Status = %q, want completed", r.State.Status)
	}
	for _, s := range r.State.Stages {
		if s.Status != StageCompleted {
			t.Errorf("stage %d status = %q after resume", s.ID, s.Status)
		}
	}
}

func TestLoadStateVersionlessStillLoads(t *testing.T) {
	dir := t.TempDir()
	// A pre-version state.json (no "version" key) must load without error.
	legacy := `{"run_id":"20260101-000000-x","goal":"g","status":"running","current_stage":2,"branch":"styx/x","stages":[{"id":1,"name":"research","status":"completed"},{"id":2,"name":"intel","status":"running"}]}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState legacy: %v", err)
	}
	if s.RunID != "20260101-000000-x" || s.CurrentStage != 2 {
		t.Errorf("legacy state misread: %+v", s)
	}
	if s.Version != StateVersion {
		t.Errorf("Version not normalized: got %d want %d", s.Version, StateVersion)
	}
}
