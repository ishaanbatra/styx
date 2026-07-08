package activity

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLiveRendererPaint(t *testing.T) {
	b := NewBoard()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return now })
	b.Record("claude", "Bash: go test")

	var buf bytes.Buffer
	lr := NewLiveRenderer(&buf, b, DefaultStall)
	lr.now = func() time.Time { return now }
	lr.paint()

	if !strings.Contains(buf.String(), "claude") || !strings.Contains(buf.String(), "Bash: go test") {
		t.Fatalf("paint output missing agent: %q", buf.String())
	}
}

func TestLiveRendererStartStop(t *testing.T) {
	b := NewBoard()
	var buf bytes.Buffer
	lr := NewLiveRenderer(&buf, b, DefaultStall)
	lr.Start()
	lr.Stop() // must not hang or panic
}
