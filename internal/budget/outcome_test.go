package budget

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func testTracker(t *testing.T) *Tracker {
	t.Helper()
	tr, err := New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func TestRecordAndReadOutcomes(t *testing.T) {
	tr := testTracker(t)
	ctx := context.Background()
	if err := tr.RecordOutcome(ctx, Outcome{
		Project: "p1", Thread: "codex", TaskID: "", CLI: "codex",
		Model: "", Signals: "complex", Risk: "edit",
		DurationS: 42.5, TokensIn: 500, TokensOut: 60, Background: false,
	}); err != nil {
		t.Fatalf("record outcome: %v", err)
	}
	if err := tr.RecordOutcome(ctx, Outcome{
		Project: "p1", Thread: "codex", TaskID: "t1", CLI: "codex",
		Risk: "edit", ErrorKind: "timeout", Background: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := tr.OutcomesSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("read outcomes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 outcomes, got %d", len(got))
	}
	// Newest first.
	if got[0].TaskID != "t1" || !got[0].Background || got[0].ErrorKind != "timeout" {
		t.Fatalf("newest row mismatch: %+v", got[0])
	}
	if got[1].DurationS != 42.5 || got[1].Signals != "complex" || got[1].Rating != "" {
		t.Fatalf("oldest row mismatch: %+v", got[1])
	}
	old, err := tr.OutcomesSince(ctx, time.Now().Add(time.Hour))
	if err != nil || len(old) != 0 {
		t.Fatalf("future cutoff must return no rows, got %d (%v)", len(old), err)
	}
}

func TestRateOutcomeMostRecentMatch(t *testing.T) {
	tr := testTracker(t)
	ctx := context.Background()
	for _, o := range []Outcome{
		{Thread: "codex", CLI: "codex"},
		{Thread: "codex", CLI: "codex"},                 // most recent thread=codex
		{Thread: "claude", TaskID: "t3", CLI: "claude"}, // most recent overall
	} {
		if err := tr.RecordOutcome(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	// Rating by thread hits the most recent codex row, not the first.
	id, err := tr.RateOutcome(ctx, "codex", false, "wandered off-plan")
	if err != nil {
		t.Fatalf("rate by thread: %v", err)
	}
	rows, _ := tr.OutcomesSince(ctx, time.Time{})
	var rated Outcome
	for _, r := range rows {
		if r.ID == id {
			rated = r
		}
	}
	if rated.Thread != "codex" || rated.Rating != "bad" || rated.Note != "wandered off-plan" {
		t.Fatalf("rated wrong row: %+v", rated)
	}
	if rows[1].ID != id { // rows are newest-first; the newer codex row is index 1
		t.Fatalf("must rate the MOST RECENT matching row, rated id=%d rows=%+v", id, rows)
	}
	// Rating by task id.
	if _, err := tr.RateOutcome(ctx, "t3", true, ""); err != nil {
		t.Fatalf("rate by task id: %v", err)
	}
	rows, _ = tr.OutcomesSince(ctx, time.Time{})
	if rows[0].Rating != "good" {
		t.Fatalf("task-id rating not applied: %+v", rows[0])
	}
	// No match is a loud error.
	if _, err := tr.RateOutcome(ctx, "nope", true, ""); err == nil {
		t.Fatal("rating an unknown thread/task must error")
	}
}
