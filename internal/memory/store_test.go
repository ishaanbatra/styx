package memory

import (
	"context"
	"math"
	"path/filepath"
	"testing"
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
