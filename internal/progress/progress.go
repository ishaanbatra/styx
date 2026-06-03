// Package progress provides a TTY-aware progress emitter that narrates long
// operations to an io.Writer (typically os.Stderr) "like an LLM showing its
// thinking."
//
// Three modes:
//
//   - Quiet (quiet=true): all methods are no-ops; nothing is printed.
//   - Non-TTY (w is not a terminal): plain text lines, no animation.
//   - TTY (w is a terminal): animated braille spinner with elapsed time.
//
// Every emitted line is prefixed with "[styx] ".
package progress

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mattn/go-isatty"
)

// Tracker is the top-level progress emitter for one styx invocation.
// All exported methods are safe for concurrent use.
type Tracker struct {
	w       io.Writer
	quiet   bool
	verbose bool
	isTTY   bool

	mu          sync.Mutex
	activeStage *Stage // the currently open stage (nil if none)
}

// New creates a Tracker writing to w.
// quiet suppresses all output; verbose enables Info lines.
func New(w io.Writer, quiet, verbose bool) *Tracker {
	isTTY := false
	if f, ok := w.(*os.File); ok {
		isTTY = isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
	}
	return &Tracker{
		w:       w,
		quiet:   quiet,
		verbose: verbose,
		isTTY:   isTTY,
	}
}

// Default returns a Tracker writing to os.Stderr.
// quiet and verbose are read from environment variables STYX_QUIET / STYX_VERBOSE
// (accepted values: "1" or "true").
func Default() *Tracker {
	quiet := envBool("STYX_QUIET")
	verbose := envBool("STYX_VERBOSE")
	return New(os.Stderr, quiet, verbose)
}

// Quiet returns a no-op Tracker that prints nothing.
func Quiet() *Tracker {
	return New(io.Discard, true, false)
}

// Stage opens a new named stage. If a stage is already active it is
// implicitly closed (the tracker calls Done on the previous stage with an
// empty summary before opening the new one).
func (t *Tracker) Stage(name string) *Stage {
	if t.quiet {
		return &Stage{quiet: true}
	}

	// Read prev under lock, then close it outside the lock (prev is already
	// published so no other goroutine can replace it under us).
	t.mu.Lock()
	prev := t.activeStage
	t.mu.Unlock()

	if prev != nil {
		prev.implicitClose()
	}

	// Build s COMPLETELY before publishing it to t.activeStage.
	// This ensures that any concurrent goroutine that reads t.activeStage
	// and calls s methods (e.g. implicitClose → finish → s.sp) always sees
	// a fully-initialised Stage — eliminating the data race on s.sp.
	s := &Stage{
		tracker: t,
		name:    name,
		start:   time.Now(),
	}

	if t.isTTY {
		// On a TTY the spinner goroutine handles writing; we start it here.
		// We pass a write function that acquires the tracker mutex so that the
		// goroutine and public API methods don't race on w.
		s.sp = newSpinner(name, t.w, func(line string) {
			t.mu.Lock()
			fmt.Fprint(t.w, line)
			t.mu.Unlock()
		})
	} else {
		// Non-TTY: plain text start line.
		t.mu.Lock()
		fmt.Fprintf(t.w, "[styx] %s... (started)\n", name)
		t.mu.Unlock()
	}

	// Only NOW publish s; it is fully constructed.
	t.mu.Lock()
	t.activeStage = s
	t.mu.Unlock()

	return s
}

// writeLine writes a single complete line to w under the tracker mutex.
// It clears the spinner line first when on a TTY.
func (t *Tracker) writeLine(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.isTTY {
		// Clear the current spinner line before printing the final result.
		fmt.Fprint(t.w, "\r\033[K")
	}
	fmt.Fprintln(t.w, line)
}

// clearActiveStage sets the tracker's active stage to nil.
func (t *Tracker) clearActiveStage(s *Stage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.activeStage == s {
		t.activeStage = nil
	}
}

// Stage represents an ongoing named operation.
type Stage struct {
	tracker *Tracker
	name    string
	start   time.Time

	// sp is set only in TTY mode; nil otherwise.
	sp *spinner

	quiet bool

	// done is 0 until Done or Fail is called; CAS from 0→1 to claim "closer".
	// This ensures idempotent completion (double-Done is safe).
	done atomic.Uint32
}

// Info emits a sub-detail line. It is shown only when verbose=true.
// In quiet mode or non-verbose mode it is a no-op.
func (s *Stage) Info(format string, args ...any) {
	if s.quiet {
		return
	}
	if !s.tracker.verbose {
		return
	}
	detail := fmt.Sprintf(format, args...)

	t := s.tracker
	if t.isTTY {
		// Clear the spinner line, print the detail, let the spinner resume.
		t.mu.Lock()
		fmt.Fprint(t.w, "\r\033[K")
		fmt.Fprintf(t.w, "[styx]   %s\n", detail)
		t.mu.Unlock()
	} else {
		t.mu.Lock()
		fmt.Fprintf(t.w, "[styx]   %s\n", detail)
		t.mu.Unlock()
	}
}

// Done completes the stage with a summary message.
// Calling Done more than once is safe (subsequent calls are no-ops).
func (s *Stage) Done(format string, args ...any) {
	if s.quiet {
		return
	}
	if !s.done.CompareAndSwap(0, 1) {
		return // already completed
	}
	s.finish(fmt.Sprintf(format, args...), nil)
}

// Fail completes the stage as failed.
// Calling Fail (or Done) more than once is safe (subsequent calls are no-ops).
func (s *Stage) Fail(err error) {
	if s.quiet {
		return
	}
	if !s.done.CompareAndSwap(0, 1) {
		return
	}
	s.finish("", err)
}

// Pause suspends the spinner so a child process can write to the same stream
// without the animation interleaving. No-op unless on a TTY with a running spinner.
// Pause does NOT complete the stage; Done/Fail still work afterward.
func (s *Stage) Pause() {
	if s.quiet {
		return
	}
	if s.sp != nil {
		s.sp.stopAndWait()
		s.sp = nil
		s.tracker.mu.Lock()
		fmt.Fprint(s.tracker.w, "\r\033[K") // clear the spinner line
		s.tracker.mu.Unlock()
	}
}

// Resume restarts the spinner after a Pause. No-op in quiet/non-TTY mode, if the
// stage is already complete, or if a spinner is already running.
// Note: the resumed spinner resets its elapsed clock to zero — acceptable here
// because Apply pauses-then-Done's without resuming in the common path.
func (s *Stage) Resume() {
	if s.quiet || !s.tracker.isTTY {
		return
	}
	if s.done.Load() != 0 || s.sp != nil {
		return
	}
	s.sp = newSpinner(s.name, s.tracker.w, func(line string) {
		s.tracker.mu.Lock()
		fmt.Fprint(s.tracker.w, line)
		s.tracker.mu.Unlock()
	})
}

// implicitClose is called when a new Stage is opened while this one is still
// active. It closes the stage with an implicit completion marker.
func (s *Stage) implicitClose() {
	if s.quiet {
		return
	}
	if !s.done.CompareAndSwap(0, 1) {
		return
	}
	s.finish("", nil) // summary will show as "(elapsed)"
}

// finish stops any spinner, then emits the final line.
func (s *Stage) finish(summary string, err error) {
	if s.sp != nil {
		s.sp.stopAndWait()
	}

	elapsed := formatDuration(time.Since(s.start))
	var line string
	if err != nil {
		line = fmt.Sprintf("[styx] %s... (%s, failed: %s)", s.name, elapsed, err.Error())
	} else if summary != "" {
		line = fmt.Sprintf("[styx] %s... (%s, %s)", s.name, elapsed, summary)
	} else {
		line = fmt.Sprintf("[styx] %s... (%s)", s.name, elapsed)
	}

	s.tracker.writeLine(line)
	s.tracker.clearActiveStage(s)
}

// formatDuration formats a time.Duration compactly:
//   - sub-second: "120ms"
//   - seconds only: "8s"
//   - minutes+seconds: "1m23s"
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%ds", m, s)
}

// envBool returns true if the environment variable named key is set to "1"
// or "true" (case-insensitive).
func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "1" || v == "true" || v == "True" || v == "TRUE"
}
