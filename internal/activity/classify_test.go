package activity

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	base := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		events        []string
		step          time.Duration
		idle          time.Duration
		wantVerdict   verdict
		wantIdentical int
		wantFiles     int
		wantRate      bool
	}{
		{
			name: "steady distinct-file edits", events: []string{
				"Edit: internal/a.go", "Edit: internal/b.go", "Bash: go test ./internal/...",
			}, step: 10 * time.Second, idle: 5 * time.Second, wantVerdict: healthy, wantIdentical: 1, wantFiles: 2, wantRate: true,
		},
		{
			name: "identical run", events: []string{
				"Bash: go test ./...", "Bash: go test ./...", "Bash: go test ./...", "Bash: go test ./...",
			}, step: 5 * time.Second, idle: time.Second, wantVerdict: suspicious, wantIdentical: loopRun, wantRate: true,
		},
		{
			name: "idle beyond stall", events: []string{
				"Read: internal/activity/watcher.go",
			}, idle: DefaultStall + time.Second, wantVerdict: suspicious, wantIdentical: 1, wantFiles: 1,
		},
		{
			name: "changing work on one file", events: []string{
				"Read: internal/activity/watcher.go", "Edit: internal/activity/watcher.go",
				"Bash: go test ./internal/activity", "Read: internal/activity/watcher.go",
				"Edit: internal/activity/watcher.go",
			}, step: 5 * time.Second, idle: time.Second, wantVerdict: healthy, wantIdentical: 1, wantFiles: 1, wantRate: true,
		},
		{
			name: "low event rate but recent", events: []string{
				"Read: README.md",
			}, idle: 45 * time.Second, wantVerdict: healthy, wantIdentical: 1, wantFiles: 1, wantRate: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBoard()
			now := base
			b.SetClock(func() time.Time { return now })
			for _, event := range tc.events {
				b.Record("agent", event)
				now = now.Add(tc.step)
			}
			lastAt := b.Snapshot()[0].LastAt
			now = lastAt.Add(tc.idle)
			signals, got := classify(b.Snapshot()[0], now, DefaultStall)
			if got != tc.wantVerdict {
				t.Fatalf("verdict = %v, want %v (signals %+v)", got, tc.wantVerdict, signals)
			}
			if signals.ConsecutiveIdentical != tc.wantIdentical || signals.DistinctFiles != tc.wantFiles {
				t.Fatalf("signals = %+v, want identical=%d files=%d", signals, tc.wantIdentical, tc.wantFiles)
			}
			if tc.wantRate && signals.EventsPerMin <= 0 {
				t.Fatalf("recent case must have a positive event rate: %+v", signals)
			}
		})
	}
}
