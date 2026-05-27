package execute

import (
	"context"
	"os/exec"
	"strings"
)

// TestResult is the outcome of a single RunTests call.
type TestResult struct {
	Passed   bool
	Skipped  bool   // true when no test command was detected
	Output   string // combined stdout+stderr
	ExitCode int
}

// DetectTestCommand maps a sniffed test framework name to an argv slice that
// runs the suite. Returns nil for unknown frameworks (caller treats nil as "skip").
func DetectTestCommand(framework string) []string {
	switch strings.ToLower(strings.TrimSpace(framework)) {
	case "pytest":
		return []string{"pytest"}
	case "jest", "vitest":
		return []string{"npm", "test"}
	case "go test":
		return []string{"go", "test", "./..."}
	case "cargo test":
		return []string{"cargo", "test"}
	}
	return nil
}

// RunTests executes argv (empty -> skipped) in workDir and returns a TestResult.
func RunTests(ctx context.Context, workDir string, argv []string) (TestResult, error) {
	if len(argv) == 0 {
		return TestResult{Skipped: true}, nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	res := TestResult{Output: string(out)}
	if err == nil {
		res.Passed = true
		return res, nil
	}
	var ee *exec.ExitError
	if errAs(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, nil
	}
	// Non-ExitError failures (binary missing, etc.) bubble up.
	return res, err
}
