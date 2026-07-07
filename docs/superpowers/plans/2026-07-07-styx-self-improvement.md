# Styx Self-Improvement (Learning Loop) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Styx learns from three feeds — mechanical dispatch outcomes, conductor retrospectives, and explicit user preferences — via a manual `styx learn` verb that digests them (local ollama) into plain-text memories with provenance, applied only through launch-time guidance injection. Inspectable (`--list`), reversible (`--forget`), additive (never code, never routing.toml).

**Architecture:** A new `internal/learn` package holds the deterministic scorecard and the ollama digest client (mirroring `internal/brain`'s httptest-able chat shape). Memory kinds and store methods extend `internal/memory`. The verb lives in `cmd/styx/learn.go`; application is two sections in `conductorGuidance` (`cmd/styx/launch.go`). Spec: `docs/superpowers/specs/2026-07-07-styx-self-improvement-design.md` (Decisions and Non-goals are settled — do not redesign).

**⚠️ Dependency:** This plan requires the async-dispatch plan (`docs/superpowers/plans/2026-07-07-styx-async-dispatch.md`) **Tasks 1–3 landed first**: the `outcomes` table with `Tracker.RecordOutcome` / `Tracker.RateOutcome` / `Tracker.OutcomesSince` and the `rate_dispatch` tool are this plan's input feed. Task 7 here also assumes that plan's Task 11 (guidance seed at V4-current with `seedV3` retained) — if executing before it, adjust the retained-seed constant numbering accordingly.

**Tech Stack:** Go stdlib only (no new deps), `modernc.org/sqlite` via existing packages, `httptest` for the ollama digester and embedder, seeded sqlite fixtures via `budget.New` in temp dirs.

## Global Constraints

- **The prime constraint (spec):** all learning lands as plain-text memories with provenance — never code changes, never edits to `routing.toml`. Nothing in this plan writes routing.toml.
- No background/daemon learning: the digest runs only inside `styx learn`.
- Ollama down → `styx learn` fails loudly; **nothing partial is written** (all embeds/dedupe checks happen before any write).
- Pure Go, no cgo, no provider SDKs. Never swallow errors (`fmt.Errorf("...: %w", err)`); best-effort is allowed only where the codebase already does it (guidance recall enhancement, narrated via `logStatus`).
- All file writes atomic (tmp + rename). Never break `styx auto --resume` (nothing here touches pipeline state).
- In `styx mcp`, stdout carries JSON-RPC exclusively.
- **Drift contract:** every task updates the named `docs/ARCHITECTURE.md` sections **in the same commit** and bumps `last_verified`. Task 4 also updates README.md's verb table and `cmd/styx/help.go` (new verb `learn`).
- Before every commit: `go vet ./... && gofmt -w .` and `make test` (full suite).
- Hermetic tests only (httptest ollama, temp sqlite). No live AI calls except the optional `STYX_E2E_LIVE=1` / manual-digest checks in post-plan verification.

## Pinned plan-time decisions (spec left these open)

1. **Global store for learning state:** `retrospective` and `user-preference` memories live in `global.db` (they describe the user/session, not one repo), matching where `routing-preference` recall already reads. `memory_save` routes those two kinds to a lazily-opened global store; other kinds keep the project store.
2. **Rating notes window:** the digest consumes rating notes from the same trailing-30-day outcomes window as the scorecard (no "last run" state file). Retrospectives alone carry consumed-marking; re-seen rating notes are harmless because of the dedupe + 5-candidate cap.
3. **Evidence citation format (mechanical guard):** a candidate's `evidence` is exactly `scorecard:<cli>/<signal>` (must match a real scorecard cell) or `retro:<id>` (must match an unconsumed retrospective id). Anything else — including out-of-range confidence — is dropped, and drops are printed.
4. **Provenance encoding:** `Source: "styx-learn"` column, `CreatedAt` as the date, and the citation appended to the text: `<sentence> [learned-by styx-learn 2026-07-07; evidence: scorecard:codex/complex]` — visible verbatim in `--list` and in injected guidance.
5. **Dedupe:** same-kind cosine similarity ≥ 0.9 against the global store → `UpdateEvidence` on the existing row (new text + refreshed `created_at`) instead of a new row.
6. **Launch injection ranking:** both learned sections use `Store.TopByKind` — confidence × recency (the recall decay curve with similarity fixed at 1), top 5. `recallRoutingPrefs` switches from embedding-recall to `TopByKind` too: it becomes kind-exact (embedding recall would cross-match the new user-preference texts) and works with ollama down. This implements the spec's "top-5 by recency + confidence" application mechanism.

---

### Task 1: memory substrate — new kinds, consumed-marking, learning store methods

**Files:**
- Modify: `internal/memory/store.go`
- Test: `internal/memory/store_test.go`

**Interfaces:**
- Produces (later tasks rely on these exact names):
  - `const KindUserPreference Kind = "user-preference"`, `const KindRetrospective Kind = "retrospective"`
  - `Item.ConsumedAt time.Time` (zero = unconsumed)
  - `func (s *Store) TopByKind(ctx context.Context, kind Kind, k int) ([]Item, error)` — confidence × recency ranking.
  - `func (s *Store) UnconsumedByKind(ctx context.Context, kind Kind) ([]Item, error)` — oldest first.
  - `func (s *Store) MarkConsumed(ctx context.Context, ids []int64) error`
  - `func (s *Store) Delete(ctx context.Context, id int64) error`
  - `func (s *Store) UpdateEvidence(ctx context.Context, id int64, text string) error` — rewrites text, refreshes `created_at`.
  - `func (s *Store) MostSimilar(ctx context.Context, kind Kind, vec []float32) (Item, float64, error)`

- [ ] **Step 1: Write the failing tests**

Add to `internal/memory/store_test.go` (the file already opens stores in temp dirs — mirror `TestStoreAddAndAll`'s setup):

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/memory/ -run 'TestTopByKind|TestConsumedMarking|TestDeleteAndUpdate|TestMostSimilar' -v`
Expected: FAIL — `undefined: KindUserPreference`, `s.TopByKind undefined`

- [ ] **Step 3: Implement**

In `internal/memory/store.go`:

1. Kinds:

```go
const (
	KindDecision          Kind = "decision"
	KindTodo              Kind = "todo"
	KindDistillation      Kind = "distillation"
	KindBrief             Kind = "brief"
	KindFact              Kind = "fact"
	KindRoutingPreference Kind = "routing-preference"
	KindUserPreference    Kind = "user-preference" // how the user likes to work (styx learn)
	KindRetrospective     Kind = "retrospective"   // raw session notes; digest fuel, never injected
)
```

2. `Item` gains:

```go
	ConsumedAt time.Time // retrospectives: when the digest consumed this (zero = unconsumed)
```

3. Schema + migration: append to the `schema` const's column list `consumed_at  INTEGER NOT NULL DEFAULT 0` (before the closing `);`), and add to `migrate`'s `want` map:

```go
		"consumed_at":  "INTEGER NOT NULL DEFAULT 0",
```

4. `All` must scan the new column — change its SELECT to

```go
		`SELECT id, kind, text, source, project, scope, confidence, embedding, created_at, last_used_at, consumed_at FROM memory ORDER BY id DESC`
```

and its scan loop to

```go
		var ts, lastUsed, consumed int64
		if err := rows.Scan(&it.ID, &kind, &it.Text, &it.Source, &it.Project, &it.Scope, &it.Confidence, &blob, &ts, &lastUsed, &consumed); err != nil {
			return nil, fmt.Errorf("scan memory item: %w", err)
		}
		...
		if consumed != 0 {
			it.ConsumedAt = time.Unix(consumed, 0)
		}
```

(`Add` keeps inserting the explicit column list without `consumed_at`; the DEFAULT 0 covers it.)

5. New methods (append to the file):

```go
// TopByKind returns up to k items of kind ranked by confidence × recency —
// the launch-guidance ranking for learned preferences (the recall decay
// curve with similarity fixed at 1). Newer, more confident memories outrank
// older ones, so preference drift resolves itself.
func (s *Store) TopByKind(ctx context.Context, kind Kind, k int) ([]Item, error) {
	items, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si := decayedScore(1, out[i].Confidence, time.Since(out[i].CreatedAt))
		sj := decayedScore(1, out[j].Confidence, time.Since(out[j].CreatedAt))
		return si > sj
	})
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// UnconsumedByKind returns items of kind not yet marked consumed, oldest
// first (digest order).
func (s *Store) UnconsumedByKind(ctx context.Context, kind Kind) ([]Item, error) {
	items, err := s.All(ctx) // newest first
	if err != nil {
		return nil, err
	}
	var out []Item
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == kind && items[i].ConsumedAt.IsZero() {
			out = append(out, items[i])
		}
	}
	return out, nil
}

// MarkConsumed stamps consumed_at on the given items so future digests skip
// them. An empty id list is a no-op.
func (s *Store) MarkConsumed(ctx context.Context, ids []int64) error {
	now := time.Now().Unix()
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE memory SET consumed_at = ? WHERE id = ?`, now, id); err != nil {
			return fmt.Errorf("mark memory %d consumed: %w", id, err)
		}
	}
	return nil
}

// Delete removes one item — styx learn --forget. Unknown ids error loudly.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM memory WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete memory %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("delete memory %d: no such memory (see styx learn --list)", id)
	}
	return nil
}

// UpdateEvidence rewrites an item's text and refreshes created_at — the
// digest's dedupe path: a re-learned memory gets fresher evidence and
// recency instead of a duplicate row.
func (s *Store) UpdateEvidence(ctx context.Context, id int64, text string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE memory SET text = ?, created_at = ? WHERE id = ?`, text, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("update memory %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update memory %d: no such memory", id)
	}
	return nil
}

// MostSimilar returns the same-kind item with the highest cosine similarity
// to vec. Similarity 0 (zero Item) when the store holds no items of kind.
func (s *Store) MostSimilar(ctx context.Context, kind Kind, vec []float32) (Item, float64, error) {
	items, err := s.All(ctx)
	if err != nil {
		return Item{}, 0, err
	}
	var best Item
	bestSim := 0.0
	for _, it := range items {
		if it.Kind != kind {
			continue
		}
		if sim := cosine(vec, it.Embedding); sim > bestSim {
			best, bestSim = it, sim
		}
	}
	return best, bestSim, nil
}
```

Add `"sort"` to the imports (`cosine` and `decayedScore` live in `recall.go`, same package).

- [ ] **Step 4: Run tests**

Run: `go test ./internal/memory/ -v` → PASS (all — the schema/`All` change must not disturb existing store/recall tests; an old DB fixture gains `consumed_at` via `migrate`).

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Memory (internal/memory)": add the two kinds (retrospective marked input-only/never injected), the `consumed_at` column + additive migration, and the learning store methods (`TopByKind` ranking rule, `MostSimilar` dedupe seam, `Delete`/`UpdateEvidence`/`MarkConsumed`). Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(memory): user-preference + retrospective kinds with consumed-marking and learning store methods"
```

---

### Task 2: `memory_save` accepts the new kinds and routes them to the global store

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (`memory_save` handler + schema; `conductorDeps` gains a lazy global store)
- Test: `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Consumes: `memory.KindUserPreference`/`KindRetrospective` (Task 1).
- Produces: `func (d *conductorDeps) globalMem() (*memory.Store, error)` — lazy, cached, mutex-guarded; `memory_save(kind=user-preference|retrospective)` writes to `global.db` with `Scope: "global"`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/styx/mcp_conductor_test.go` (imports `memory` — add `github.com/ishaanbatra/styx/internal/memory` if absent):

```go
func TestMemorySaveRoutesLearningKindsToGlobalStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &conductorDeps{
		gate:     shipgate.New(shipgate.ModeOff),
		emb:      replEmbedder{},
		managers: map[string]*managed{},
	}
	res, err := callTool(t, d, "memory_save", map[string]any{
		"kind": "retrospective", "text": "worked: codex on specced tasks / didn't: agy timeouts",
	})
	if err != nil {
		t.Fatalf("memory_save retrospective: %v", err)
	}
	if res["saved"] != true {
		t.Fatalf("want saved=true, got %v", res)
	}
	if _, err := callTool(t, d, "memory_save", map[string]any{
		"kind": "user-preference", "text": "prefers table-driven tests",
	}); err != nil {
		t.Fatalf("memory_save user-preference: %v", err)
	}

	// Both landed in global.db — not a project store (note: no project was
	// even resolvable here; global kinds must not require one).
	memDir, err := paths.MemoryDir()
	if err != nil {
		t.Fatal(err)
	}
	glob, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer glob.Close()
	items, err := glob.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 global items, got %d", len(items))
	}
	kinds := map[memory.Kind]bool{items[0].Kind: true, items[1].Kind: true}
	if !kinds[memory.KindRetrospective] || !kinds[memory.KindUserPreference] {
		t.Fatalf("wrong kinds in global store: %+v", items)
	}
	if items[0].Scope != "global" {
		t.Fatalf("learning kinds default to global scope, got %q", items[0].Scope)
	}
}
```

(Add the `paths` import — `github.com/ishaanbatra/styx/internal/paths` — to the test file if absent.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestMemorySaveRoutesLearningKinds -v`
Expected: FAIL — `unknown kind "retrospective"`

- [ ] **Step 3: Implement**

`conductorDeps` gains a cached global store (field next to `managers`):

```go
	gmem     *memory.Store // lazy global.db handle for learning kinds
```

```go
// globalMem lazily opens the shared global memory store. user-preference and
// retrospective memories describe the user, not a repo, so they live in
// global.db — the same store launch-time guidance injection reads.
func (d *conductorDeps) globalMem() (*memory.Store, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gmem != nil {
		return d.gmem, nil
	}
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, err
	}
	s, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		return nil, fmt.Errorf("open global memory: %w", err)
	}
	d.gmem = s
	return s, nil
}
```

In the `memory_save` tool: schema enum becomes

```go
					"kind":    map[string]any{"type": "string", "enum": []string{"fact", "decision", "todo", "routing-preference", "user-preference", "retrospective"}},
```

Handler: extend the kind switch and fork the store:

```go
				switch memory.Kind(in.Kind) {
				case memory.KindFact, memory.KindDecision, memory.KindTodo, memory.KindRoutingPreference,
					memory.KindUserPreference, memory.KindRetrospective:
				default:
					return nil, fmt.Errorf("unknown kind %q", in.Kind)
				}
				if in.Text == "" {
					return nil, fmt.Errorf("text is required")
				}
				kind := memory.Kind(in.Kind)
				store := (*memory.Store)(nil)
				projectName := ""
				defaultScope := "project"
				if kind == memory.KindUserPreference || kind == memory.KindRetrospective {
					// Learning kinds are about the user/session, not one repo:
					// they live in global.db (no project resolution needed).
					g, err := d.globalMem()
					if err != nil {
						return nil, err
					}
					store, defaultScope = g, "global"
				} else {
					m, err := d.managerFor(in.Project)
					if err != nil {
						return nil, err
					}
					store, projectName = m.mem, m.mgr.Project.Name
				}
				vec, err := d.emb.Embed(ctx, in.Text)
				if err != nil {
					return nil, fmt.Errorf("embed (is ollama up?): %w", err)
				}
				scope := in.Scope
				if scope == "" {
					scope = defaultScope
				}
				id, err := store.Add(ctx, memory.Item{
					Kind: kind, Text: in.Text, Source: "conductor",
					Project: projectName, Scope: scope, Confidence: 0.9, Embedding: vec,
				})
				if err != nil {
					return nil, fmt.Errorf("save memory: %w", err)
				}
				return map[string]any{"saved": true, "id": id}, nil
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestMemorySave -v` → PASS (including the existing `TestMemorySaveValidatesKind`)

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Conductor MCP tools" `memory_save` bullet: the two new kinds, their global-store routing, and the `globalMem` lazy handle. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(mcp): memory_save accepts user-preference + retrospective, routed to the global store"
```

---

### Task 3: scorecard — deterministic aggregation over outcomes (internal/learn)

**Files:**
- Create: `internal/learn/scorecard.go`
- Create: `internal/learn/scorecard_test.go`

**Interfaces:**
- Consumes: `budget.Outcome` (async-dispatch plan Task 1).
- Produces:
  - `type Cell struct { CLI, Signal string; Attempts, Clean int; MedianDurationS float64; MedianTokens int; Good, Bad int }`
  - `type Scorecard struct { WindowDays int; Cells []Cell }` — cells sorted by CLI then Signal.
  - `func Build(rows []budget.Outcome, windowDays int) Scorecard`
  - `func (s Scorecard) Render() string`
  - `func (s Scorecard) HasCell(cli, signal string) bool` — the evidence guard's ground truth.

- [ ] **Step 1: Write the failing tests**

Create `internal/learn/scorecard_test.go` — table-driven over a real seeded sqlite fixture (per the spec's testing section), exercising grouping, signal explosion, clean-rate, medians, and rating tallies:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/learn/ -v`
Expected: FAIL — package doesn't exist / `undefined: Build`

- [ ] **Step 3: Implement**

Create `internal/learn/scorecard.go`:

```go
// Package learn implements styx's self-improvement loop: a deterministic
// scorecard over dispatch outcomes, and an ollama-backed digest that turns
// the scorecard + session retrospectives into plain-text preference memories
// with provenance. Learning is additive and inspectable — never code
// changes, never routing.toml edits (the transparent table is absolute).
package learn

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ishaanbatra/styx/internal/budget"
)

// Cell is one cli × signal aggregate of the trailing outcome window.
type Cell struct {
	CLI     string
	Signal  string // "(none)" for signal-less dispatches
	Attempts int
	Clean    int // no classified error AND not rated bad
	MedianDurationS float64
	MedianTokens    int // median of tokens_in + tokens_out
	Good, Bad       int // rating tallies
}

// Scorecard is the deterministic layer of styx learn: pure aggregation, no
// LLM involvement, independently useful via styx learn --scorecard. The
// digest consumes it as ground truth (evidence guard).
type Scorecard struct {
	WindowDays int
	Cells      []Cell // sorted by CLI, then Signal
}

// Build aggregates outcome rows into cli × signal cells. A row with N
// signals contributes to N cells; a row with none lands in "(none)".
func Build(rows []budget.Outcome, windowDays int) Scorecard {
	type acc struct {
		cell      Cell
		durations []float64
		tokens    []int
	}
	byKey := map[string]*acc{}
	for _, o := range rows {
		sigs := strings.Split(o.Signals, ",")
		var clean []string
		for _, s := range sigs {
			if s = strings.TrimSpace(s); s != "" {
				clean = append(clean, s)
			}
		}
		if len(clean) == 0 {
			clean = []string{"(none)"}
		}
		for _, sig := range clean {
			key := o.CLI + "\x00" + sig
			a, ok := byKey[key]
			if !ok {
				a = &acc{cell: Cell{CLI: o.CLI, Signal: sig}}
				byKey[key] = a
			}
			a.cell.Attempts++
			if o.ErrorKind == "" && o.Rating != "bad" {
				a.cell.Clean++
			}
			switch o.Rating {
			case "good":
				a.cell.Good++
			case "bad":
				a.cell.Bad++
			}
			a.durations = append(a.durations, o.DurationS)
			a.tokens = append(a.tokens, o.TokensIn+o.TokensOut)
		}
	}
	sc := Scorecard{WindowDays: windowDays}
	for _, a := range byKey {
		a.cell.MedianDurationS = medianF(a.durations)
		a.cell.MedianTokens = medianI(a.tokens)
		sc.Cells = append(sc.Cells, a.cell)
	}
	sort.Slice(sc.Cells, func(i, j int) bool {
		if sc.Cells[i].CLI != sc.Cells[j].CLI {
			return sc.Cells[i].CLI < sc.Cells[j].CLI
		}
		return sc.Cells[i].Signal < sc.Cells[j].Signal
	})
	return sc
}

// HasCell reports whether a cli × signal line exists — the mechanical
// evidence guard's ground truth for "scorecard:<cli>/<signal>" citations.
func (s Scorecard) HasCell(cli, signal string) bool {
	for _, c := range s.Cells {
		if c.CLI == cli && c.Signal == signal {
			return true
		}
	}
	return false
}

// Render prints one line per cell, stable order.
func (s Scorecard) Render() string {
	if len(s.Cells) == 0 {
		return fmt.Sprintf("scorecard (trailing %dd): no dispatch outcomes yet", s.WindowDays)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "scorecard (trailing %dd, cli × signal):\n", s.WindowDays)
	for _, c := range s.Cells {
		pct := 0
		if c.Attempts > 0 {
			pct = c.Clean * 100 / c.Attempts
		}
		fmt.Fprintf(&b, "  %s × %s: %d/%d clean (%d%%), median %.1fs, %d tok, rated +%d/-%d\n",
			c.CLI, c.Signal, c.Clean, c.Attempts, pct, c.MedianDurationS, c.MedianTokens, c.Good, c.Bad)
	}
	return b.String()
}

func medianF(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	if n := len(v); n%2 == 1 {
		return v[n/2]
	} else {
		return (v[n/2-1] + v[n/2]) / 2
	}
}

func medianI(v []int) int {
	if len(v) == 0 {
		return 0
	}
	sort.Ints(v)
	if n := len(v); n%2 == 1 {
		return v[n/2]
	} else {
		return (v[n/2-1] + v[n/2]) / 2
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/learn/ -v` → PASS

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md`: add a new `## Learning (internal/learn)` section (after "Memory"): package role, the scorecard as the deterministic no-LLM layer, cell semantics (clean = no classified error and not rated bad; multi-signal explosion; "(none)"). Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(learn): deterministic scorecard aggregation over dispatch outcomes"
```

---

### Task 4: `styx learn` verb — `--scorecard`, `--list`, `--forget` (no LLM yet)

**Files:**
- Create: `cmd/styx/learn.go`
- Create: `cmd/styx/learn_test.go`
- Modify: `cmd/styx/dispatch.go` (verb switch, second tier — needs the app)
- Modify: `cmd/styx/help.go`, `README.md` (verb table)

**Interfaces:**
- Consumes: `Tracker.OutcomesSince`, `learn.Build/Render`, `memory.Store.TopByKind/Delete` (Tasks 1, 3).
- Produces:
  - Verb `learn` in the app-tier switch → `cmdLearn(a *app, args []string) error`.
  - Testable helpers: `learnScorecard(ctx, a) (string, error)`, `learnList(ctx, store *memory.Store) (string, error)`, `learnForget(ctx, store *memory.Store, id int64) (string, error)`, `openGlobalMemory() (*memory.Store, error)`.
  - The digest branch errors with "not implemented" until Task 6 replaces it.

- [ ] **Step 1: Write the failing tests**

Create `cmd/styx/learn_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run TestLearn -v`
Expected: FAIL — `undefined: learnScorecard`, `undefined: cmdLearn`

- [ ] **Step 3: Implement**

Create `cmd/styx/learn.go`:

```go
package main

// styx learn — the self-improvement digest verb (spec
// docs/superpowers/specs/2026-07-07-styx-self-improvement-design.md).
// Manual only: no daemons, no background learning. All learning lands as
// plain-text memories with provenance in the global memory store — never
// code changes, never routing.toml edits.

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/learn"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
)

// scorecardWindow is the trailing outcome window styx learn aggregates.
const scorecardWindow = 30 * 24 * time.Hour

func cmdLearn(a *app, args []string) error {
	var scorecardOnly, dryRun, list bool
	var forgetID int64
	var forget bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--scorecard":
			scorecardOnly = true
		case "--dry-run":
			dryRun = true
		case "--list":
			list = true
		case "--forget":
			i++
			if i >= len(args) {
				return fmt.Errorf("--forget needs a memory id (styx learn --list shows ids)")
			}
			id, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil {
				return fmt.Errorf("--forget: bad id %q: %w", args[i], err)
			}
			forgetID, forget = id, true
		default:
			return fmt.Errorf("unknown flag %q (usage: styx learn [--scorecard|--dry-run|--list|--forget <id>])", args[i])
		}
	}
	ctx := context.Background()
	if scorecardOnly {
		out, err := learnScorecard(ctx, a)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	store, err := openGlobalMemory()
	if err != nil {
		return err
	}
	defer store.Close()
	switch {
	case list:
		out, err := learnList(ctx, store)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	case forget:
		out, err := learnForget(ctx, store, forgetID)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	return runLearn(ctx, a, store, dryRun) // Task 6 implements the digest
}

// runLearn is the digest entry point; replaced by Task 6.
func runLearn(ctx context.Context, a *app, store *memory.Store, dryRun bool) error {
	return fmt.Errorf("styx learn digest not implemented yet — use --scorecard, --list, or --forget")
}

// openGlobalMemory opens ~/.config/styx/state/memory/global.db — where
// learned memories live (the store launch guidance injection reads).
func openGlobalMemory() (*memory.Store, error) {
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, err
	}
	return memory.Open(filepath.Join(memDir, "global.db"))
}

// learnScorecard renders the deterministic cli × signal scorecard over the
// trailing 30 days of dispatch outcomes. No LLM involvement.
func learnScorecard(ctx context.Context, a *app) (string, error) {
	rows, err := a.tracker.OutcomesSince(ctx, time.Now().Add(-scorecardWindow))
	if err != nil {
		return "", fmt.Errorf("read outcomes: %w", err)
	}
	return learn.Build(rows, 30).Render(), nil
}

// learnList renders the learned set (routing + user preferences) with ids
// and provenance so --forget has addressable targets.
func learnList(ctx context.Context, store *memory.Store) (string, error) {
	var b strings.Builder
	total := 0
	for _, kind := range []memory.Kind{memory.KindRoutingPreference, memory.KindUserPreference} {
		items, err := store.TopByKind(ctx, kind, 100)
		if err != nil {
			return "", fmt.Errorf("list %s memories: %w", kind, err)
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s:\n", kind)
		for _, it := range items {
			fmt.Fprintf(&b, "  [%d] %s (source %s, %s, conf %.2f)\n",
				it.ID, it.Text, it.Source, it.CreatedAt.Format("2006-01-02"), it.Confidence)
		}
		total += len(items)
	}
	if total == 0 {
		return "no learned memories yet — dispatch some work, then run styx learn\n", nil
	}
	return b.String(), nil
}

// learnForget hard-deletes one memory by id — the reversibility guarantee.
func learnForget(ctx context.Context, store *memory.Store, id int64) (string, error) {
	if err := store.Delete(ctx, id); err != nil {
		return "", err
	}
	return fmt.Sprintf("forgot memory %d\n", id), nil
}
```

In `cmd/styx/dispatch.go`, add to the **second-tier** verb switch (needs the app; place after `case "intel":`):

```go
	case "learn":
		return cmdLearn(a, args)
```

In `cmd/styx/help.go`, add under the `budget` line:

```
  learn [--scorecard|--dry-run|--list|--forget <id>]
                            Digest dispatch outcomes + retrospectives into
                            learned preferences (local ollama; reversible)
```

In `README.md`, add a row to the utilities verb table (the one containing `budget` and `doctor`):

```markdown
| `learn [--scorecard\|--dry-run\|--list\|--forget <id>]` | Digest dispatch outcomes + session retrospectives into learned routing/user preferences — plain-text memories with provenance, injected into conductor guidance; `--list` inspects, `--forget` reverses |
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestLearn -v` → PASS
Run: `make build && ./bin/styx learn --scorecard` → prints a scorecard (or "no dispatch outcomes yet")
Run: `./bin/styx learn --list` → prints the learned set or the empty-set line

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md`: "cmd/styx — verbs and app wiring" gains a `learn.go` bullet (flags, global-store target, digest deferred to the next commits); "Learning" section gains the verb surface. README verb table + help text updated above. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(cli): styx learn verb with --scorecard, --list, --forget (digest wired next)"
```

---

### Task 5: digest client + evidence guard (internal/learn)

**Files:**
- Create: `internal/learn/digest.go`
- Create: `internal/learn/digest_test.go`

**Interfaces:**
- Produces:
  - `type Candidate struct { Kind, Text string; Confidence float64; Evidence string }` (JSON tags `kind,text,confidence,evidence`)
  - `type RetroNote struct { ID int64; Text string }`
  - `type Digester struct { BaseURL, Model string }` with `func (d *Digester) Propose(ctx context.Context, scorecard string, retros []RetroNote, ratingNotes []string) ([]Candidate, error)` — structured-output ollama chat, loud error when unreachable.
  - `func FilterByEvidence(cands []Candidate, sc Scorecard, retros []RetroNote) (kept []Candidate, dropped []string)` — cap 5, kind whitelist, confidence clamp, citation-must-be-real.

- [ ] **Step 1: Write the failing tests**

Create `internal/learn/digest_test.go`:

```go
package learn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
)

// scriptedOllama returns an httptest server answering /api/chat with the
// given candidates payload, capturing the request for assertions.
func scriptedOllama(t *testing.T, candidates string) (*httptest.Server, *map[string]any) {
	t.Helper()
	captured := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": candidates},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func TestDigesterPropose(t *testing.T) {
	srv, captured := scriptedOllama(t, `{"candidates":[
		{"kind":"routing-preference","text":"codex for specced work","confidence":0.8,"evidence":"scorecard:codex/complex"}
	]}`)
	d := &Digester{BaseURL: srv.URL, Model: "qwen2.5-coder:7b"}
	got, err := d.Propose(context.Background(),
		"scorecard text", []RetroNote{{ID: 7, Text: "codex nailed it"}}, []string{"good (codex): clean"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "routing-preference" || got[0].Evidence != "scorecard:codex/complex" {
		t.Fatalf("candidates mismatch: %+v", got)
	}
	// The request is a schema-constrained, non-thinking, keep-alive chat and
	// the prompt carries all three feeds.
	req := *captured
	if req["think"] != false || req["keep_alive"] != "30m" || req["format"] == nil {
		t.Fatalf("chat request must mirror the brain's shape (think=false, keep_alive, format), got %v", req)
	}
	msgs, _ := req["messages"].([]any)
	user, _ := msgs[1].(map[string]any)["content"].(string)
	for _, want := range []string{"scorecard text", "retro:7", "codex nailed it", "good (codex): clean"} {
		if !strings.Contains(user, want) {
			t.Fatalf("prompt missing %q:\n%s", want, user)
		}
	}
}

func TestDigesterFailsLoudWhenOllamaDown(t *testing.T) {
	d := &Digester{BaseURL: "http://127.0.0.1:1", Model: "m"} // nothing listens
	if _, err := d.Propose(context.Background(), "sc", nil, nil); err == nil {
		t.Fatal("unreachable ollama must be a loud error")
	}
}

func TestFilterByEvidence(t *testing.T) {
	sc := Build([]budget.Outcome{{CLI: "codex", Signals: "complex"}}, 30)
	retros := []RetroNote{{ID: 7, Text: "note"}}
	cands := []Candidate{
		{Kind: "routing-preference", Text: "keep", Confidence: 0.8, Evidence: "scorecard:codex/complex"},
		{Kind: "user-preference", Text: "keep too", Confidence: 0.5, Evidence: "retro:7"},
		{Kind: "routing-preference", Text: "fabricated cell", Confidence: 0.9, Evidence: "scorecard:agy/huge"},
		{Kind: "user-preference", Text: "fabricated retro", Confidence: 0.9, Evidence: "retro:99"},
		{Kind: "decision", Text: "wrong kind", Confidence: 0.9, Evidence: "retro:7"},
		{Kind: "user-preference", Text: "no evidence", Confidence: 0.9, Evidence: ""},
		{Kind: "user-preference", Text: "bad confidence", Confidence: 1.5, Evidence: "retro:7"},
		{Kind: "user-preference", Text: "", Confidence: 0.5, Evidence: "retro:7"},
	}
	kept, dropped := FilterByEvidence(cands, sc, retros)
	if len(kept) != 2 || kept[0].Text != "keep" || kept[1].Text != "keep too" {
		t.Fatalf("guard kept the wrong set: %+v", kept)
	}
	if len(dropped) != 6 {
		t.Fatalf("want 6 drop reasons, got %d: %v", len(dropped), dropped)
	}

	// The 5-candidate cap truncates even valid extras.
	many := make([]Candidate, 8)
	for i := range many {
		many[i] = Candidate{Kind: "user-preference", Text: "t", Confidence: 0.5, Evidence: "retro:7"}
	}
	kept, dropped = FilterByEvidence(many, sc, retros)
	if len(kept) != 5 {
		t.Fatalf("cap must hold at 5, got %d", len(kept))
	}
	if len(dropped) != 3 || !strings.Contains(dropped[0], "cap") {
		t.Fatalf("over-cap drops must be reported, got %v", dropped)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/learn/ -run 'TestDigester|TestFilterByEvidence' -v`
Expected: FAIL — `undefined: Digester`, `undefined: FilterByEvidence`

- [ ] **Step 3: Implement**

Create `internal/learn/digest.go` (the chat shape deliberately mirrors `internal/brain`'s `chat()` — structured output, `think: false`, `keep_alive`, num_ctx sizing — but stays self-contained; brain is REPL-coupled):

```go
package learn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Candidate is one memory the digest model proposes. The evidence guard
// (FilterByEvidence) decides whether it survives.
type Candidate struct {
	Kind       string  `json:"kind"` // routing-preference | user-preference
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	Evidence   string  `json:"evidence"` // "scorecard:<cli>/<signal>" or "retro:<id>"
}

// RetroNote is one unconsumed retrospective offered to the digest.
type RetroNote struct {
	ID   int64
	Text string
}

// maxCandidates caps what one digest run may propose — a hallucination
// bound: at worst 5 bad sentences, each still evidence-checked and printed.
const maxCandidates = 5

// candidateSchema is the ollama structured-output format for Propose.
var candidateSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"candidates": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"kind": {"type": "string", "enum": ["routing-preference", "user-preference"]},
					"text": {"type": "string"},
					"confidence": {"type": "number"},
					"evidence": {"type": "string"}
				},
				"required": ["kind", "text", "confidence", "evidence"]
			}
		}
	},
	"required": ["candidates"]
}`)

const digestSystem = `You are styx's learning digester. From a dispatch
scorecard and session notes, propose at most 5 durable memories that would
improve future routing or match how the user works. Kinds:
- routing-preference: which CLI suits which kind of work, grounded in the scorecard.
- user-preference: how the user likes to work, grounded in retrospectives or rating notes.
Each candidate needs:
- text: ONE standalone plain sentence (it will be injected into future guidance verbatim).
- confidence: 0 to 1.
- evidence: EXACTLY one citation — "scorecard:<cli>/<signal>" naming a scorecard line, or "retro:<id>" naming a retrospective id shown to you.
Propose nothing when the data is thin; fewer, stronger memories beat many weak ones.`

// Digester proposes candidate memories via the local ollama brain model.
type Digester struct {
	BaseURL string // e.g. http://localhost:11434
	Model   string // e.g. qwen2.5-coder:7b

	client *http.Client
}

func (d *Digester) httpClient() *http.Client {
	if d.client == nil {
		d.client = &http.Client{Timeout: 120 * time.Second}
	}
	return d.client
}

type digestChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type digestChatRequest struct {
	Model     string              `json:"model"`
	Stream    bool                `json:"stream"`
	Think     bool                `json:"think"`
	Format    json.RawMessage     `json:"format"`
	KeepAlive string              `json:"keep_alive,omitempty"`
	Options   map[string]any      `json:"options"`
	Messages  []digestChatMessage `json:"messages"`
}

type digestChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// Propose asks the local model for candidate memories. Fails loudly when
// ollama is unreachable or emits garbage — the caller writes nothing then.
func (d *Digester) Propose(ctx context.Context, scorecard string, retros []RetroNote, ratingNotes []string) ([]Candidate, error) {
	var u strings.Builder
	u.WriteString("Scorecard (ground truth):\n")
	u.WriteString(scorecard)
	u.WriteString("\nUnconsumed retrospectives:\n")
	if len(retros) == 0 {
		u.WriteString("  (none)\n")
	}
	for _, r := range retros {
		fmt.Fprintf(&u, "  retro:%d: %s\n", r.ID, r.Text)
	}
	u.WriteString("Rating notes (30d):\n")
	if len(ratingNotes) == 0 {
		u.WriteString("  (none)\n")
	}
	for _, n := range ratingNotes {
		fmt.Fprintf(&u, "  %s\n", n)
	}
	user := u.String()

	opts := map[string]any{"temperature": 0}
	if est := (len(digestSystem) + len(user)) / 4; est+1024 > 4096 {
		// Ollama defaults num_ctx to 4096 and silently truncates beyond it.
		opts["num_ctx"] = est + 2048
	}
	body, err := json.Marshal(digestChatRequest{
		Model:     d.Model,
		Stream:    false,
		Think:     false, // classification-shaped task; reasoning bleed breaks structured output
		Format:    candidateSchema,
		KeepAlive: "30m",
		Options:   opts,
		Messages: []digestChatMessage{
			{Role: "system", Content: digestSystem},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshal digest request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build digest request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("digest call (is ollama up?): %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read digest response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama digest %d: %s", resp.StatusCode, string(raw))
	}
	var cr digestChatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return nil, fmt.Errorf("parse digest response envelope: %w", err)
	}
	var out struct {
		Candidates []Candidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(cr.Message.Content), &out); err != nil {
		return nil, fmt.Errorf("digest model emitted invalid JSON: %w", err)
	}
	return out.Candidates, nil
}

// FilterByEvidence is the mechanical hallucination guard: candidates whose
// citation does not name a real scorecard cell or a gathered retrospective —
// or whose kind/text/confidence is malformed — are dropped before anything
// is written. Returns the survivors (at most maxCandidates) and one
// human-readable reason per drop.
func FilterByEvidence(cands []Candidate, sc Scorecard, retros []RetroNote) (kept []Candidate, dropped []string) {
	retroIDs := map[int64]bool{}
	for _, r := range retros {
		retroIDs[r.ID] = true
	}
	for _, c := range cands {
		reason := ""
		switch {
		case c.Kind != "routing-preference" && c.Kind != "user-preference":
			reason = fmt.Sprintf("kind %q is not learnable", c.Kind)
		case strings.TrimSpace(c.Text) == "":
			reason = "empty text"
		case c.Confidence <= 0 || c.Confidence > 1:
			reason = fmt.Sprintf("confidence %.2f out of (0,1]", c.Confidence)
		case strings.HasPrefix(c.Evidence, "scorecard:"):
			parts := strings.SplitN(strings.TrimPrefix(c.Evidence, "scorecard:"), "/", 2)
			if len(parts) != 2 || !sc.HasCell(parts[0], parts[1]) {
				reason = fmt.Sprintf("citation %q matches no scorecard line", c.Evidence)
			}
		case strings.HasPrefix(c.Evidence, "retro:"):
			id, err := strconv.ParseInt(strings.TrimPrefix(c.Evidence, "retro:"), 10, 64)
			if err != nil || !retroIDs[id] {
				reason = fmt.Sprintf("citation %q matches no retrospective", c.Evidence)
			}
		default:
			reason = fmt.Sprintf("citation %q is neither scorecard:<cli>/<signal> nor retro:<id>", c.Evidence)
		}
		if reason != "" {
			dropped = append(dropped, fmt.Sprintf("%q — %s", c.Text, reason))
			continue
		}
		if len(kept) >= maxCandidates {
			dropped = append(dropped, fmt.Sprintf("%q — over the %d-candidate cap", c.Text, maxCandidates))
			continue
		}
		kept = append(kept, c)
	}
	return kept, dropped
}
```

Add `"strconv"` to the imports.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/learn/ -v` → PASS

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Learning" section: the digest client (mirrors the brain's chat shape; why it is separate from `internal/brain`), the candidate schema, the evidence-citation grammar, and the guard's drop rules + cap. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(learn): ollama digest client with structured candidates and mechanical evidence guard"
```

---

### Task 6: the full digest — dedupe, provenance writes, consumed-marking, `--dry-run`

**Files:**
- Modify: `cmd/styx/learn.go` (replace the `runLearn` stub)
- Test: `cmd/styx/learn_test.go`

**Interfaces:**
- Consumes: everything from Tasks 1, 3, 5; `memory.NewOllamaEmbedder`, `memory.Embedder`.
- Produces: `func runLearnDigest(ctx context.Context, a *app, store *memory.Store, emb memory.Embedder, dig *learn.Digester, dryRun bool) (string, error)` — `runLearn` becomes a thin production wrapper wiring the real embedder + digester.

- [ ] **Step 1: Write the failing tests**

Add to `cmd/styx/learn_test.go` (new imports: `"encoding/json"`, `"fmt"`, `"net/http"`, `"net/http/httptest"`, `"github.com/ishaanbatra/styx/internal/learn"`):

```go
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
		Text: "codex for complex work [learned-by styx-learn 2026-06-01; evidence: scorecard:codex/complex]",
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run TestRunLearnDigest -v`
Expected: FAIL — `undefined: runLearnDigest`

- [ ] **Step 3: Implement**

In `cmd/styx/learn.go`, replace the `runLearn` stub:

```go
// dedupeSimilarity is the same-kind cosine threshold above which a candidate
// refreshes an existing memory instead of adding a new row.
const dedupeSimilarity = 0.9

// runLearn wires the production digester (local ollama brain model) and
// embedder into runLearnDigest.
func runLearn(ctx context.Context, a *app, store *memory.Store, dryRun bool) error {
	emb := memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel)
	dig := &learn.Digester{BaseURL: "http://localhost:11434", Model: a.routing.Brain.Model}
	out, err := runLearnDigest(ctx, a, store, emb, dig, dryRun)
	if err != nil {
		return err
	}
	fmt.Print(out)
	return nil
}

// runLearnDigest is one digest pass: scorecard → gather → propose → evidence
// guard → dedupe → write with provenance → mark retrospectives consumed.
// All embeds and dedupe decisions happen BEFORE any write, so an ollama
// failure mid-pass writes nothing partial. Dry-run stops after planning.
func runLearnDigest(ctx context.Context, a *app, store *memory.Store, emb memory.Embedder, dig *learn.Digester, dryRun bool) (string, error) {
	rows, err := a.tracker.OutcomesSince(ctx, time.Now().Add(-scorecardWindow))
	if err != nil {
		return "", fmt.Errorf("read outcomes: %w", err)
	}
	sc := learn.Build(rows, 30)
	retroItems, err := store.UnconsumedByKind(ctx, memory.KindRetrospective)
	if err != nil {
		return "", fmt.Errorf("gather retrospectives: %w", err)
	}
	retros := make([]learn.RetroNote, len(retroItems))
	for i, it := range retroItems {
		retros[i] = learn.RetroNote{ID: it.ID, Text: it.Text}
	}
	var notes []string
	for _, o := range rows {
		if o.Rating != "" && o.Note != "" {
			notes = append(notes, fmt.Sprintf("%s (%s): %s", o.Rating, o.CLI, o.Note))
		}
	}

	cands, err := dig.Propose(ctx, sc.Render(), retros, notes)
	if err != nil {
		return "", err // loud; nothing written
	}
	kept, dropped := learn.FilterByEvidence(cands, sc, retros)

	var b strings.Builder
	for _, d := range dropped {
		fmt.Fprintf(&b, "dropped: %s\n", d)
	}
	if len(kept) == 0 {
		b.WriteString("nothing to learn this round (no candidates survived the evidence guard)\n")
		return b.String(), nil
	}

	// Plan phase: embed + dedupe-check every survivor before writing anything.
	type plannedWrite struct {
		cand   learn.Candidate
		text   string
		vec    []float32
		dupeID int64 // >0: refresh this row instead of adding
	}
	date := time.Now().Format("2006-01-02")
	var plans []plannedWrite
	for _, c := range kept {
		vec, err := emb.Embed(ctx, c.Text)
		if err != nil {
			return "", fmt.Errorf("embed candidate (is ollama up?): %w", err)
		}
		p := plannedWrite{
			cand: c,
			text: fmt.Sprintf("%s [learned-by styx-learn %s; evidence: %s]", c.Text, date, c.Evidence),
			vec:  vec,
		}
		if it, sim, err := store.MostSimilar(ctx, memory.Kind(c.Kind), vec); err != nil {
			return "", fmt.Errorf("dedupe scan: %w", err)
		} else if sim >= dedupeSimilarity {
			p.dupeID = it.ID
		}
		plans = append(plans, p)
	}

	if dryRun {
		for _, p := range plans {
			verb := "would learn"
			if p.dupeID > 0 {
				verb = fmt.Sprintf("would refresh memory %d", p.dupeID)
			}
			fmt.Fprintf(&b, "%s [%s, conf %.2f]: %s\n", verb, p.cand.Kind, p.cand.Confidence, p.text)
		}
		b.WriteString("dry run: nothing written, retrospectives left unconsumed\n")
		return b.String(), nil
	}

	// Write phase.
	for _, p := range plans {
		if p.dupeID > 0 {
			if err := store.UpdateEvidence(ctx, p.dupeID, p.text); err != nil {
				return "", fmt.Errorf("refresh memory %d: %w", p.dupeID, err)
			}
			fmt.Fprintf(&b, "refreshed %d [%s]: %s\n", p.dupeID, p.cand.Kind, p.text)
			continue
		}
		id, err := store.Add(ctx, memory.Item{
			Kind: memory.Kind(p.cand.Kind), Text: p.text, Source: "styx-learn",
			Scope: "global", Confidence: p.cand.Confidence, Embedding: p.vec,
		})
		if err != nil {
			return "", fmt.Errorf("write memory: %w", err)
		}
		fmt.Fprintf(&b, "learned %d [%s]: %s\n", id, p.cand.Kind, p.text)
	}
	retroIDs := make([]int64, len(retros))
	for i, r := range retros {
		retroIDs[i] = r.ID
	}
	if err := store.MarkConsumed(ctx, retroIDs); err != nil {
		return "", fmt.Errorf("mark retrospectives consumed: %w", err)
	}
	return b.String(), nil
}
```

(`learn` is already imported from Task 4's file header; verify `memory` and `time` are too.)

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestRunLearnDigest -v` → PASS
Run: `go test ./cmd/styx/ -run TestLearn -v` → PASS (flag tests unaffected)

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Learning" section: the full digest pipeline (six spec steps), plan-before-write partial-failure rule, dedupe threshold + refresh semantics, provenance text format, consumed-marking timing. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(learn): full styx learn digest — dedupe, provenance writes, consumed retrospectives, dry-run"
```

---

### Task 7: closing the loop — guidance injection + seed nudge (seed → V5, retaining V4)

Launch-time guidance injection is the entire application mechanism: the existing routing-preferences section switches to kind-exact `TopByKind` ranking, a new user-preferences section joins it, and the conductor seed nudges retrospectives + explicit preferences.

**Files:**
- Modify: `cmd/styx/launch.go` (`conductorGuidance` gains a 5th param; `recallRoutingPrefs`/`recallUserPrefs` via `TopByKind`)
- Modify: `internal/guidance/guidance.go` (Seed edits; current Seed retained as `seedV4`)
- Test: `cmd/styx/launch_test.go`, `internal/guidance/guidance_test.go`

**Interfaces:**
- Produces:
  - `func conductorGuidance(base, focusName, extraNote, prefs, userPrefs string) string`
  - `func recallRoutingPrefs() string`, `func recallUserPrefs() string` — both via `topLearnedPrefs(kind)`; no embedder, no `*app` param (works with ollama down).
  - Seed teaches `kind=user-preference` and `kind=retrospective` saving; previous Seed retained as `const seedV4`; `Load` recognizes `seedV1 … seedV4`.

> **Numbering note:** this assumes the async-dispatch plan's Task 11 already made the current Seed the post-B1 version with `seedV3` retained. If that task has NOT landed, retain the current Seed as `seedV3` here instead and keep `Load`'s list contiguous.

- [ ] **Step 1: Write the failing tests**

In `cmd/styx/launch_test.go`, update `TestConductorGuidanceNamesFocusProject` to the 5-arg signature (`conductorGuidance("BASE", "styx", "", "", "")` and `conductorGuidance("BASE", "styx", "- ai-ta: /x (extra)\n", "- prefer codex\n", "")`), then add:

```go
func TestConductorGuidanceUserPreferencesSection(t *testing.T) {
	got := conductorGuidance("BASE", "styx", "", "- prefer codex\n", "- prefers table-driven tests\n")
	if !strings.Contains(got, "## User preferences (learned)") ||
		!strings.Contains(got, "prefers table-driven tests") {
		t.Fatalf("guidance must carry the user-preferences section:\n%s", got)
	}
	// Section order: routing prefs before user prefs.
	if strings.Index(got, "Routing preferences") > strings.Index(got, "User preferences") {
		t.Fatalf("routing prefs must precede user prefs:\n%s", got)
	}
	// Absent prefs => absent section.
	none := conductorGuidance("BASE", "styx", "", "", "")
	if strings.Contains(none, "User preferences") {
		t.Fatal("empty userPrefs must not emit the section")
	}
}

func TestRecallPrefsViaTopByKind(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	memDir, err := paths.MemoryDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.EnsureDir(memDir); err != nil {
		t.Fatal(err)
	}
	glob, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	glob.Add(ctx, memory.Item{Kind: memory.KindRoutingPreference, Text: "codex implements", Confidence: 0.9, Embedding: []float32{1}})
	glob.Add(ctx, memory.Item{Kind: memory.KindUserPreference, Text: "short summaries", Confidence: 0.9, Embedding: []float32{1}})
	glob.Close()

	// No embedder, no ollama: kind-exact recall still works.
	if got := recallRoutingPrefs(); !strings.Contains(got, "codex implements") || strings.Contains(got, "short summaries") {
		t.Fatalf("routing prefs must be kind-exact, got %q", got)
	}
	if got := recallUserPrefs(); !strings.Contains(got, "short summaries") || strings.Contains(got, "codex implements") {
		t.Fatalf("user prefs must be kind-exact, got %q", got)
	}
}
```

(Add `context`, `memory`, `paths`, `filepath` imports to `launch_test.go` as needed.)

In `internal/guidance/guidance_test.go`:

```go
func TestSeedV4UpgradesToCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := guidanceFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(seedV4), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != Seed {
		t.Fatal("an unmodified v4 seed must upgrade to the current Seed")
	}
	for _, want := range []string{"user-preference", "retrospective", "styx learn"} {
		if !strings.Contains(Seed, want) {
			t.Fatalf("current Seed must teach %q", want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run 'TestConductorGuidance|TestRecallPrefs' -v && go test ./internal/guidance/ -run TestSeedV4 -v`
Expected: FAIL — wrong arg count on `conductorGuidance`; `undefined: recallUserPrefs`; `undefined: seedV4`

- [ ] **Step 3: Implement**

`cmd/styx/launch.go`:

```go
// conductorGuidance assembles the final --append-system-prompt content:
// base guidance, the focus project's registry alias, extra-repo notes, and
// the two learned-preference sections (the entire application mechanism of
// styx learn — nothing else consumes learned state).
func conductorGuidance(base, focusName, extraNote, prefs, userPrefs string) string {
	g := base
	g += "\n\n## This session's project\n" +
		"Registry alias: `" + focusName + "`. Pass it as `project` on dispatch/" +
		"thread_status/memory_save (an empty project also resolves to this repo)."
	if extraNote != "" {
		g += "\n\n## Bound repos beyond " + focusName + "\n" + extraNote
	}
	if prefs != "" {
		g += "\n\n## Routing preferences (learned)\n" + prefs
	}
	if userPrefs != "" {
		g += "\n\n## User preferences (learned)\n" + userPrefs
	}
	return g
}
```

Replace `recallRoutingPrefs` (the embedding-recall version) with kind-exact helpers:

```go
// topLearnedPrefs renders the global store's top-5 memories of kind, ranked
// by confidence × recency (TopByKind) — kind-exact and embedder-free, so the
// launch path works even with ollama down. A pure enhancement: any failure
// is narrated via logStatus and yields "".
func topLearnedPrefs(kind memory.Kind) string {
	memDir, err := paths.MemoryDir()
	if err != nil {
		logStatus("%s recall skipped: %v", kind, err)
		return ""
	}
	if err := paths.EnsureDir(memDir); err != nil {
		logStatus("%s recall skipped: %v", kind, err)
		return ""
	}
	glob, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		logStatus("%s recall skipped: %v", kind, err)
		return ""
	}
	defer glob.Close()
	items, err := glob.TopByKind(context.Background(), kind, 5)
	if err != nil {
		logStatus("%s recall skipped: %v", kind, err)
		return ""
	}
	if len(items) == 0 {
		return ""
	}
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Text
	}
	return "- " + strings.Join(texts, "\n- ") + "\n"
}

// recallRoutingPrefs and recallUserPrefs feed the two learned guidance
// sections. Kind-exact TopByKind replaced the old embedding recall (which
// would cross-match user-preference texts against "routing preference").
func recallRoutingPrefs() string { return topLearnedPrefs(memory.KindRoutingPreference) }
func recallUserPrefs() string    { return topLearnedPrefs(memory.KindUserPreference) }
```

Update the call site in `launchConductor`:

```go
	guide = conductorGuidance(guide, p.Name, extraNote.String(), recallRoutingPrefs(), recallUserPrefs())
```

(The old `recallRoutingPrefs(a *app)` body — embedder construction and `memory.Recall` — is deleted; if `a` becomes otherwise unused in `launchConductor`'s guidance block, everything else still uses it.)

`internal/guidance/guidance.go`:

1. Copy the entire current `Seed` into `const seedV4 = …` verbatim, commented:

```go
// seedV4 is the async-dispatch-era conductor seed (shipped with B1, before
// the learning-loop nudges). Kept verbatim so Load can detect an unmodified
// v4 file and upgrade it transparently.
```

2. `Load`: `if s := string(b); s == seedV1 || s == seedV2 || s == seedV3 || s == seedV4 {`

3. Edit `Seed` — in the "## Working style" section, after the "Consult recall before re-deriving project facts" bullet, add:

```
- When the user states a durable working preference ("remember I prefer
  X", "always do Y"), memory_save it as kind=user-preference — explicit
  statements are trusted as-is, no digest needed.
- At natural session endpoints (a task shipped, a plan abandoned, the user
  wrapping up), memory_save a 2-line retrospective — what worked / what
  didn't — as kind=retrospective. These are digest fuel for `styx learn`
  and are never injected into guidance directly.
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run 'TestConductorGuidance|TestRecallPrefs' -v` → PASS
Run: `go test ./internal/guidance/ -v` → PASS
Run: `make test` → green (fix any test still calling 4-arg `conductorGuidance` or 1-arg `recallRoutingPrefs`)

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md`: "Launcher" conductor-data-flow paragraph — both learned sections, `TopByKind` ranking, the embedding→kind-exact recall switch (and why); "Guidance" section — the V4→current upgrade and the two new nudges; "Learning" section — "application: launch injection is the entire mechanism". Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(launch): learned user-preference guidance section + retrospective nudges (seed v5, v4 retained)"
```

---

### Task 8: e2e — outcome rows exist and `styx learn --scorecard` sees them

**Files:**
- Modify: `e2e/e2e_test.go`

**Interfaces:**
- Consumes: the e2e harness (`startServer`), `styx learn --scorecard` (Task 4), outcome rows written by the MCP server's dispatch (async-dispatch plan Task 2).

- [ ] **Step 1: Write the test**

Add to `e2e/e2e_test.go`:

```go
// TestLearnScorecardSeesDispatchOutcomes closes the loop hermetically: a
// dispatch through the real `styx mcp` subprocess writes an outcome row to
// the isolated usage.db, and `styx learn --scorecard` (a separate process,
// same isolated config) aggregates it. No ollama involved (--scorecard is
// the deterministic layer).
func TestLearnScorecardSeesDispatchOutcomes(t *testing.T) {
	c, proj := startServer(t)
	if _, isErr := c.toolCall("dispatch", map[string]any{
		"cli": "claude", "message": "reply ok", "risk": "read",
	}); isErr {
		t.Fatal("dispatch errored")
	}

	// Reconstruct the server's isolated env: proj is <home>/demo-proj.
	home := filepath.Dir(proj)
	repoRoot, _ := filepath.Abs("..")
	cmd := exec.Command(filepath.Join(repoRoot, "bin", "styx"), "learn", "--scorecard")
	cmd.Dir = proj
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"PATH="+filepath.Join(home, "fakebin")+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("styx learn --scorecard: %v\n%s", err, out)
	}
	// "reply ok" is ≤50 chars => the trivial signal; the row must aggregate.
	for _, want := range []string{"claude", "1/1 clean"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("scorecard must show the dispatch outcome (want %q):\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run**

Run: `make e2e`
Expected: all tests PASS including `TestLearnScorecardSeesDispatchOutcomes`; `TestLiveSmoke` SKIP.
Debug tip: if the scorecard is empty, the two processes disagree on the config path — confirm the env derivation (`home := filepath.Dir(proj)`) against `startServer`'s layout, and that WAL mode lets the second process read the fresh row (it does — `busy_timeout` is set on open).

- [ ] **Step 3: Commit**

`docs/ARCHITECTURE.md` "Testing conventions" e2e paragraph: add the learn-scorecard assertion to the described coverage. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test && make e2e
git add -A
git commit -m "test(e2e): dispatch outcome rows aggregate in styx learn --scorecard"
```

---

## Post-plan verification (whole-phase acceptance)

- [ ] `make test && make e2e` green.
- [ ] `STYX_E2E_LIVE=1 make e2e` green on this machine (the one sanctioned live run; ollama up).
- [ ] Manual loop with real ollama (local, free — not a cloud AI call):
  1. `make install`; run a few dispatches via a conductor session; `rate_dispatch` one of them with a note.
  2. In the session: "remember I prefer table-driven tests" → conductor saves `kind=user-preference`; end the session with a retrospective save.
  3. `styx learn --scorecard` shows the cells; `styx learn --dry-run` proposes ≤5 evidence-cited candidates; `styx learn` writes them and prints them; a second `styx learn` run refreshes rather than duplicates (dedupe) and consumes nothing new.
  4. `styx learn --list` shows provenance; `styx learn --forget <id>` removes one.
  5. Relaunch `styx` and confirm both learned sections appear in the guidance (ask the conductor "what learned preferences were you given?").
  6. Confirm `~/.config/styx/routing.toml` is byte-identical before/after the whole loop (`shasum` it) — the prime constraint.
- [ ] `docs/ARCHITECTURE.md` `last_verified` is the final commit date; README verb table has the `learn` row; `git log --oneline` shows one commit per task.
