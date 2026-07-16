package activity

import (
	"sync"
	"testing"
	"time"
)

func TestBoardRecordAndSnapshot(t *testing.T) {
	b := NewBoard()
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return base })

	b.Record("claude", "Bash: go test")
	b.Record("codex", "Read: main.go")
	b.Record("claude", "WebFetch: example.com")

	snap := b.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("want 2 agents, got %d", len(snap))
	}
	if snap[0].Label != "claude" || snap[0].Last != "WebFetch: example.com" {
		t.Fatalf("claude row wrong: %+v", snap[0])
	}
	if !snap[0].LastAt.Equal(base) {
		t.Fatalf("LastAt not stamped from clock: %v", snap[0].LastAt)
	}
}

func TestBoardDone(t *testing.T) {
	b := NewBoard()
	b.Record("claude", "Bash: go build")
	b.Done("claude", 3*time.Minute)
	snap := b.Snapshot()
	if !snap[0].Done || snap[0].Elapsed != 3*time.Minute {
		t.Fatalf("done not recorded: %+v", snap[0])
	}
}

func TestBoardRecentCap(t *testing.T) {
	b := NewBoard()
	base := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	now := base
	b.SetClock(func() time.Time { return now })
	for i := 0; i < recentCap+5; i++ {
		b.Record("claude", "event "+time.Duration(i).String())
		now = now.Add(time.Second)
	}
	if got := len(b.Recent("claude")); got != recentCap {
		t.Fatalf("recent cap = %d, want %d", got, recentCap)
	}
	events := b.RecentEvents("claude")
	if len(events) != recentCap {
		t.Fatalf("recent event cap = %d, want %d", len(events), recentCap)
	}
	if !events[0].At.Equal(base.Add(5*time.Second)) || !events[len(events)-1].At.Equal(base.Add(24*time.Second)) {
		t.Fatalf("timestamped event order/cap wrong: first=%v last=%v", events[0].At, events[len(events)-1].At)
	}
	lines := b.Recent("claude")
	if lines[0] != events[0].Summary || lines[len(lines)-1] != events[len(events)-1].Summary {
		t.Fatalf("Recent must derive summaries from events: lines=%v events=%v", lines, events)
	}
}

func TestBoardWatcherNote(t *testing.T) {
	b := NewBoard()
	b.SetWatcherNote("both healthy")
	if b.WatcherNote() != "both healthy" {
		t.Fatalf("note = %q", b.WatcherNote())
	}
}

func TestBoardConcurrentWriters(t *testing.T) {
	b := NewBoard()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.Record("claude", "x") }()
	}
	wg.Wait()
	if len(b.Snapshot()) != 1 {
		t.Fatalf("want 1 agent after concurrent writes")
	}
}
