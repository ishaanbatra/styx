package budget

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestTracker(t *testing.T) *Tracker {
	t.Helper()
	dir := t.TempDir()
	tr, err := New(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func TestRecord_AppendsRow(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 100, TokensOut: 200, Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 50, TokensOut: 30, Success: true}); err != nil {
		t.Fatal(err)
	}
	got, err := tr.totalTokens(ctx, "claude", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := 380; got != want {
		t.Errorf("totalTokens: got %d, want %d", got, want)
	}
}

func TestState_UsedPctReflectsCap(t *testing.T) {
	tr := newTestTracker(t)
	tr.SetCap("claude", 100_000) // 100k tokens for the window
	ctx := context.Background()
	if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 30_000, TokensOut: 20_000, Success: true}); err != nil {
		t.Fatal(err)
	}
	st, err := tr.State(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.UsedPct < 49 || st.UsedPct > 51 {
		t.Errorf("UsedPct = %.2f, want ~50", st.UsedPct)
	}
}

func TestState_UnknownChannelHasZeroUsage(t *testing.T) {
	tr := newTestTracker(t)
	st, err := tr.State(context.Background(), "ollama")
	if err != nil {
		t.Fatal(err)
	}
	if st.UsedPct != 0 {
		t.Errorf("UsedPct for unrecorded channel: got %.2f, want 0", st.UsedPct)
	}
}
