package pipeline

import (
	"context"
	"testing"
)

// scenarioRunner builds a Runner with all adapters as configurable per-test stubs.
type scenarioOpts struct {
	testFailUntilAttempt   int // 0 = never fail; N = pass at attempt N
	reviewFailUntilAttempt int
	executeReturnsError    bool
}

func runScenario(t *testing.T, opts scenarioOpts) *Runner {
	t.Helper()
	proj := t.TempDir()
	r := newStubRunner(t, proj, "scenario goal")

	testAttempt := 0
	r.RunTest = func(ctx context.Context, rr *Runner) (bool, string, error) {
		testAttempt++
		if opts.testFailUntilAttempt > 0 && testAttempt < opts.testFailUntilAttempt {
			return false, "test failed", nil
		}
		return true, "", nil
	}

	reviewAttempt := 0
	r.RunReview = func(ctx context.Context, rr *Runner) (int, int, string, error) {
		reviewAttempt++
		if opts.reviewFailUntilAttempt > 0 && reviewAttempt < opts.reviewFailUntilAttempt {
			return 1, 0, "BLOCKING: something", nil
		}
		return 0, 0, "clean", nil
	}

	if opts.executeReturnsError {
		r.RunExecute = func(ctx context.Context, rr *Runner) ([]string, error) {
			return nil, errFromString("execute failed")
		}
	}
	return r
}

func TestScenario_HappyPath(t *testing.T) {
	r := runScenario(t, scenarioOpts{})
	if err := Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.State.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", r.State.Status)
	}
}

func TestScenario_TestFixLoopSucceeds(t *testing.T) {
	r := runScenario(t, scenarioOpts{testFailUntilAttempt: 3})
	if err := Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.State.Status != StatusCompleted {
		t.Errorf("status = %q, want completed (recovered after 2 failed test attempts)", r.State.Status)
	}
}

func TestScenario_TestFixLoopExhaustion(t *testing.T) {
	// Tests never pass within 5 attempts.
	r := runScenario(t, scenarioOpts{testFailUntilAttempt: 99})
	err := Run(context.Background(), r)
	if err == nil {
		t.Fatal("expected halt error after test fix-loop exhaustion")
	}
	if r.State.Status != StatusFailed {
		t.Errorf("status = %q, want failed", r.State.Status)
	}
}

func TestScenario_ReviewFixLoopSucceeds(t *testing.T) {
	r := runScenario(t, scenarioOpts{reviewFailUntilAttempt: 2})
	if err := Run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if r.State.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", r.State.Status)
	}
}

func TestScenario_ReviewFixLoopExhaustion(t *testing.T) {
	r := runScenario(t, scenarioOpts{reviewFailUntilAttempt: 99})
	err := Run(context.Background(), r)
	if err == nil {
		t.Fatal("expected halt error after review fix-loop exhaustion")
	}
}

func TestScenario_ResumeAfterTestExhaustion(t *testing.T) {
	// First run: tests never pass, pipeline halts at test stage.
	r1 := runScenario(t, scenarioOpts{testFailUntilAttempt: 99})
	_ = Run(context.Background(), r1)
	if r1.State.Status != StatusFailed {
		t.Fatalf("setup failed: expected first run to fail, got %q", r1.State.Status)
	}
	// Second run: same project + runID, but tests now pass.
	proj := r1.ProjectPath
	runID := r1.State.RunID
	r2 := newStubRunner(t, proj, "resume scenario")
	r2.State.RunID = runID
	r2.StateDir = RunDir(proj, runID)
	if err := Resume(context.Background(), r2); err != nil {
		t.Fatal(err)
	}
	if r2.State.Status != StatusCompleted {
		t.Errorf("resume status = %q, want completed", r2.State.Status)
	}
}

func TestScenario_ConcurrentRunRejected(t *testing.T) {
	proj := t.TempDir()
	r1 := newStubRunner(t, proj, "first")
	// Acquire lock manually to simulate a concurrent run in progress.
	if err := AcquireLock(proj, r1.State.RunID); err != nil {
		t.Fatal(err)
	}
	defer ReleaseLock(proj)
	// Second runner with the same projectPath should reject.
	r2 := newStubRunner(t, proj, "second")
	err := Run(context.Background(), r2)
	if err == nil {
		t.Error("expected lock-held error from second Run")
	}
}

func TestScenario_ExecuteFailureHalts(t *testing.T) {
	r := runScenario(t, scenarioOpts{executeReturnsError: true})
	err := Run(context.Background(), r)
	if err == nil {
		t.Fatal("expected halt from execute stage failure")
	}
	if r.State.Status != StatusFailed {
		t.Errorf("status = %q, want failed", r.State.Status)
	}
}
