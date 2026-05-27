package pipeline

import (
	"context"
	"fmt"
	"os"
)

// Run executes all pending stages of r.State in order, persisting state.json
// after each stage. Halts on the first stage that returns an error.
func Run(ctx context.Context, r *Runner) error {
	if err := AcquireLock(r.ProjectPath, r.State.RunID); err != nil {
		return err
	}
	defer func() { _ = ReleaseLock(r.ProjectPath) }()

	if err := os.MkdirAll(r.StateDir, 0o755); err != nil {
		return err
	}
	if err := SaveState(r.StateDir, r.State); err != nil {
		return err
	}

	for i := r.State.CurrentStage - 1; i < len(r.State.Stages); i++ {
		s := &r.State.Stages[i]
		r.State.CurrentStage = s.ID
		if s.Status == StageCompleted {
			continue
		}
		fmt.Fprintf(os.Stderr, "[styx] stage %d/%d %s...\n", s.ID, len(r.State.Stages), s.Name)
		fn := stageDispatch(s.ID)
		if fn == nil {
			r.State.Status = StatusFailed
			_ = SaveState(r.StateDir, r.State)
			return fmt.Errorf("no dispatcher for stage %d", s.ID)
		}
		if err := fn(ctx, r, s); err != nil {
			r.State.Status = StatusFailed
			_ = SaveState(r.StateDir, r.State)
			return fmt.Errorf("stage %d (%s): %w", s.ID, s.Name, err)
		}
		if err := SaveState(r.StateDir, r.State); err != nil {
			return err
		}
	}
	r.State.Status = StatusCompleted
	return SaveState(r.StateDir, r.State)
}

// Resume picks up where a prior Run left off, skipping completed stages and
// re-running the current one (which may have been "running" when it was paused).
func Resume(ctx context.Context, r *Runner) error {
	loaded, err := LoadState(r.StateDir)
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	r.State = loaded
	// Reset CurrentStage to first non-completed stage.
	for i, s := range r.State.Stages {
		if s.Status != StageCompleted {
			r.State.CurrentStage = s.ID
			if s.Status == StageFailed || s.Status == StageRunning {
				r.State.Stages[i].Status = StagePending
			}
			break
		}
	}
	r.State.Status = StatusRunning
	return Run(ctx, r)
}
