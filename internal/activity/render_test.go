package activity

import (
	"strings"
	"testing"
	"time"
)

func TestRenderLiveAndStalled(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	states := []AgentState{
		{Label: "claude", Last: "WebFetch: example.com", LastAt: now.Add(-2 * time.Second)},
		{Label: "codex", Last: "Bash: go test ./...", LastAt: now.Add(-94 * time.Second)},
	}
	lines := Render(states, "", 90*time.Second, now)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "▸") || !strings.Contains(lines[0], "WebFetch: example.com") {
		t.Errorf("claude line wrong: %q", lines[0])
	}
	if !strings.Contains(lines[1], "⚠") || !strings.Contains(lines[1], "idle") {
		t.Errorf("codex should be stalled: %q", lines[1])
	}
}

func TestRenderDoneAndNote(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	states := []AgentState{{Label: "claude", Done: true, Elapsed: 3*time.Minute + 12*time.Second}}
	lines := Render(states, "both healthy", 90*time.Second, now)
	if !strings.Contains(lines[0], "✓ done") || !strings.Contains(lines[0], "3m12s") {
		t.Errorf("done line wrong: %q", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "watch (ollama): both healthy") {
		t.Errorf("note line wrong: %q", last)
	}
}
