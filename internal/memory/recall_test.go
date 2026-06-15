package memory

import (
	"context"
	"path/filepath"
	"testing"
)

// fakeEmbedder returns a fixed vector per exact text, so recall is deterministic.
type fakeEmbedder struct{ vecs map[string][]float32 }

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	return f.vecs[text], nil
}

func TestCosine(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 0}, []float32{1, 0}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"zero vector", []float32{0, 0}, []float32{1, 0}, 0},
		{"length mismatch", []float32{1}, []float32{1, 0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cosine(tt.a, tt.b); got != tt.want {
				t.Errorf("cosine = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRecallTopKAcrossStores(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	proj, err := Open(filepath.Join(dir, "proj.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer proj.Close()
	glob, err := Open(filepath.Join(dir, "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer glob.Close()

	// Project store: one near-match, one orthogonal.
	mustAdd(t, proj, Item{Kind: KindDecision, Text: "near", Embedding: []float32{0.9, 0.1, 0}})
	mustAdd(t, proj, Item{Kind: KindFact, Text: "far", Embedding: []float32{0, 0, 1}})
	// Global store: exact match.
	mustAdd(t, glob, Item{Kind: KindFact, Text: "exact", Embedding: []float32{1, 0, 0}})

	emb := &fakeEmbedder{vecs: map[string][]float32{"query": {1, 0, 0}}}
	hits, err := Recall(ctx, emb, "query", 2, proj, glob)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2", len(hits))
	}
	if hits[0].Item.Text != "exact" {
		t.Errorf("hits[0] = %q, want exact", hits[0].Item.Text)
	}
	if hits[1].Item.Text != "near" {
		t.Errorf("hits[1] = %q, want near", hits[1].Item.Text)
	}
	if hits[0].Score < hits[1].Score {
		t.Errorf("hits not sorted by score: %v < %v", hits[0].Score, hits[1].Score)
	}
}

func mustAdd(t *testing.T, s *Store, it Item) {
	t.Helper()
	if _, err := s.Add(context.Background(), it); err != nil {
		t.Fatal(err)
	}
}
