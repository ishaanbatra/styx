package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
)

func TestLearnScorecard(t *testing.T) {
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	ctx := context.Background()
	if err := bud.RecordOutcome(ctx, budget.Outcome{CLI: "codex", Signals: "complex", DurationS: 12}); err != nil {
		t.Fatal(err)
	}
	a := &app{tracker: bud, routing: config.Routing{}}
	out, err := learnScorecard(ctx, a)
	if err != nil {
		t.Fatalf("learnScorecard: %v", err)
	}
	if !strings.Contains(out, "codex × complex") {
		t.Fatalf("scorecard missing the seeded cell:\n%s", out)
	}
}

func TestLearnListAndForget(t *testing.T) {
	store, err := memory.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	id, err := store.Add(ctx, memory.Item{
		Kind: memory.KindRoutingPreference, Source: "styx-learn", Confidence: 0.8,
		Text:      "codex for specced work [learned-by styx-learn 2026-07-07; evidence: scorecard:codex/complex]",
		Embedding: []float32{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := learnList(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "codex for specced work") || !strings.Contains(out, "styx-learn") {
		t.Fatalf("--list must show text + provenance:\n%s", out)
	}
	if _, err := learnForget(ctx, store, id); err != nil {
		t.Fatalf("forget: %v", err)
	}
	out, _ = learnList(ctx, store)
	if !strings.Contains(out, "no learned memories") {
		t.Fatalf("forgotten memory must be gone:\n%s", out)
	}
	if _, err := learnForget(ctx, store, 999); err == nil {
		t.Fatal("forgetting an unknown id must error")
	}
}

func TestLearnFlagParsing(t *testing.T) {
	a := &app{}
	if err := cmdLearn(a, []string{"--bogus"}); err == nil || !strings.Contains(err.Error(), "--bogus") {
		t.Fatalf("unknown flag must error naming itself, got %v", err)
	}
	if err := cmdLearn(a, []string{"--forget"}); err == nil || !strings.Contains(err.Error(), "--forget") {
		t.Fatalf("--forget without id must error, got %v", err)
	}
	if err := cmdLearn(a, []string{"--forget", "abc"}); err == nil {
		t.Fatal("--forget with a non-numeric id must error")
	}
}
