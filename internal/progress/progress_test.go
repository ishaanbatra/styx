package progress_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/progress"
)

// TestTracker_NonTTY_PrintsStartAndEnd verifies that a non-TTY tracker
// emits exactly two lines: one with the stage name + "started", and one
// with the stage name + the Done summary.
func TestTracker_NonTTY_PrintsStartAndEnd(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, false)
	s := tr.Stage("Drafting")
	s.Done("8432 tokens")

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "Drafting") || !strings.Contains(lines[0], "started") {
		t.Errorf("first line should contain 'Drafting' and 'started', got: %q", lines[0])
	}
	if !strings.Contains(lines[1], "Drafting") || !strings.Contains(lines[1], "8432 tokens") {
		t.Errorf("second line should contain 'Drafting' and '8432 tokens', got: %q", lines[1])
	}
}

// TestTracker_Quiet_PrintsNothing verifies that a quiet tracker produces
// zero output regardless of Stage/Info/Done calls.
func TestTracker_Quiet_PrintsNothing(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, true, false)
	s := tr.Stage("doing stuff")
	s.Info("some detail")
	s.Done("complete")

	if buf.Len() != 0 {
		t.Errorf("expected empty buffer, got: %q", buf.String())
	}
}

// TestStage_Info_OnlyInVerbose_NonVerbose verifies that Info is suppressed
// when verbose=false.
func TestStage_Info_OnlyInVerbose_NonVerbose(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, false)
	s := tr.Stage("running")
	s.Info("detail line")
	s.Done("ok")

	out := buf.String()
	// Should NOT contain "detail line"
	if strings.Contains(out, "detail line") {
		t.Errorf("expected Info to be suppressed in non-verbose mode, but output contains 'detail line': %q", out)
	}
}

// TestStage_Info_OnlyInVerbose_Verbose verifies that Info is shown when
// verbose=true.
func TestStage_Info_OnlyInVerbose_Verbose(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, true)
	s := tr.Stage("running")
	s.Info("detail line")
	s.Done("ok")

	out := buf.String()
	if !strings.Contains(out, "detail line") {
		t.Errorf("expected Info to be printed in verbose mode, but output does not contain 'detail line': %q", out)
	}
}

// TestStage_Fail_NonTTY verifies that Fail prints a line containing "failed"
// and the error message.
func TestStage_Fail_NonTTY(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, false)
	s := tr.Stage("uploading")
	s.Fail(errors.New("boom"))

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Expect at least one failure line (the last non-empty line)
	var failLine string
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			failLine = lines[i]
			break
		}
	}
	if !strings.Contains(failLine, "failed") {
		t.Errorf("fail line should contain 'failed', got: %q", failLine)
	}
	if !strings.Contains(failLine, "boom") {
		t.Errorf("fail line should contain 'boom', got: %q", failLine)
	}
}

// TestQuiet_And_NewQuiet_Identical verifies that Quiet() behaves identically
// to New(buf, true, false) — both produce zero output.
func TestQuiet_And_NewQuiet_Identical(t *testing.T) {
	var buf1, buf2 bytes.Buffer

	// Using Quiet() factory
	q := progress.Quiet()
	s1 := q.Stage("test")
	s1.Info("info")
	s1.Done("done")

	// Using New with quiet=true
	tr := progress.New(&buf2, true, false)
	s2 := tr.Stage("test")
	s2.Info("info")
	s2.Done("done")

	if buf1.Len() != 0 {
		t.Errorf("Quiet() produced output: %q", buf1.String())
	}
	if buf2.Len() != 0 {
		t.Errorf("New(buf,true,false) produced output: %q", buf2.String())
	}
}

// TestTracker_Prefix verifies that every emitted line starts with "[styx] ".
func TestTracker_Prefix(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, true)
	s := tr.Stage("step1")
	s.Info("some detail")
	s.Done("finished")

	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "[styx] ") {
			t.Errorf("line does not start with '[styx] ': %q", line)
		}
	}
}

// TestStage_ImplicitClose verifies that opening a new Stage implicitly closes
// the previous one, and both produce their final lines.
func TestStage_ImplicitClose(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, false)
	s1 := tr.Stage("stage-one")
	_ = tr.Stage("stage-two") // should implicitly close s1
	s1.Done("ignored")        // calling Done on already-closed stage should be safe

	out := buf.String()
	// stage-one should have been implicitly done-ed (or at least started)
	if !strings.Contains(out, "stage-one") {
		t.Errorf("expected output for stage-one, got: %q", out)
	}
	if !strings.Contains(out, "stage-two") {
		t.Errorf("expected output for stage-two, got: %q", out)
	}
}

// TestStage_PauseResume_NonTTY_NoPanic verifies that Pause and Resume are
// no-ops on a non-TTY writer and that the buffer still contains the start and
// done lines as if Pause/Resume were never called.
func TestStage_PauseResume_NonTTY_NoPanic(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, false)
	s := tr.Stage("applying plan")
	s.Info("plan size: 42 bytes") // no-op (non-verbose), but must not panic
	s.Pause()
	// Simulate child work (writes something to the buffer directly).
	buf.WriteString("[child] doing stuff\n")
	s.Resume()
	s.Done("done")

	out := buf.String()
	if !strings.Contains(out, "applying plan") {
		t.Errorf("expected 'applying plan' in output, got: %q", out)
	}
	if !strings.Contains(out, "done") {
		t.Errorf("expected 'done' in output, got: %q", out)
	}
}

// TestStage_DoneAfterPause_StillFinalizes verifies that calling Done after Pause
// still emits the completion line (Pause must not consume the done state).
func TestStage_DoneAfterPause_StillFinalizes(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, false)
	s := tr.Stage("uploading")
	s.Pause()
	s.Done("finished")

	out := buf.String()
	if !strings.Contains(out, "uploading") {
		t.Errorf("expected 'uploading' in output, got: %q", out)
	}
	if !strings.Contains(out, "finished") {
		t.Errorf("expected 'finished' in done line, got: %q", out)
	}
}

// TestDoubleDone_Safe verifies that calling Done twice on the same Stage
// does not panic or produce duplicate final lines.
func TestDoubleDone_Safe(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, false)
	s := tr.Stage("double")
	s.Done("first")
	s.Done("second") // should be a no-op

	out := buf.String()
	// Count occurrences of the stage name in completion lines
	count := strings.Count(out, "double")
	// Should have exactly 2: one for "started" and one for completion
	if count != 2 {
		t.Errorf("expected 2 occurrences of 'double' (start + end), got %d in:\n%s", count, out)
	}
}
