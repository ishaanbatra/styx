package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/learn"
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

// learnOllama serves BOTH /api/chat (scripted candidates) and /api/embed
// (deterministic per-text vectors) so the digest runs fully hermetic.
func learnOllama(t *testing.T, candidates string, vecFor func(text string) []float32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat":
			json.NewEncoder(w).Encode(map[string]any{"message": map[string]any{"content": candidates}})
		case "/api/embed":
			var req struct {
				Input string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{vecFor(req.Input)}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func learnFixture(t *testing.T) (*app, *memory.Store, int64) {
	t.Helper()
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	ctx := context.Background()
	// A scorecard cell the candidate can cite, plus a rated outcome note.
	for i := 0; i < 3; i++ {
		if err := bud.RecordOutcome(ctx, budget.Outcome{CLI: "codex", Signals: "complex", DurationS: 20}); err != nil {
			t.Fatal(err)
		}
	}
	if err := bud.RecordOutcome(ctx, budget.Outcome{CLI: "agy", Rating: "bad", Note: "timed out twice"}); err != nil {
		t.Fatal(err)
	}
	store, err := memory.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	retroID, err := store.Add(ctx, memory.Item{Kind: memory.KindRetrospective,
		Text: "user wanted shorter summaries", Embedding: []float32{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	return &app{tracker: bud, routing: config.Routing{}}, store, retroID
}

func TestRunLearnDigestWritesGuardedCandidates(t *testing.T) {
	a, store, retroID := learnFixture(t)
	ctx := context.Background()
	cands := fmt.Sprintf(`{"candidates":[
		{"kind":"routing-preference","text":"codex handles complex specced work well","confidence":0.8,"evidence":"scorecard:codex/complex"},
		{"kind":"user-preference","text":"prefers shorter summaries","confidence":0.7,"evidence":"retro:%d"},
		{"kind":"routing-preference","text":"fabricated","confidence":0.9,"evidence":"scorecard:ollama/huge"}
	]}`, retroID)
	// Orthogonal vectors: nothing dedupes.
	vecs := map[string][]float32{}
	next := 0
	srv := learnOllama(t, cands, func(text string) []float32 {
		if v, ok := vecs[text]; ok {
			return v
		}
		v := make([]float32, 8)
		v[next%8] = 1
		next++
		vecs[text] = v
		return v
	})
	emb := memory.NewOllamaEmbedder(srv.URL, "test-embed")
	dig := &learn.Digester{BaseURL: srv.URL, Model: "test-model"}

	out, err := runLearnDigest(ctx, a, store, emb, dig, false)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	for _, want := range []string{"learned", "codex handles complex specced work well", "dropped", "fabricated"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	// Two survivors written with provenance.
	items, _ := store.TopByKind(ctx, memory.KindRoutingPreference, 10)
	if len(items) != 1 || items[0].Source != "styx-learn" ||
		!strings.Contains(items[0].Text, "learned-by styx-learn") ||
		!strings.Contains(items[0].Text, "scorecard:codex/complex") {
		t.Fatalf("routing memory must carry provenance: %+v", items)
	}
	if items[0].Confidence != 0.8 {
		t.Fatalf("candidate confidence must persist, got %v", items[0].Confidence)
	}
	if ups, _ := store.TopByKind(ctx, memory.KindUserPreference, 10); len(ups) != 1 {
		t.Fatalf("user-preference must be written, got %+v", ups)
	}
	// Retrospective consumed.
	if left, _ := store.UnconsumedByKind(ctx, memory.KindRetrospective); len(left) != 0 {
		t.Fatalf("digested retrospective must be marked consumed, got %+v", left)
	}
}

func TestRunLearnDigestDedupesNearDuplicates(t *testing.T) {
	a, store, _ := learnFixture(t)
	ctx := context.Background()
	// Existing learned memory with a known embedding.
	sameVec := []float32{1, 0, 0, 0}
	existingID, err := store.Add(ctx, memory.Item{Kind: memory.KindRoutingPreference,
		Text:   "codex for complex work [learned-by styx-learn 2026-06-01; evidence: scorecard:codex/complex]",
		Source: "styx-learn", Confidence: 0.7, Embedding: sameVec})
	if err != nil {
		t.Fatal(err)
	}
	cands := `{"candidates":[{"kind":"routing-preference","text":"codex is best for complex work","confidence":0.8,"evidence":"scorecard:codex/complex"}]}`
	srv := learnOllama(t, cands, func(string) []float32 { return sameVec })
	emb := memory.NewOllamaEmbedder(srv.URL, "test-embed")
	dig := &learn.Digester{BaseURL: srv.URL, Model: "test-model"}

	out, err := runLearnDigest(ctx, a, store, emb, dig, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "refreshed") {
		t.Fatalf("near-duplicate must refresh, not multiply:\n%s", out)
	}
	items, _ := store.TopByKind(ctx, memory.KindRoutingPreference, 10)
	if len(items) != 1 || items[0].ID != existingID {
		t.Fatalf("want exactly the refreshed original, got %+v", items)
	}
	if !strings.Contains(items[0].Text, "codex is best for complex work") {
		t.Fatalf("refreshed row must carry the new text+evidence, got %q", items[0].Text)
	}
}

func TestRunLearnDigestDryRunWritesNothing(t *testing.T) {
	a, store, retroID := learnFixture(t)
	ctx := context.Background()
	cands := fmt.Sprintf(`{"candidates":[{"kind":"user-preference","text":"prefers X","confidence":0.6,"evidence":"retro:%d"}]}`, retroID)
	srv := learnOllama(t, cands, func(string) []float32 { return []float32{1} })
	emb := memory.NewOllamaEmbedder(srv.URL, "test-embed")
	dig := &learn.Digester{BaseURL: srv.URL, Model: "test-model"}

	out, err := runLearnDigest(ctx, a, store, emb, dig, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "would learn") || !strings.Contains(out, "dry run") {
		t.Fatalf("dry run must narrate without writing:\n%s", out)
	}
	if items, _ := store.TopByKind(ctx, memory.KindUserPreference, 10); len(items) != 0 {
		t.Fatalf("dry run must write nothing, got %+v", items)
	}
	if left, _ := store.UnconsumedByKind(ctx, memory.KindRetrospective); len(left) != 1 {
		t.Fatal("dry run must leave retrospectives unconsumed")
	}
}

func TestRunLearnDigestFailsLoudWithoutOllama(t *testing.T) {
	a, store, _ := learnFixture(t)
	emb := memory.NewOllamaEmbedder("http://127.0.0.1:1", "test-embed")
	dig := &learn.Digester{BaseURL: "http://127.0.0.1:1", Model: "test-model"}
	if _, err := runLearnDigest(context.Background(), a, store, emb, dig, false); err == nil {
		t.Fatal("ollama down must fail the digest loudly")
	}
	// Nothing partial was written and nothing was consumed.
	if items, _ := store.TopByKind(context.Background(), memory.KindUserPreference, 10); len(items) != 0 {
		t.Fatal("failed digest must write nothing")
	}
	if left, _ := store.UnconsumedByKind(context.Background(), memory.KindRetrospective); len(left) != 1 {
		t.Fatal("failed digest must consume nothing")
	}
}
