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

func TestMarkCooldown_ReflectsInState(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	until := time.Now().Add(15 * time.Minute)
	if err := tr.MarkCooldown(ctx, "codex", until); err != nil {
		t.Fatal(err)
	}
	st, err := tr.State(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if st.CooldownUntil.IsZero() {
		t.Error("CooldownUntil zero after MarkCooldown")
	}
	if d := st.CooldownUntil.Sub(until); d > time.Second || d < -time.Second {
		t.Errorf("CooldownUntil drift: %v (want within 1s of %v)", st.CooldownUntil, until)
	}
}

func TestRecentErrors_TriggersCircuit(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = tr.Record(ctx, Event{Channel: "gemini", Verb: "research", Success: false, ErrorKind: "5xx"})
	}
	tripped, err := tr.ShouldCircuitBreak(ctx, "gemini", 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !tripped {
		t.Error("circuit should trip after 5 errors in 60s")
	}
}

func TestRecentErrors_DoesNotTripBelowThreshold(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = tr.Record(ctx, Event{Channel: "gemini", Verb: "research", Success: false, ErrorKind: "5xx"})
	}
	tripped, err := tr.ShouldCircuitBreak(ctx, "gemini", 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if tripped {
		t.Error("circuit should not trip with only 3 errors")
	}
}

func TestState_MessageCounts(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 10, TokensOut: 20, Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := tr.State(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionCount != 3 {
		t.Errorf("SessionCount = %d, want 3", st.SessionCount)
	}
	if st.WeeklyCount != 3 {
		t.Errorf("WeeklyCount = %d, want 3", st.WeeklyCount)
	}
}

func TestUsedPct_ReturnsMaxOfSessionAndWeekly(t *testing.T) {
	ctx := context.Background()

	// Case 1: session ceiling hit, weekly barely ticked — UsedPct returns SessionPct.
	// SetMessageLimits("claude", session=5, weekly=1000): record 5 messages.
	// SessionPct = 5/5*100 = 100, WeeklyPct = 5/1000*100 = 0.5 → max is 100.
	t.Run("session_dominates", func(t *testing.T) {
		tr := newTestTracker(t)
		tr.SetMessageLimits("claude", 5, 1000)
		for i := 0; i < 5; i++ {
			if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 1, TokensOut: 1, Success: true}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := tr.UsedPct(ctx, "claude")
		if err != nil {
			t.Fatal(err)
		}
		if got < 99 || got > 101 {
			t.Errorf("UsedPct = %.2f, want ~100 (session dominates)", got)
		}
	})

	// Case 2: weekly ceiling hit, session barely ticked — UsedPct returns WeeklyPct.
	// SetMessageLimits("agy", session=1000, weekly=4): record 4 messages.
	// SessionPct = 4/1000*100 = 0.4, WeeklyPct = 4/4*100 = 100 → max is 100.
	t.Run("weekly_dominates", func(t *testing.T) {
		tr := newTestTracker(t)
		tr.SetMessageLimits("agy", 1000, 4)
		for i := 0; i < 4; i++ {
			if err := tr.Record(ctx, Event{Channel: "agy", Verb: "plan", TokensIn: 1, TokensOut: 1, Success: true}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := tr.UsedPct(ctx, "agy")
		if err != nil {
			t.Fatal(err)
		}
		if got < 99 || got > 101 {
			t.Errorf("UsedPct = %.2f, want ~100 (weekly dominates)", got)
		}
	})

	// Case 3: no limits configured — UsedPct returns 0.
	t.Run("no_limits_returns_zero", func(t *testing.T) {
		tr := newTestTracker(t)
		// record events but set no limits
		for i := 0; i < 10; i++ {
			if err := tr.Record(ctx, Event{Channel: "ollama", Verb: "grunt", TokensIn: 1, TokensOut: 1, Success: true}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := tr.UsedPct(ctx, "ollama")
		if err != nil {
			t.Fatal(err)
		}
		if got != 0 {
			t.Errorf("UsedPct = %.2f, want 0 for channel with no configured limits", got)
		}
	})
}

func TestSetMessageLimits_ComputesPct(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	tr.SetMessageLimits("claude", 10, 100)

	// Record 5 messages — should be 50% session, 5% weekly.
	for i := 0; i < 5; i++ {
		if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 10, TokensOut: 20, Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := tr.State(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionLimit != 10 {
		t.Errorf("SessionLimit = %d, want 10", st.SessionLimit)
	}
	if st.WeeklyLimit != 100 {
		t.Errorf("WeeklyLimit = %d, want 100", st.WeeklyLimit)
	}
	if st.SessionPct < 49 || st.SessionPct > 51 {
		t.Errorf("SessionPct = %.2f, want ~50", st.SessionPct)
	}
	if st.WeeklyPct < 4 || st.WeeklyPct > 6 {
		t.Errorf("WeeklyPct = %.2f, want ~5", st.WeeklyPct)
	}
	if st.LimitHit {
		t.Error("LimitHit should be false with 5/10 session messages")
	}

	// Record 5 more — session should now hit 100%, LimitHit must flip.
	for i := 0; i < 5; i++ {
		if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 10, TokensOut: 20, Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	st2, err := tr.State(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st2.SessionPct < 100 {
		t.Errorf("SessionPct = %.2f, want >= 100 after 10 messages", st2.SessionPct)
	}
	if !st2.LimitHit {
		t.Error("LimitHit should be true when SessionPct >= 100")
	}
}
