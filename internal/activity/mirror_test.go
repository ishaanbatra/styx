package activity

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"
)

func TestMirrorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.json")
	states := []AgentState{
		{Label: "claude", Last: "Bash: go test", LastAt: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)},
	}
	if err := WriteMirror(path, states, "healthy"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, note, err := ReadMirror(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if note != "healthy" || len(got) != 1 || got[0].Label != "claude" || got[0].Last != "Bash: go test" {
		t.Fatalf("round trip mismatch: %+v note=%q", got, note)
	}
	if !got[0].LastAt.Equal(states[0].LastAt) {
		t.Fatalf("LastAt mismatch: got %v want %v", got[0].LastAt, states[0].LastAt)
	}
}

func TestReadMirrorMissing(t *testing.T) {
	_, _, err := ReadMirror(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("want error for missing mirror")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("want errors.Is(err, fs.ErrNotExist), got %v", err)
	}
}

func TestMirrorThrottleDebounces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.json")
	b := NewBoard()
	b.Record("claude", "Bash: go build")

	throttle := MirrorThrottle(b, path, time.Hour) // huge window: second call must be suppressed
	if err := throttle(); err != nil {
		t.Fatalf("first throttled write: %v", err)
	}
	states, _, err := ReadMirror(path)
	if err != nil {
		t.Fatalf("read after first write: %v", err)
	}
	if len(states) != 1 || states[0].Label != "claude" {
		t.Fatalf("unexpected mirrored state: %+v", states)
	}

	b.Record("codex", "Read: main.go")
	if err := throttle(); err != nil {
		t.Fatalf("second throttled write: %v", err)
	}
	states, _, err = ReadMirror(path)
	if err != nil {
		t.Fatalf("read after second (suppressed) write: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("throttle should have suppressed the second write, got %+v", states)
	}
}

func TestMirrorThrottleWritesAfterInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.json")
	b := NewBoard()
	b.Record("claude", "Bash: go build")

	throttle := MirrorThrottle(b, path, 10*time.Millisecond)
	if err := throttle(); err != nil {
		t.Fatalf("first throttled write: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	b.Record("codex", "Read: main.go")
	if err := throttle(); err != nil {
		t.Fatalf("second throttled write: %v", err)
	}
	states, _, err := ReadMirror(path)
	if err != nil {
		t.Fatalf("read after second write: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("want 2 states after interval elapsed, got %+v", states)
	}
}
