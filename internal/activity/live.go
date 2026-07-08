package activity

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

// LiveRenderer repaints the board to w on a ticker. On a TTY it clears the
// previous frame in place; on a non-TTY it appends frames (quiet cadence). One
// per session; Start/Stop bracket a watched span.
type LiveRenderer struct {
	w     io.Writer
	board *Board
	stall time.Duration
	isTTY bool
	now   func() time.Time

	mu   sync.Mutex
	prev int // lines painted last frame (TTY clear)
	stop chan struct{}
	done chan struct{}
}

// NewLiveRenderer builds a renderer. TTY detection mirrors internal/progress.
func NewLiveRenderer(w io.Writer, b *Board, stall time.Duration) *LiveRenderer {
	isTTY := false
	if f, ok := w.(*os.File); ok {
		isTTY = isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
	}
	return &LiveRenderer{w: w, board: b, stall: stall, isTTY: isTTY, now: time.Now}
}

// paint writes one frame.
func (l *LiveRenderer) paint() {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := Render(l.board.Snapshot(), l.board.WatcherNote(), l.stall, l.now())
	if l.isTTY && l.prev > 0 {
		fmt.Fprintf(l.w, "\033[%dA", l.prev) // cursor up prev lines
	}
	for _, line := range lines {
		if l.isTTY {
			fmt.Fprint(l.w, "\r\033[K")
		}
		fmt.Fprintln(l.w, line)
	}
	l.prev = len(lines)
}

// Start begins repainting every second until Stop.
func (l *LiveRenderer) Start() {
	l.stop = make(chan struct{})
	l.done = make(chan struct{})
	go func() {
		defer close(l.done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				l.paint()
			}
		}
	}()
}

// Stop halts repainting and paints a final frame.
func (l *LiveRenderer) Stop() {
	if l.stop == nil {
		return
	}
	close(l.stop)
	<-l.done
	l.paint()
}
