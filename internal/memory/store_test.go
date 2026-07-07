package memory

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreAddAndAll(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	id, err := s.Add(ctx, Item{
		Kind:      KindDecision,
		Text:      "use sqlite for memory",
		Source:    "thread/claude",
		Embedding: []float32{0.1, -0.5, 0.25},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if id <= 0 {
		t.Errorf("Add returned id %d, want > 0", id)
	}

	items, err := s.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("All returned %d items, want 1", len(items))
	}
	got := items[0]
	if got.Kind != KindDecision || got.Text != "use sqlite for memory" || got.Source != "thread/claude" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	want := []float32{0.1, -0.5, 0.25}
	if len(got.Embedding) != len(want) {
		t.Fatalf("embedding len %d, want %d", len(got.Embedding), len(want))
	}
	for i := range want {
		if math.Abs(float64(got.Embedding[i]-want[i])) > 1e-6 {
			t.Errorf("embedding[%d] = %v, want %v", i, got.Embedding[i], want[i])
		}
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}
}

func TestVecEncodeDecodeRoundTrip(t *testing.T) {
	in := []float32{1.5, -2.25, 0, 3.14159}
	out := decodeVec(encodeVec(in))
	if len(out) != len(in) {
		t.Fatalf("len %d, want %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("[%d] = %v, want %v", i, out[i], in[i])
		}
	}
}

func TestStoreProvenanceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Add(ctx, Item{
		Kind: KindRoutingPreference, Text: "codex for reviews",
		Project: "styx", Scope: "reviews", Confidence: 0.6, Embedding: []float32{0.1},
	}); err != nil {
		t.Fatal(err)
	}
	items, err := s.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	it := items[0]
	if it.Project != "styx" || it.Scope != "reviews" || it.Confidence != 0.6 {
		t.Errorf("provenance round-trip = %+v", it)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestTopByKindRanksByConfidenceAndRecency(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Same kind, different confidence: higher confidence wins at equal age.
	lo, err := s.Add(ctx, Item{Kind: KindUserPreference, Text: "low", Confidence: 0.3, Embedding: []float32{1}})
	if err != nil {
		t.Fatal(err)
	}
	hi, err := s.Add(ctx, Item{Kind: KindUserPreference, Text: "high", Confidence: 0.9, Embedding: []float32{1}})
	if err != nil {
		t.Fatal(err)
	}
	// A different kind never leaks in.
	if _, err := s.Add(ctx, Item{Kind: KindRoutingPreference, Text: "other kind", Confidence: 1, Embedding: []float32{1}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.TopByKind(ctx, KindUserPreference, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != hi || got[1].ID != lo {
		t.Fatalf("want [high low], got %+v", got)
	}
	// k truncates.
	one, _ := s.TopByKind(ctx, KindUserPreference, 1)
	if len(one) != 1 || one[0].Text != "high" {
		t.Fatalf("k=1 must keep the best, got %+v", one)
	}
}

func TestConsumedMarking(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a, _ := s.Add(ctx, Item{Kind: KindRetrospective, Text: "retro A", Embedding: []float32{1}})
	b, _ := s.Add(ctx, Item{Kind: KindRetrospective, Text: "retro B", Embedding: []float32{1}})
	got, err := s.UnconsumedByKind(ctx, KindRetrospective)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a { // oldest first
		t.Fatalf("want both unconsumed oldest-first, got %+v", got)
	}
	if err := s.MarkConsumed(ctx, []int64{a}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.UnconsumedByKind(ctx, KindRetrospective)
	if len(got) != 1 || got[0].ID != b {
		t.Fatalf("consumed retro must be skipped, got %+v", got)
	}
	if err := s.MarkConsumed(ctx, nil); err != nil {
		t.Fatalf("empty MarkConsumed must be a no-op, got %v", err)
	}
}

func TestDeleteAndUpdateEvidence(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _ := s.Add(ctx, Item{Kind: KindRoutingPreference, Text: "old evidence", Embedding: []float32{1}})
	if err := s.UpdateEvidence(ctx, id, "new evidence"); err != nil {
		t.Fatal(err)
	}
	items, _ := s.All(ctx)
	if items[0].Text != "new evidence" {
		t.Fatalf("text must be rewritten, got %q", items[0].Text)
	}
	if time.Since(items[0].CreatedAt) > time.Minute {
		t.Fatal("created_at must be refreshed (dedupe = fresher evidence)")
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if items, _ := s.All(ctx); len(items) != 0 {
		t.Fatalf("deleted item must be gone, got %+v", items)
	}
	if err := s.Delete(ctx, 999); err == nil {
		t.Fatal("deleting an unknown id must error (--forget must not lie)")
	}
	if err := s.UpdateEvidence(ctx, 999, "x"); err == nil {
		t.Fatal("updating an unknown id must error")
	}
}

func TestMostSimilar(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.Add(ctx, Item{Kind: KindRoutingPreference, Text: "codex implements", Embedding: []float32{1, 0}})
	s.Add(ctx, Item{Kind: KindRoutingPreference, Text: "claude reviews", Embedding: []float32{0, 1}})
	s.Add(ctx, Item{Kind: KindUserPreference, Text: "same vec other kind", Embedding: []float32{1, 0}})
	it, sim, err := s.MostSimilar(ctx, KindRoutingPreference, []float32{0.9, 0.1})
	if err != nil {
		t.Fatal(err)
	}
	if it.Text != "codex implements" || sim < 0.9 {
		t.Fatalf("want the codex row with high similarity, got %q sim=%.2f", it.Text, sim)
	}
	if _, sim, _ := s.MostSimilar(ctx, KindDistillation, []float32{1, 0}); sim != 0 {
		t.Fatalf("no items of the kind => similarity 0, got %.2f", sim)
	}
}
