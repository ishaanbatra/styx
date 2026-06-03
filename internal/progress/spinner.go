package progress

import (
	"fmt"
	"io"
	"time"
)

// brailleFrames are the spinner animation frames shown on a TTY.
var brailleFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// spinnerInterval is how often the spinner frame advances.
const spinnerInterval = 100 * time.Millisecond

// spinner drives the animated spinner on a TTY.
// It is only created when isTTY is true.
//
// Design notes for C7 (Pause/Resume):
//   - stop is a plain channel; closing it signals the goroutine to exit.
//   - done is closed by the goroutine after it acknowledges the stop, letting
//     callers block until the goroutine has actually exited.
//   - A future Pause() can send on a separate pause channel; Resume() sends
//     on a resume channel. The goroutine select loop can handle those signals
//     without structural changes.
type spinner struct {
	name  string
	w     io.Writer
	start time.Time

	stop chan struct{} // closed by caller to stop the spinner
	done chan struct{} // closed by goroutine when it exits
}

// newSpinner creates and starts a spinner goroutine.
// The caller must hold the tracker mutex while creating it but must release
// before the goroutine starts writing; the goroutine acquires the mutex on
// each tick via writeFunc.
func newSpinner(name string, w io.Writer, writeFunc func(string)) *spinner {
	s := &spinner{
		name:  name,
		w:     w,
		start: time.Now(),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go s.run(writeFunc)
	return s
}

// run is the spinner goroutine body.
func (s *spinner) run(writeFunc func(string)) {
	defer close(s.done)

	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()

	frame := 0

	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			// Redraw every tick so the braille frame advances smoothly at the
			// spinner interval; the elapsed-time text only changes once a second
			// but the animation must keep moving.
			elapsed := formatDuration(time.Since(s.start))
			f := brailleFrames[frame%len(brailleFrames)]
			frame++
			writeFunc(fmt.Sprintf("\r[styx] %s %s %s", s.name, f, elapsed))
		}
	}
}

// stopAndWait signals the spinner to stop and blocks until it exits.
func (s *spinner) stopAndWait() {
	close(s.stop)
	<-s.done
}
