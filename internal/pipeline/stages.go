package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/ishaanbatra/styx/internal/progress"
)

// StageFunc runs one stage. The runner mutates `stage` (status, attempts,
// artifact, commits) and returns an error to halt the pipeline. Returning
// nil means proceed to next stage.
type StageFunc func(ctx context.Context, p *Runner, stage *Stage) error

// Runner holds everything a stage needs.
type Runner struct {
	State       *State // shared mutable state for this run
	StateDir    string // <project>/.styx/runs/<run-id>
	ProjectPath string // absolute project path
	Goal        string // raw goal text
	Deep        bool   // --deep flag (only affects research stage)
	NoPR        bool   // --no-pr flag
	NoPush      bool   // --no-push flag

	Prog *progress.Tracker // progress tracker; nil → Quiet

	// Adapter functions injected by Run(). Tests inject stubs.
	RunResearch  func(ctx context.Context, r *Runner) (artifact string, err error)
	EnsureIntel  func(ctx context.Context, r *Runner) (skipped bool, reason string, err error)
	RunPlan      func(ctx context.Context, r *Runner) (artifact string, err error)
	RunExecute   func(ctx context.Context, r *Runner) (commits []string, err error)
	RunTest      func(ctx context.Context, r *Runner) (passed bool, output string, err error)
	RunFixTests  func(ctx context.Context, r *Runner, failureOutput string, attempt int) error
	RunReview    func(ctx context.Context, r *Runner) (blocking, important int, output string, err error)
	RunFixReview func(ctx context.Context, r *Runner, findings string, attempt int) error
	RunShip      func(ctx context.Context, r *Runner) (prURL string, pushed bool, err error)
}

// stageDispatch returns the StageFunc registered for stage id.
func stageDispatch(id int) StageFunc {
	switch id {
	case 1:
		return runStageResearch
	case 2:
		return runStageIntel
	case 3:
		return runStagePlan
	case 4:
		return runStageExecute
	case 5:
		return runStageTest
	case 6:
		return runStageReview
	case 7:
		return runStageShip
	}
	return nil
}

// markStarted updates a stage to "running" with a start time.
func markStarted(s *Stage) {
	s.Status = StageRunning
	s.StartedAt = time.Now().UTC()
}

// markDone updates a stage to "completed" with the artifact.
func markDone(s *Stage, artifact string) {
	s.Status = StageCompleted
	s.EndedAt = time.Now().UTC()
	if artifact != "" {
		s.Artifact = artifact
	}
}

func markFailed(s *Stage) {
	s.Status = StageFailed
	s.EndedAt = time.Now().UTC()
}

func markSkipped(s *Stage, reason string) {
	s.Status = StageCompleted
	s.SkippedReason = reason
	s.EndedAt = time.Now().UTC()
}

func runStageResearch(ctx context.Context, r *Runner, s *Stage) error {
	markStarted(s)
	artifact, err := r.RunResearch(ctx, r)
	if err != nil {
		markFailed(s)
		return err
	}
	markDone(s, artifact)
	return nil
}

func runStageIntel(ctx context.Context, r *Runner, s *Stage) error {
	markStarted(s)
	skipped, reason, err := r.EnsureIntel(ctx, r)
	if err != nil {
		markFailed(s)
		return err
	}
	if skipped {
		markSkipped(s, reason)
	} else {
		markDone(s, "")
	}
	return nil
}

func runStagePlan(ctx context.Context, r *Runner, s *Stage) error {
	markStarted(s)
	artifact, err := r.RunPlan(ctx, r)
	if err != nil {
		markFailed(s)
		return err
	}
	markDone(s, artifact)
	return nil
}

func runStageExecute(ctx context.Context, r *Runner, s *Stage) error {
	markStarted(s)
	commits, err := r.RunExecute(ctx, r)
	if err != nil {
		markFailed(s)
		return err
	}
	s.Commits = commits
	markDone(s, "")
	return nil
}

func runStageTest(ctx context.Context, r *Runner, s *Stage) error {
	markStarted(s)
	// Initial attempt + up to 5 fix-loops = 5 total attempts.
	for attempt := 1; attempt <= 5; attempt++ {
		s.Attempts = attempt
		passed, output, err := r.RunTest(ctx, r)
		if err != nil {
			markFailed(s)
			return err
		}
		if passed {
			markDone(s, "")
			return nil
		}
		if attempt == 5 {
			markFailed(s)
			r.State.Failures = append(r.State.Failures, "test stage exhausted after 5 attempts")
			return errFromString("test fix-loop exhausted")
		}
		if err := r.RunFixTests(ctx, r, output, attempt); err != nil {
			markFailed(s)
			return err
		}
	}
	return nil
}

func runStageReview(ctx context.Context, r *Runner, s *Stage) error {
	markStarted(s)
	for attempt := 1; attempt <= 3; attempt++ {
		s.Attempts = attempt
		blocking, important, output, err := r.RunReview(ctx, r)
		if err != nil {
			markFailed(s)
			return err
		}
		if blocking == 0 && important == 0 {
			markDone(s, "")
			return nil
		}
		if attempt == 3 {
			markFailed(s)
			r.State.Failures = append(r.State.Failures, "review fix-loop exhausted after 3 attempts")
			return errFromString("review fix-loop exhausted")
		}
		if err := r.RunFixReview(ctx, r, output, attempt); err != nil {
			markFailed(s)
			return err
		}
	}
	return nil
}

func runStageShip(ctx context.Context, r *Runner, s *Stage) error {
	markStarted(s)
	prURL, _, err := r.RunShip(ctx, r)
	if err != nil {
		markFailed(s)
		return err
	}
	markDone(s, prURL)
	return nil
}

// stageSummary returns a concise one-line completion summary for a stage.
// It is called after fn(ctx,r,s) returns nil, so Artifact/Commits/SkippedReason
// have already been populated by the stage function.
func stageSummary(s *Stage) string {
	if s.SkippedReason != "" {
		return "skipped: " + s.SkippedReason
	}
	if len(s.Commits) > 0 {
		return fmt.Sprintf("%d commit(s)", len(s.Commits))
	}
	if s.Artifact != "" {
		return s.Artifact
	}
	return "done"
}

// errFromString avoids importing errors in this file.
type stageErr struct{ msg string }

func (e *stageErr) Error() string { return e.msg }

func errFromString(s string) error { return &stageErr{msg: s} }
