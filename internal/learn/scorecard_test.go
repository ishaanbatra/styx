package learn

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
)

// seedOutcomes writes rows through the real budget tracker so the test
// covers the sqlite round trip, then reads them back the way styx learn does.
func seedOutcomes(t *testing.T, rows []budget.Outcome) []budget.Outcome {
	t.Helper()
	tr, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	ctx := context.Background()
	for _, o := range rows {
		if err := tr.RecordOutcome(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	got, err := tr.OutcomesSince(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func cell(t *testing.T, s Scorecard, cli, signal string) Cell {
	t.Helper()
	for _, c := range s.Cells {
		if c.CLI == cli && c.Signal == signal {
			return c
		}
	}
	t.Fatalf("no cell %s/%s in %+v", cli, signal, s.Cells)
	return Cell{}
}

func TestBuildScorecard(t *testing.T) {
	rows := seedOutcomes(t, []budget.Outcome{
		// codex × complex: 2 attempts, 1 clean (one classified error).
		{CLI: "codex", Signals: "complex", DurationS: 10, TokensIn: 100, TokensOut: 10},
		{CLI: "codex", Signals: "complex", DurationS: 30, TokensIn: 300, TokensOut: 30, ErrorKind: "timeout"},
		// codex × trivial AND complex: multi-signal row lands in both cells.
		{CLI: "codex", Signals: "complex,trivial", DurationS: 20, TokensIn: 200, TokensOut: 20},
		// claude, no signals: "(none)" cell; rated bad => not clean despite no error.
		{CLI: "claude", DurationS: 5, Rating: "bad", Note: "wandered"},
		// claude good rating.
		{CLI: "claude", DurationS: 7, Rating: "good"},
	})
	s := Build(rows, 30)

	cx := cell(t, s, "codex", "complex")
	if cx.Attempts != 3 || cx.Clean != 2 {
		t.Fatalf("codex/complex want 3 attempts 2 clean, got %+v", cx)
	}
	if cx.MedianDurationS != 20 {
		t.Fatalf("median duration of {10,30,20} = 20, got %v", cx.MedianDurationS)
	}
	if cx.MedianTokens != 220 {
		t.Fatalf("median tokens of {110,330,220} = 220, got %v", cx.MedianTokens)
	}
	tv := cell(t, s, "codex", "trivial")
	if tv.Attempts != 1 || tv.Clean != 1 {
		t.Fatalf("codex/trivial want 1/1, got %+v", tv)
	}
	cl := cell(t, s, "claude", "(none)")
	if cl.Attempts != 2 || cl.Clean != 1 || cl.Good != 1 || cl.Bad != 1 {
		t.Fatalf("claude/(none) want 2 attempts, 1 clean (bad rating is unclean), +1/-1, got %+v", cl)
	}

	if !s.HasCell("codex", "complex") || s.HasCell("codex", "nope") || s.HasCell("agy", "complex") {
		t.Fatal("HasCell must match real cells only")
	}

	out := s.Render()
	for _, want := range []string{"codex × complex", "2/3 clean", "claude × (none)", "rated +1/-1", "30d"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	// Deterministic order: claude before codex.
	if strings.Index(out, "claude") > strings.Index(out, "codex") {
		t.Fatalf("cells must be sorted by cli then signal:\n%s", out)
	}
}

func TestBuildScorecardEmpty(t *testing.T) {
	s := Build(nil, 30)
	if len(s.Cells) != 0 {
		t.Fatalf("no rows => no cells, got %+v", s.Cells)
	}
	if !strings.Contains(s.Render(), "no dispatch outcomes") {
		t.Fatalf("empty scorecard must say so, got %q", s.Render())
	}
}
