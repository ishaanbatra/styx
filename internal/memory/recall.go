package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// Hit is one recalled memory with its similarity score.
type Hit struct {
	Item  Item
	Score float64
}

// recallHalfLife is how fast an item's recall weight halves with age - old
// memories (and low-confidence ones) lose to fresh, certain ones.
const recallHalfLife = 90 * 24 * time.Hour

// decayedScore weights raw cosine similarity by confidence and recency.
func decayedScore(cos, confidence float64, age time.Duration) float64 {
	if confidence <= 0 {
		confidence = 1
	}
	rec := math.Pow(0.5, float64(age)/float64(recallHalfLife))
	return cos * confidence * rec
}

// Recall embeds query and returns the top-k most similar items across the
// given stores (brute-force cosine; personal scale needs no index).
func Recall(ctx context.Context, emb Embedder, query string, k int, stores ...*Store) ([]Hit, error) {
	qv, err := emb.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	var hits []Hit
	for _, s := range stores {
		if s == nil {
			continue
		}
		items, err := s.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("load memories: %w", err)
		}
		for _, it := range items {
			cos := cosine(qv, it.Embedding)
			score := decayedScore(cos, it.Confidence, time.Since(it.CreatedAt))
			hits = append(hits, Hit{Item: it, Score: score})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

// cosine returns the cosine similarity of a and b (0 on mismatch/zero vectors).
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
