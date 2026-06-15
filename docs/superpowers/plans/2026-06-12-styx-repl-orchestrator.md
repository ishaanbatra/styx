# Styx REPL Orchestrator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn styx into a persistent conversational REPL that multiplexes live agent sessions (claude/codex/agy) and local ollama models, routed per-utterance by a local ollama "brain" with structured output, backed by per-project embedding memory.

**Architecture:** Three new packages (`internal/memory`, `internal/brain`, `internal/agent`) plus a REPL frontend in `cmd/styx`. The brain (small ollama model, schema-constrained JSON) decides one Action per utterance; agent threads invoke CLIs fresh each turn with `--resume <session-id>` so durability is free; memory is SQLite + brute-force cosine over ollama embeddings. Existing channels/router/budget/intel/pipelines are kept and extended, not replaced.

**Tech Stack:** Go 1.22, `modernc.org/sqlite` (already a dep), ollama HTTP API (`/api/chat` with `format` for structured output, `/api/embed` for embeddings), claude CLI `stream-json` protocol. **No new Go module dependencies.**

**Spec:** `docs/superpowers/specs/2026-06-12-styx-repl-orchestrator-design.md`

---

## File structure

New files:

```
internal/memory/store.go          SQLite store, Item, vector blob encode/decode
internal/memory/store_test.go
internal/memory/embed.go          Embedder interface + OllamaEmbedder (/api/embed)
internal/memory/embed_test.go
internal/memory/recall.go         cosine + top-k Recall across stores
internal/memory/recall_test.go
internal/brain/action.go          Action/Dispatch types + JSON schema
internal/brain/action_test.go
internal/brain/cards.go           capability cards (curated CLI expertise)
internal/brain/prompt.go          BuildPrompt(Turn) -> system+user strings
internal/brain/prompt_test.go
internal/brain/brain.go           Brain interface, Ollama brain, ClaudeEscalator, ErrNeedUser
internal/brain/brain_test.go
internal/brain/integration_test.go  env-gated real-ollama routing accuracy test
internal/agent/event.go           Event type + ParseClaudeEvent (stream-json)
internal/agent/event_test.go
internal/agent/adapter.go         Adapter interface, ClaudeAdapter, PlainAdapter (codex/agy)
internal/agent/adapter_test.go
internal/agent/thread.go          Thread, ThreadStore (JSON persistence)
internal/agent/thread_test.go
internal/agent/runner.go          one turn: exec CLI, stream events, capture session+usage
internal/agent/runner_test.go
internal/agent/manager.go         Dispatch, distill-and-restart, crash recovery, Handoff
internal/agent/manager_test.go
cmd/styx/doctor.go                doctor verb: CLI probing, card drift, ollama model pulls
cmd/styx/doctor_test.go
cmd/styx/repl.go                  replSession: turn loop, action execution, slash commands
cmd/styx/repl_test.go
testdata/fakeagent                executable shell script speaking claude stream-json
testdata/brain/utterances.json    labeled fixture set for routing accuracy
internal/audit/log.go             append-only per-session JSONL trail (Phase 8)
internal/audit/log_test.go
```

Modified files:

```
internal/paths/paths.go           + MemoryDir(), ThreadsDir(), AuditDir() (Phase 8)
internal/config/routing.go        + BrainConfig, Tiers, ChannelCap.TimeoutMinutes, defaults
internal/budget/budget.go         + Event.Model, model column migration, ModelCount()
internal/router/router.go         + BreakerSource, unavailable() (circuit breaker wiring)
internal/channel/decorator.go     + WithTimeout decorator
internal/research/loop.go         fix silent critique-parse swallow (line 76)
cmd/styx/auto.go                  fix silent review-parse swallow (line 274)
cmd/styx/dispatch.go              defaultChannels timeout wiring, doctor verb, unknown verb -> brain turn
cmd/styx/main.go                  bare `styx` -> REPL
cmd/styx/default_routing.go       + [brain] and [tiers] sections
cmd/styx/help.go                  document new verbs
internal/brain/action.go          + RiskLevel, per-dispatch/action risk, schema, EffectiveRisk (Phase 8)
internal/brain/prompt.go          + risk-setting guidance in the preamble (Phase 8)
internal/agent/adapter.go         + DispatchSpec.ReadOnly → omit --dangerously-skip-permissions (Phase 8)
internal/memory/store.go          + provenance columns (project/scope/confidence/last_used_at) + migration (Phase 8)
internal/memory/recall.go         + confidence·recency decay scoring (Phase 8)
cmd/styx/repl.go                  + ship-risk confirmation gate, memory provenance, audit wiring, /audit (Phase 8)
```

Conventions to follow (from this codebase): table-driven tests with `t.Run`, errors wrapped with `fmt.Errorf("context: %w", err)`, `channel.ClassifiedError` for channel errors, atomic file writes via tmp+rename, `paths.EnsureDir` for directories. Run `go build ./... && go vet ./...` before every commit.

---

## Phase 1 — Groundwork

### Task 1: paths additions (MemoryDir, ThreadsDir)

**Files:**
- Modify: `internal/paths/paths.go` (append after `UsageDBPath`, before `EnsureDir`)
- Test: `internal/paths/paths_test.go` (append)

- [ ] **Step 1: Write the failing test** — append to `internal/paths/paths_test.go`:

```go
func TestMemoryAndThreadsDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	md, err := MemoryDir()
	if err != nil {
		t.Fatalf("MemoryDir: %v", err)
	}
	if md != "/tmp/xdg-test/styx/state/memory" {
		t.Errorf("MemoryDir = %q, want /tmp/xdg-test/styx/state/memory", md)
	}
	td, err := ThreadsDir()
	if err != nil {
		t.Fatalf("ThreadsDir: %v", err)
	}
	if td != "/tmp/xdg-test/styx/state/threads" {
		t.Errorf("ThreadsDir = %q, want /tmp/xdg-test/styx/state/threads", td)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/paths/ -run TestMemoryAndThreadsDirs -v`
Expected: FAIL — `undefined: MemoryDir`

- [ ] **Step 3: Implement** — append to `internal/paths/paths.go` (before `EnsureDir`):

```go
// MemoryDir returns the directory holding per-project memory databases.
func MemoryDir() (string, error) {
	s, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(s, "memory"), nil
}

// ThreadsDir returns the directory holding per-project agent-thread state.
func ThreadsDir() (string, error) {
	s, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(s, "threads"), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/paths/ -v`
Expected: PASS (all tests)

- [ ] **Step 5: Commit**

```bash
git add internal/paths/
git commit -m "feat(paths): MemoryDir and ThreadsDir for REPL state"
```

### Task 2: config — [brain] section, [tiers] map, per-channel timeouts

**Files:**
- Modify: `internal/config/routing.go`
- Modify: `cmd/styx/default_routing.go`
- Test: `internal/config/routing_test.go` (append)

- [ ] **Step 1: Write the failing test** — append to `internal/config/routing_test.go`:

```go
func TestBrainAndTiersConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routing.toml")
	content := `
[brain]
model = "qwen3:4b"
embed_model = "nomic-embed-text"
confidence_threshold = 0.6
context_threshold_pct = 75
fable_weekly_cap = 50

[tiers]
fable = "fable"
opus = "opus"
sonnet = "sonnet"
haiku = "haiku"

[budget]
claude.cap_pct = 80
claude.timeout_minutes = 12
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRoutingFile(p)
	if err != nil {
		t.Fatalf("LoadRoutingFile: %v", err)
	}
	if r.Brain.Model != "qwen3:4b" || r.Brain.EmbedModel != "nomic-embed-text" {
		t.Errorf("brain models = %q/%q", r.Brain.Model, r.Brain.EmbedModel)
	}
	if r.Brain.ConfidenceThreshold != 0.6 || r.Brain.ContextThresholdPct != 75 {
		t.Errorf("brain thresholds = %v/%v", r.Brain.ConfidenceThreshold, r.Brain.ContextThresholdPct)
	}
	if r.Brain.FableWeeklyCap != 50 {
		t.Errorf("FableWeeklyCap = %d", r.Brain.FableWeeklyCap)
	}
	if r.Tiers["sonnet"] != "sonnet" {
		t.Errorf("Tiers[sonnet] = %q", r.Tiers["sonnet"])
	}
	if r.Budget.Claude.TimeoutMinutes != 12 {
		t.Errorf("TimeoutMinutes = %d", r.Budget.Claude.TimeoutMinutes)
	}
}

func TestBrainDefaultsAppliedWhenSectionMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(p, []byte("[budget]\nclaude.cap_pct = 80\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRoutingFile(p)
	if err != nil {
		t.Fatalf("LoadRoutingFile: %v", err)
	}
	if r.Brain.Model != "qwen3:4b" {
		t.Errorf("default brain model = %q, want qwen3:4b", r.Brain.Model)
	}
	if r.Brain.EmbedModel != "nomic-embed-text" {
		t.Errorf("default embed model = %q", r.Brain.EmbedModel)
	}
	if r.Brain.ConfidenceThreshold != 0.5 {
		t.Errorf("default confidence = %v, want 0.5", r.Brain.ConfidenceThreshold)
	}
	if r.Brain.ContextThresholdPct != 70 {
		t.Errorf("default context threshold = %v, want 70", r.Brain.ContextThresholdPct)
	}
	if r.Brain.FableWeeklyCap != 80 {
		t.Errorf("default fable cap = %d, want 80", r.Brain.FableWeeklyCap)
	}
	if r.Tiers["fable"] != "opus" || r.Tiers["haiku"] != "haiku" {
		t.Errorf("default tiers = %v", r.Tiers)
	}
}
```

(Add `"os"` and `"path/filepath"` to the test file imports if not present.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'TestBrain' -v`
Expected: FAIL — `r.Brain undefined`

- [ ] **Step 3: Implement** — in `internal/config/routing.go`:

Add to the `Routing` struct:

```go
// Routing is the parsed routing.toml.
type Routing struct {
	Budget BudgetCaps        `toml:"budget"`
	Rules  []Rule            `toml:"rule"`
	Brain  BrainConfig       `toml:"brain"`
	Tiers  map[string]string `toml:"tiers"`
}
```

Add to `ChannelCap`:

```go
// ChannelCap is the maximum percentage of a channel's budget to use before degrading.
type ChannelCap struct {
	CapPct          float64 `toml:"cap_pct"`
	MessagesPer5h   int     `toml:"messages_per_5h"`
	MessagesPerWeek int     `toml:"messages_per_week"`
	TimeoutMinutes  int     `toml:"timeout_minutes"`
}
```

Add the new type and defaults function:

```go
// BrainConfig configures the REPL's local routing brain.
type BrainConfig struct {
	Model               string  `toml:"model"`                 // ollama model for routing decisions
	EmbedModel          string  `toml:"embed_model"`           // ollama model for memory embeddings
	ConfidenceThreshold float64 `toml:"confidence_threshold"`  // below this, escalate routing to claude haiku
	ContextThresholdPct float64 `toml:"context_threshold_pct"` // distill-and-restart threads above this
	FableWeeklyCap      int     `toml:"fable_weekly_cap"`      // weekly fable messages before degrading to opus
}

// applyBrainDefaults fills zero-valued brain/tier settings so configs written
// before this section existed keep working.
func applyBrainDefaults(r *Routing) {
	if r.Brain.Model == "" {
		r.Brain.Model = "qwen3:4b"
	}
	if r.Brain.EmbedModel == "" {
		r.Brain.EmbedModel = "nomic-embed-text"
	}
	if r.Brain.ConfidenceThreshold == 0 {
		r.Brain.ConfidenceThreshold = 0.5
	}
	if r.Brain.ContextThresholdPct == 0 {
		r.Brain.ContextThresholdPct = 70
	}
	if r.Brain.FableWeeklyCap == 0 {
		r.Brain.FableWeeklyCap = 80
	}
	if r.Tiers == nil {
		r.Tiers = map[string]string{}
	}
	for tier, model := range map[string]string{
		// fable -> opus: Fable 5 is suspended worldwide (2026-06-12 US export
		// directive). Opus 4.8 is the top callable model. Restore "fable" if it returns.
		"fable": "opus", "opus": "opus", "sonnet": "sonnet", "haiku": "haiku",
	} {
		if r.Tiers[tier] == "" {
			r.Tiers[tier] = model
		}
	}
}
```

Call it at the end of `LoadRoutingFile` (before the final return):

```go
	var r Routing
	if err := toml.Unmarshal(b, &r); err != nil {
		return Routing{}, fmt.Errorf("parse routing config %s: %w", path, err)
	}
	applyBrainDefaults(&r)
	return r, nil
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/ -v`
Expected: PASS (including pre-existing tests — they load configs without `[brain]`, defaults must not break them)

- [ ] **Step 5: Seed defaults for new users** — append to `defaultRoutingTOML` in `cmd/styx/default_routing.go` (at the end of the backtick string, before the closing backtick):

```toml

# ── REPL brain ──
[brain]
model                 = "qwen3:4b"
embed_model           = "nomic-embed-text"
confidence_threshold  = 0.5
context_threshold_pct = 70
fable_weekly_cap      = 80   # vestigial while fable maps to opus (see [tiers] note); kept for easy restore

# Tier -> claude CLI model alias. The brain emits tiers; the REPL maps them here.
# NOTE (2026-06-12): Claude Fable 5 and Mythos 5 are suspended worldwide under a
# US export-control directive, so the "fable" tier maps to opus until access is
# restored. Opus 4.8 is the most capable model currently callable. Flip fable
# back to "fable" if/when Anthropic restores it.
[tiers]
fable  = "opus"
opus   = "opus"
sonnet = "sonnet"
haiku  = "haiku"
```

- [ ] **Step 6: Build and commit**

Run: `go build ./... && go vet ./... && go test ./internal/config/`
Expected: all pass

```bash
git add internal/config/ cmd/styx/default_routing.go
git commit -m "feat(config): [brain] + [tiers] sections and per-channel timeouts"
```

---

## Phase 2 — Memory

### Task 3: memory store (SQLite + vector blobs)

**Files:**
- Create: `internal/memory/store.go`
- Test: `internal/memory/store_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/memory/store_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory/ -v`
Expected: FAIL — package does not exist / undefined symbols

- [ ] **Step 3: Implement** — create `internal/memory/store.go`:

```go
// Package memory implements styx's per-project and global long-term memory:
// SQLite-backed items with ollama embeddings, recalled by brute-force cosine
// similarity (personal scale needs no vector DB).
package memory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"time"

	_ "modernc.org/sqlite"
)

// Kind labels what a memory item is.
type Kind string

const (
	KindDecision          Kind = "decision"
	KindTodo              Kind = "todo"
	KindDistillation      Kind = "distillation"
	KindBrief             Kind = "brief"
	KindFact              Kind = "fact"
	KindRoutingPreference Kind = "routing-preference"
)

// Item is one memory record.
type Item struct {
	ID        int64
	Kind      Kind
	Text      string
	Source    string // which thread/session/pipeline wrote it
	Embedding []float32
	CreatedAt time.Time
}

// Store is one SQLite memory database (per-project or global).
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS memory (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    source     TEXT    NOT NULL DEFAULT '',
    embedding  BLOB    NOT NULL,
    created_at INTEGER NOT NULL
);
`

// Open opens (creating if needed) the memory database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open memory db %s: %w", path, err)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply memory schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Add inserts an item and returns its id.
func (s *Store) Add(ctx context.Context, it Item) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO memory (kind, text, source, embedding, created_at) VALUES (?, ?, ?, ?, ?)`,
		string(it.Kind), it.Text, it.Source, encodeVec(it.Embedding), time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("insert memory item: %w", err)
	}
	return res.LastInsertId()
}

// All returns every item in the store, newest first.
func (s *Store) All(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, text, source, embedding, created_at FROM memory ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query memory: %w", err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var kind string
		var blob []byte
		var ts int64
		if err := rows.Scan(&it.ID, &kind, &it.Text, &it.Source, &blob, &ts); err != nil {
			return nil, fmt.Errorf("scan memory item: %w", err)
		}
		it.Kind = Kind(kind)
		it.Embedding = decodeVec(blob)
		it.CreatedAt = time.Unix(ts, 0)
		out = append(out, it)
	}
	return out, rows.Err()
}

// encodeVec packs a float32 vector as a little-endian blob.
func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVec unpacks a little-endian blob into a float32 vector.
func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/memory/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/memory/
git commit -m "feat(memory): sqlite store with float32 embedding blobs"
```

### Task 4: ollama embedder

**Files:**
- Create: `internal/memory/embed.go`
- Test: `internal/memory/embed_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/memory/embed_test.go`:

```go
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbedder(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("path = %q, want /api/embed", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embeddings": [][]float64{{0.25, -1.0, 0.5}},
		})
	}))
	defer srv.Close()

	e := NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	vec, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotBody["model"] != "nomic-embed-text" || gotBody["input"] != "hello world" {
		t.Errorf("request body = %v", gotBody)
	}
	want := []float32{0.25, -1.0, 0.5}
	if len(vec) != 3 {
		t.Fatalf("vec len %d, want 3", len(vec))
	}
	for i := range want {
		if vec[i] != want[i] {
			t.Errorf("vec[%d] = %v, want %v", i, vec[i], want[i])
		}
	}
}

func TestOllamaEmbedderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "model not found", http.StatusNotFound)
	}))
	defer srv.Close()
	e := NewOllamaEmbedder(srv.URL, "nope")
	if _, err := e.Embed(context.Background(), "x"); err == nil {
		t.Fatal("want error on 404, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory/ -run TestOllamaEmbedder -v`
Expected: FAIL — `undefined: NewOllamaEmbedder`

- [ ] **Step 3: Implement** — create `internal/memory/embed.go`:

```go
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Embedder turns text into a vector. Production uses ollama; tests use fakes.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// OllamaEmbedder calls ollama's /api/embed endpoint.
type OllamaEmbedder struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaEmbedder returns an embedder against baseURL (e.g.
// "http://localhost:11434") using the given model (e.g. "nomic-embed-text").
func NewOllamaEmbedder(baseURL, model string) *OllamaEmbedder {
	return &OllamaEmbedder{
		baseURL: baseURL,
		model:   model,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// Embed implements Embedder.
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: text})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read embed response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama embed %d: %s", resp.StatusCode, string(raw))
	}
	var er embedResponse
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, fmt.Errorf("parse embed response: %w", err)
	}
	if len(er.Embeddings) == 0 || len(er.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("ollama embed returned no vector")
	}
	return er.Embeddings[0], nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/memory/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/memory/
git commit -m "feat(memory): ollama /api/embed embedder"
```

### Task 5: recall (cosine top-k across stores)

**Files:**
- Create: `internal/memory/recall.go`
- Test: `internal/memory/recall_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/memory/recall_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/memory/ -run 'TestCosine|TestRecall' -v`
Expected: FAIL — `undefined: cosine`, `undefined: Recall`

- [ ] **Step 3: Implement** — create `internal/memory/recall.go`:

```go
package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// Hit is one recalled memory with its similarity score.
type Hit struct {
	Item  Item
	Score float64
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
			hits = append(hits, Hit{Item: it, Score: cosine(qv, it.Embedding)})
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/memory/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/memory/
git commit -m "feat(memory): brute-force cosine top-k recall across stores"
```

---

## Phase 3 — The brain

### Task 6: Action types + JSON schema

**Files:**
- Create: `internal/brain/action.go`
- Test: `internal/brain/action_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/brain/action_test.go`:

```go
package brain

import (
	"encoding/json"
	"testing"
)

func TestActionUnmarshal(t *testing.T) {
	raw := `{
		"action": "dispatch",
		"dispatches": [{
			"thread": "claude",
			"model": "sonnet",
			"message": "refactor the session loader",
			"cli_options": ["--add-dir", "../other"],
			"rationale": "implementation work"
		}],
		"confidence": 0.9
	}`
	var a Action
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.Action != ActionDispatch {
		t.Errorf("action = %q", a.Action)
	}
	if len(a.Dispatches) != 1 || a.Dispatches[0].Thread != "claude" || a.Dispatches[0].Model != "sonnet" {
		t.Errorf("dispatches = %+v", a.Dispatches)
	}
	if a.Confidence != 0.9 {
		t.Errorf("confidence = %v", a.Confidence)
	}
}

func TestActionValid(t *testing.T) {
	tests := []struct {
		name string
		a    Action
		want bool
	}{
		{"reply ok", Action{Action: ActionReply, Reply: "hi", Confidence: 0.8}, true},
		{"reply missing text", Action{Action: ActionReply, Confidence: 0.8}, false},
		{"dispatch ok", Action{Action: ActionDispatch, Confidence: 0.7,
			Dispatches: []Dispatch{{Thread: "claude", Message: "do it"}}}, true},
		{"dispatch empty", Action{Action: ActionDispatch, Confidence: 0.7}, false},
		{"dispatch bad thread", Action{Action: ActionDispatch, Confidence: 0.7,
			Dispatches: []Dispatch{{Thread: "gpt9", Message: "x"}}}, false},
		{"pipeline ok", Action{Action: ActionPipeline, Pipeline: "research", Confidence: 0.9}, true},
		{"pipeline bad name", Action{Action: ActionPipeline, Pipeline: "destroy", Confidence: 0.9}, false},
		{"remember ok", Action{Action: ActionRemember, Remember: "fact", Confidence: 1}, true},
		{"remember empty", Action{Action: ActionRemember, Confidence: 1}, false},
		{"handoff ok", Action{Action: ActionHandoff, Confidence: 0.9}, true},
		{"escalate ok", Action{Action: ActionEscalate, Confidence: 0.2}, true},
		{"unknown action", Action{Action: "fly", Confidence: 1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Valid(); got != tt.want {
				t.Errorf("Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestActionSchemaIsValidJSON(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal(ActionSchema, &v); err != nil {
		t.Fatalf("ActionSchema is not valid JSON: %v", err)
	}
	if v["type"] != "object" {
		t.Errorf("schema root type = %v", v["type"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement** — create `internal/brain/action.go`:

```go
// Package brain implements the styx REPL's routing brain: a small local
// ollama model emitting schema-constrained Actions, with claude-haiku
// escalation when confidence is low.
package brain

import "encoding/json"

// ActionType enumerates what the brain can decide to do with one utterance.
type ActionType string

const (
	ActionReply            ActionType = "reply"             // answer directly, no dispatch
	ActionDispatch         ActionType = "dispatch"          // send to one agent thread
	ActionParallelDispatch ActionType = "parallel_dispatch" // send to several threads at once
	ActionPipeline         ActionType = "pipeline"          // run an existing styx pipeline verb
	ActionHandoff          ActionType = "handoff"           // open interactive claude on the thread
	ActionRemember         ActionType = "remember"          // store a memory item
	ActionEscalate         ActionType = "escalate"          // brain is unsure; escalate routing
)

// Dispatch is one outbound message to an agent thread.
type Dispatch struct {
	Thread     string   `json:"thread"`                // claude | codex | agy | ollama
	Model      string   `json:"model,omitempty"`       // tier (fable|opus|sonnet|haiku) or ollama model
	Message    string   `json:"message"`               // what to send the agent
	CLIOptions []string `json:"cli_options,omitempty"` // extra CLI flags, e.g. --add-dir
	Rationale  string   `json:"rationale,omitempty"`   // one line, shown to the user
}

// Action is the brain's full decision for one turn.
type Action struct {
	Action     ActionType `json:"action"`
	Dispatches []Dispatch `json:"dispatches,omitempty"`
	Pipeline   string     `json:"pipeline,omitempty"` // research | auto | review | intel
	Reply      string     `json:"reply,omitempty"`
	Remember   string     `json:"remember,omitempty"`
	Confidence float64    `json:"confidence"`
}

var validThreads = map[string]bool{"claude": true, "codex": true, "agy": true, "ollama": true}
var validPipelines = map[string]bool{"research": true, "auto": true, "review": true, "intel": true}

// Valid reports whether the action is structurally usable. The REPL treats
// invalid actions like a brain failure (retry, then ask the user).
func (a Action) Valid() bool {
	switch a.Action {
	case ActionReply:
		return a.Reply != ""
	case ActionDispatch, ActionParallelDispatch:
		if len(a.Dispatches) == 0 {
			return false
		}
		for _, d := range a.Dispatches {
			if !validThreads[d.Thread] || d.Message == "" {
				return false
			}
		}
		return true
	case ActionPipeline:
		return validPipelines[a.Pipeline]
	case ActionRemember:
		return a.Remember != ""
	case ActionHandoff, ActionEscalate:
		return true
	default:
		return false
	}
}

// ActionSchema is the JSON schema sent as ollama's `format` parameter so the
// model can only emit valid Action JSON (structured outputs).
var ActionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["reply", "dispatch", "parallel_dispatch", "pipeline", "handoff", "remember", "escalate"]
    },
    "dispatches": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "thread": {"type": "string", "enum": ["claude", "codex", "agy", "ollama"]},
          "model": {"type": "string"},
          "message": {"type": "string"},
          "cli_options": {"type": "array", "items": {"type": "string"}},
          "rationale": {"type": "string"}
        },
        "required": ["thread", "message"]
      }
    },
    "pipeline": {"type": "string", "enum": ["research", "auto", "review", "intel", ""]},
    "reply": {"type": "string"},
    "remember": {"type": "string"},
    "confidence": {"type": "number"}
  },
  "required": ["action", "confidence"]
}`)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/brain/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/brain/
git commit -m "feat(brain): Action types and structured-output JSON schema"
```

### Task 7: capability cards + prompt builder

**Files:**
- Create: `internal/brain/cards.go`
- Create: `internal/brain/prompt.go`
- Test: `internal/brain/prompt_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/brain/prompt_test.go`:

```go
package brain

import (
	"strings"
	"testing"
)

func TestCardsCoverAllThreads(t *testing.T) {
	want := []string{"claude", "codex", "agy", "ollama"}
	for _, w := range want {
		found := false
		for _, c := range Cards {
			if c.CLI == w {
				found = true
				if c.Condensed == "" {
					t.Errorf("card %s has empty Condensed text", w)
				}
			}
		}
		if !found {
			t.Errorf("no capability card for %s", w)
		}
	}
}

func TestBuildPrompt(t *testing.T) {
	turn := Turn{
		Utterance:    "fix the flaky session test",
		Summary:      "we are refactoring the session loader",
		RecentTurns:  []string{"user: hello", "styx: hi"},
		ThreadStatus: []string{"claude (claude): 3 turns, context 41%"},
		MemoryHits:   []string{"[decision] use sqlite for memory"},
	}
	sys, user := BuildPrompt(turn)
	if !strings.Contains(sys, "routing brain") {
		t.Errorf("system prompt missing role statement:\n%s", sys)
	}
	// Every condensed card must reach the model every turn.
	for _, c := range Cards {
		if !strings.Contains(sys, c.Condensed) {
			t.Errorf("system prompt missing card for %s", c.CLI)
		}
	}
	for _, want := range []string{
		"fix the flaky session test",
		"we are refactoring the session loader",
		"user: hello",
		"claude (claude): 3 turns, context 41%",
		"[decision] use sqlite for memory",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q:\n%s", want, user)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -run 'TestCards|TestBuildPrompt' -v`
Expected: FAIL — `undefined: Cards`, `undefined: BuildPrompt`

- [ ] **Step 3: Implement cards** — create `internal/brain/cards.go`:

```go
package brain

// Card encodes curated expert knowledge of one CLI's surface. Condensed is
// injected into the brain's system prompt every turn; ExpectedFlags and
// ResumeProbe are used by `styx doctor` to detect knowledge drift.
type Card struct {
	CLI           string
	Bin           string   // binary to probe; "" = no binary (ollama probed via HTTP)
	Condensed     string   // what the brain sees
	ExpectedFlags []string // doctor checks --help contains each of these
	ResumeProbe   string   // substring of --help proving session-resume support
}

// Cards is the curated capability set, one per dispatchable thread kind.
var Cards = []Card{
	{
		CLI: "claude",
		Bin: "claude",
		Condensed: "claude — Claude Code CLI. Models by tier: opus (deep planning, architecture, hard debugging, complex implementation — the top callable tier), sonnet (default implementation/review), haiku (cheap classify/distill). Best for: multi-file implementation, debugging with repo context, planning, code review. Supports per-thread persistent sessions and interactive handoff. Extra option --add-dir <path> for cross-repo work. (A 'fable' tier exists for the most demanding work but is currently suspended and maps to opus — prefer opus.)",
		ExpectedFlags: []string{"--resume", "--output-format", "--model", "--add-dir", "--dangerously-skip-permissions"},
		ResumeProbe:   "--resume",
	},
	{
		CLI: "codex",
		Bin: "codex",
		Condensed: "codex — OpenAI Codex CLI (gpt-5 class). Best for: sandboxed script checks, quick second-opinion code reviews, algorithmic one-shots, cross-checking claude's work. Headless `codex exec`. No interactive handoff.",
		ExpectedFlags: []string{"exec", "--model"},
		ResumeProbe:   "resume",
	},
	{
		CLI: "agy",
		Bin: "agy",
		Condensed: "agy — Google Antigravity CLI (Gemini, 1M context). Best for: summarizing or explaining very large files/diffs, web-flavored research questions. Headless only; styx maintains conversation continuity for it.",
		ExpectedFlags: []string{"-p", "--add-dir"},
		ResumeProbe:   "", // no known resume support; doctor reports degraded mode
	},
	{
		CLI: "ollama",
		Bin: "",
		Condensed: "ollama — local models, free and instant. Models: qwen2.5-coder:7b (trivial grunt), qwen2.5-coder:14b (better grunt/summarize). Best for: summaries, commit messages, classification, boilerplate one-shots. NEVER for real implementation, planning, or anything needing accuracy.",
	},
}

// CondensedCards returns each card's brain-facing text.
func CondensedCards() []string {
	out := make([]string, 0, len(Cards))
	for _, c := range Cards {
		out = append(out, c.Condensed)
	}
	return out
}

// CardFor returns the card for a CLI name (nil if unknown).
func CardFor(cli string) *Card {
	for i := range Cards {
		if Cards[i].CLI == cli {
			return &Cards[i]
		}
	}
	return nil
}
```

- [ ] **Step 4: Implement prompt builder** — create `internal/brain/prompt.go`:

```go
package brain

import "strings"

// systemPreamble explains the brain's job and the action vocabulary. Kept
// deliberately short — the brain must stay sub-second.
const systemPreamble = `You are the routing brain of styx, a personal AI dev orchestrator. For each user utterance, decide ONE action and emit ONLY JSON matching the provided schema.

Actions:
- reply: answer small talk, status questions, or anything you can answer from the context below. Put the answer in "reply".
- dispatch: send work to one agent thread. Pick thread + model tier per the capability cards.
- parallel_dispatch: send to 2+ threads when independent perspectives help (e.g. cross-review).
- pipeline: run a styx pipeline; "research" (deep research brief), "auto" (full plan-build-review cycle), "review" (code review of current diff), "intel" (refresh codebase intelligence).
- handoff: the user wants open-ended interactive collaboration ("let's work through this together") — open interactive claude on the thread.
- remember: the user states a durable fact, decision, or preference to keep ("note this", "remember that..."). Put it in "remember". If the user is correcting a routing choice you made, prefix it with "routing-preference: ".
- escalate: you are genuinely unsure how to route.

Model tiers for claude dispatches: opus = judgment-heavy work (brainstorm, architecture, planning, hard debugging) and complex implementation. sonnet = normal implementation, refactors, review. haiku = trivial classification. (There is also a "fable" tier for the most demanding work, but it is currently suspended and maps to opus — prefer opus.)
Set "confidence" to your honest routing confidence (0-1). Respect routing-preference memories — they are corrections from this user.

Capability cards:
`

// BuildPrompt renders a Turn into (system, user) prompts for the brain.
func BuildPrompt(t Turn) (string, string) {
	var sys strings.Builder
	sys.WriteString(systemPreamble)
	for _, c := range CondensedCards() {
		sys.WriteString("- ")
		sys.WriteString(c)
		sys.WriteString("\n")
	}

	var u strings.Builder
	if t.Summary != "" {
		u.WriteString("Conversation summary:\n" + t.Summary + "\n\n")
	}
	if len(t.RecentTurns) > 0 {
		u.WriteString("Recent turns:\n" + strings.Join(t.RecentTurns, "\n") + "\n\n")
	}
	if len(t.ThreadStatus) > 0 {
		u.WriteString("Live threads:\n" + strings.Join(t.ThreadStatus, "\n") + "\n\n")
	}
	if len(t.MemoryHits) > 0 {
		u.WriteString("Relevant memories:\n" + strings.Join(t.MemoryHits, "\n") + "\n\n")
	}
	u.WriteString("User utterance:\n" + t.Utterance)
	return sys.String(), u.String()
}
```

- [ ] **Step 5: Run tests, then commit**

Run: `go test ./internal/brain/ -v`
Expected: PASS — note `Turn` is defined in Task 8's `brain.go`; if the package does not compile yet, add this minimal placeholder **at the top of `prompt.go`** and Task 8 will move it:

```go
// Turn is everything the brain sees for one routing decision.
type Turn struct {
	Utterance    string
	Summary      string   // rolling conversation summary
	RecentTurns  []string // rendered recent exchanges, oldest first
	ThreadStatus []string // one line per live thread
	MemoryHits   []string // rendered top-k memory recalls
}
```

```bash
git add internal/brain/
git commit -m "feat(brain): capability cards and turn prompt builder"
```

### Task 8: Ollama brain — Decide with retry, escalation, ErrNeedUser

**Files:**
- Create: `internal/brain/brain.go` (move `Turn` here from `prompt.go`)
- Test: `internal/brain/brain_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/brain/brain_test.go`:

```go
package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

// fakeOllama serves /api/chat returning the queued contents in order.
func fakeOllama(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	i := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["format"] == nil {
			t.Error("chat request missing format (structured output) field")
		}
		reply := replies[min(i, len(replies)-1)]
		i++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"role": "assistant", "content": reply},
			"done":    true,
		})
	}))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestDecideHappyPath(t *testing.T) {
	srv := fakeOllama(t, `{"action":"dispatch","dispatches":[{"thread":"claude","model":"sonnet","message":"do it","rationale":"impl"}],"confidence":0.9}`)
	defer srv.Close()
	b := &Ollama{BaseURL: srv.URL, Model: "qwen3:4b", ConfidenceThreshold: 0.5}
	a, err := b.Decide(context.Background(), Turn{Utterance: "implement the fix"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if a.Action != ActionDispatch || a.Dispatches[0].Thread != "claude" {
		t.Errorf("action = %+v", a)
	}
}

func TestDecideRetriesOnceOnInvalidJSON(t *testing.T) {
	srv := fakeOllama(t,
		`not json at all`,
		`{"action":"reply","reply":"hi","confidence":0.8}`)
	defer srv.Close()
	b := &Ollama{BaseURL: srv.URL, Model: "qwen3:4b"}
	a, err := b.Decide(context.Background(), Turn{Utterance: "hello"})
	if err != nil {
		t.Fatalf("Decide after retry: %v", err)
	}
	if a.Action != ActionReply || a.Reply != "hi" {
		t.Errorf("action = %+v", a)
	}
}

func TestDecideErrNeedUserAfterTwoFailures(t *testing.T) {
	srv := fakeOllama(t, `garbage`, `more garbage`)
	defer srv.Close()
	b := &Ollama{BaseURL: srv.URL, Model: "qwen3:4b"}
	_, err := b.Decide(context.Background(), Turn{Utterance: "hello"})
	if !errors.Is(err, ErrNeedUser) {
		t.Fatalf("err = %v, want ErrNeedUser", err)
	}
}

// fakeChannel is a scripted channel.Channel for escalation tests.
type fakeChannel struct{ text string }

func (f *fakeChannel) Name() string { return "claude" }
func (f *fakeChannel) BudgetState(context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}
func (f *fakeChannel) Send(_ context.Context, _ channel.Request) (channel.Response, error) {
	return channel.Response{Text: f.text}, nil
}

func TestDecideEscalatesOnLowConfidence(t *testing.T) {
	srv := fakeOllama(t, `{"action":"escalate","confidence":0.1}`)
	defer srv.Close()
	esc := &ClaudeEscalator{
		Channel: &fakeChannel{text: "Here you go:\n{\"action\":\"dispatch\",\"dispatches\":[{\"thread\":\"codex\",\"message\":\"check it\"}],\"confidence\":0.95}"},
		Model:   "haiku",
	}
	b := &Ollama{BaseURL: srv.URL, Model: "qwen3:4b", ConfidenceThreshold: 0.5, Escalator: esc}
	a, err := b.Decide(context.Background(), Turn{Utterance: "ambiguous thing"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if a.Action != ActionDispatch || a.Dispatches[0].Thread != "codex" {
		t.Errorf("escalated action = %+v", a)
	}
}

func TestDecideErrNeedUserWhenOllamaDown(t *testing.T) {
	b := &Ollama{BaseURL: "http://127.0.0.1:1", Model: "qwen3:4b"}
	_, err := b.Decide(context.Background(), Turn{Utterance: "hello"})
	if !errors.Is(err, ErrNeedUser) {
		t.Fatalf("err = %v, want ErrNeedUser", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -run TestDecide -v`
Expected: FAIL — `undefined: Ollama`, `undefined: ErrNeedUser`, `undefined: ClaudeEscalator`

- [ ] **Step 3: Implement** — create `internal/brain/brain.go` (and **delete the `Turn` placeholder from `prompt.go`**):

```go
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/channel"
)

// Turn is everything the brain sees for one routing decision.
type Turn struct {
	Utterance    string
	Summary      string   // rolling conversation summary
	RecentTurns  []string // rendered recent exchanges, oldest first
	ThreadStatus []string // one line per live thread
	MemoryHits   []string // rendered top-k memory recalls
}

// Brain decides what to do with one utterance. Production uses Ollama;
// tests use scripted fakes.
type Brain interface {
	Decide(ctx context.Context, t Turn) (Action, error)
}

// ErrNeedUser means the brain cannot produce a decision (ollama down or
// emitting invalid JSON twice). The REPL must ask the user to route manually
// — it never bricks.
var ErrNeedUser = errors.New("brain unavailable")

// Escalator re-makes a routing decision with a stronger model when the local
// brain's confidence is below threshold.
type Escalator interface {
	Escalate(ctx context.Context, t Turn) (Action, error)
}

// Ollama is the production brain: a small local model with structured output.
type Ollama struct {
	BaseURL             string  // e.g. http://localhost:11434
	Model               string  // e.g. qwen3:4b
	ConfidenceThreshold float64 // escalate below this (0 disables)
	Escalator           Escalator

	client *http.Client
}

func (b *Ollama) httpClient() *http.Client {
	if b.client == nil {
		b.client = &http.Client{Timeout: 60 * time.Second}
	}
	return b.client
}

type brainChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type brainChatRequest struct {
	Model    string             `json:"model"`
	Stream   bool               `json:"stream"`
	Format   json.RawMessage    `json:"format"`
	Options  map[string]any     `json:"options"`
	Messages []brainChatMessage `json:"messages"`
}

type brainChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// Decide implements Brain. It retries once on invalid output, escalates on
// low confidence, and wraps total failure in ErrNeedUser.
func (b *Ollama) Decide(ctx context.Context, t Turn) (Action, error) {
	sys, user := BuildPrompt(t)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := b.chat(ctx, sys, user)
		if err != nil {
			lastErr = err
			continue
		}
		var a Action
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			lastErr = fmt.Errorf("brain emitted invalid JSON: %w", err)
			continue
		}
		if !a.Valid() {
			lastErr = fmt.Errorf("brain emitted invalid action %q", a.Action)
			continue
		}
		needsEscalation := a.Action == ActionEscalate ||
			(b.ConfidenceThreshold > 0 && a.Confidence < b.ConfidenceThreshold)
		if needsEscalation && b.Escalator != nil {
			if esc, err := b.Escalator.Escalate(ctx, t); err == nil && esc.Valid() {
				return esc, nil
			}
			// Escalation failing is not fatal; fall through to the local answer.
		}
		return a, nil
	}
	return Action{}, fmt.Errorf("%w: %v", ErrNeedUser, lastErr)
}

func (b *Ollama) chat(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(brainChatRequest{
		Model:   b.Model,
		Stream:  false,
		Format:  ActionSchema,
		Options: map[string]any{"temperature": 0},
		Messages: []brainChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal brain request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build brain request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("brain call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read brain response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ollama brain %d: %s", resp.StatusCode, string(raw))
	}
	var cr brainChatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("parse brain response envelope: %w", err)
	}
	return cr.Message.Content, nil
}

// ClaudeEscalator escalates a routing decision to claude (haiku tier): one
// cheap message against the same action vocabulary.
type ClaudeEscalator struct {
	Channel channel.Channel // raw (undecorated) claude channel
	Model   string          // e.g. "haiku"
}

// Escalate implements Escalator.
func (e *ClaudeEscalator) Escalate(ctx context.Context, t Turn) (Action, error) {
	sys, user := BuildPrompt(t)
	resp, err := e.Channel.Send(ctx, channel.Request{
		Model:  e.Model,
		System: sys,
		Prompt: user + "\n\nRespond with ONLY the JSON action object, no prose.",
	})
	if err != nil {
		return Action{}, fmt.Errorf("escalation call: %w", err)
	}
	jsonText := extractJSON(resp.Text)
	if jsonText == "" {
		return Action{}, fmt.Errorf("escalation reply had no JSON object")
	}
	var a Action
	if err := json.Unmarshal([]byte(jsonText), &a); err != nil {
		return Action{}, fmt.Errorf("parse escalation reply: %w", err)
	}
	return a, nil
}

// extractJSON returns the first {...} block in s ("" if none). Frontier
// models sometimes wrap JSON in prose despite instructions.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/brain/ -v`
Expected: PASS (all brain tests)

- [ ] **Step 5: Commit**

```bash
git add internal/brain/
git commit -m "feat(brain): ollama structured-output brain with haiku escalation"
```

### Task 9: env-gated routing accuracy integration test

**Files:**
- Create: `testdata/brain/utterances.json`
- Create: `internal/brain/integration_test.go`

- [ ] **Step 1: Create the labeled fixture** — `testdata/brain/utterances.json`. Start with this set (extend freely later; the test asserts an accuracy *ratio*, so more data only sharpens it):

```json
[
  {"utterance": "hey, how's it going", "want_action": "reply"},
  {"utterance": "what threads are running right now?", "want_action": "reply"},
  {"utterance": "implement the retry logic we discussed in the session loader", "want_action": "dispatch", "want_thread": "claude"},
  {"utterance": "fix the failing TestRecall test", "want_action": "dispatch", "want_thread": "claude"},
  {"utterance": "refactor dispatch.go to split out the app wiring", "want_action": "dispatch", "want_thread": "claude"},
  {"utterance": "plan the architecture for the new sync engine", "want_action": "dispatch", "want_thread": "claude"},
  {"utterance": "have codex double-check the algorithm in cosine()", "want_action": "dispatch", "want_thread": "codex"},
  {"utterance": "run this python script in a sandbox and tell me if it works", "want_action": "dispatch", "want_thread": "codex"},
  {"utterance": "summarize this 8000-line diff for me", "want_action": "dispatch", "want_thread": "agy"},
  {"utterance": "explain what this huge legacy file does", "want_action": "dispatch", "want_thread": "agy"},
  {"utterance": "write a commit message for the staged changes", "want_action": "dispatch", "want_thread": "ollama"},
  {"utterance": "generate boilerplate getters for this struct", "want_action": "dispatch", "want_thread": "ollama"},
  {"utterance": "get claude and codex to both review this diff", "want_action": "parallel_dispatch"},
  {"utterance": "deep research: what's the state of local-first sync engines in 2026", "want_action": "pipeline", "want_pipeline": "research"},
  {"utterance": "run the full auto cycle on adding rate limiting", "want_action": "pipeline", "want_pipeline": "auto"},
  {"utterance": "review my current diff", "want_action": "pipeline", "want_pipeline": "review"},
  {"utterance": "refresh the codebase intel", "want_action": "pipeline", "want_pipeline": "intel"},
  {"utterance": "let's pair on this gnarly bug together", "want_action": "handoff"},
  {"utterance": "open claude so we can work through the migration interactively", "want_action": "handoff"},
  {"utterance": "remember that I prefer table-driven tests everywhere", "want_action": "remember"},
  {"utterance": "note for later: the staging deploy needs the VPN", "want_action": "remember"},
  {"utterance": "no, codex should have handled that review", "want_action": "remember"}
]
```

- [ ] **Step 2: Write the integration test** — create `internal/brain/integration_test.go`:

```go
package brain

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestRoutingAccuracy runs the real local ollama brain against the labeled
// fixture set. Gated behind STYX_BRAIN_IT=1 because it needs ollama running
// with the brain model pulled. This is the regression net for "is the 4b
// brain good enough."
func TestRoutingAccuracy(t *testing.T) {
	if os.Getenv("STYX_BRAIN_IT") != "1" {
		t.Skip("set STYX_BRAIN_IT=1 (and run ollama) to run the brain integration suite")
	}
	raw, err := os.ReadFile("../../testdata/brain/utterances.json")
	if err != nil {
		t.Fatalf("read fixtures: %v", err)
	}
	var cases []struct {
		Utterance    string `json:"utterance"`
		WantAction   string `json:"want_action"`
		WantThread   string `json:"want_thread"`
		WantPipeline string `json:"want_pipeline"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatalf("parse fixtures: %v", err)
	}

	model := os.Getenv("STYX_BRAIN_MODEL")
	if model == "" {
		model = "qwen3:4b"
	}
	b := &Ollama{BaseURL: "http://localhost:11434", Model: model}

	correct := 0
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		a, err := b.Decide(ctx, Turn{Utterance: c.Utterance})
		cancel()
		if err != nil {
			t.Logf("MISS (error) %q: %v", c.Utterance, err)
			continue
		}
		ok := string(a.Action) == c.WantAction
		if ok && c.WantThread != "" {
			ok = len(a.Dispatches) > 0 && a.Dispatches[0].Thread == c.WantThread
		}
		if ok && c.WantPipeline != "" {
			ok = a.Pipeline == c.WantPipeline
		}
		if ok {
			correct++
		} else {
			t.Logf("MISS %q: got action=%s dispatches=%+v pipeline=%s", c.Utterance, a.Action, a.Dispatches, a.Pipeline)
		}
	}
	accuracy := float64(correct) / float64(len(cases))
	t.Logf("routing accuracy: %d/%d = %.0f%%", correct, len(cases), accuracy*100)
	if accuracy < 0.8 {
		t.Errorf("routing accuracy %.0f%% below 80%% threshold — the 4b brain (or the prompt) needs work", accuracy*100)
	}
}
```

- [ ] **Step 3: Verify it skips by default and compiles**

Run: `go test ./internal/brain/ -run TestRoutingAccuracy -v`
Expected: SKIP with the env-var message

- [ ] **Step 4: (Optional, if ollama is running locally) run it for real**

Run: `ollama pull llama3.2:3b && STYX_BRAIN_IT=1 go test ./internal/brain/ -run TestRoutingAccuracy -v -timeout 30m`
Expected: PASS with logged accuracy ≥ 80%. If it fails, tune `systemPreamble` wording in `prompt.go` — not the test.

- [ ] **Step 5: Commit**

```bash
git add internal/brain/ testdata/brain/
git commit -m "test(brain): env-gated routing accuracy suite with labeled fixtures"
```

---

### Checkpoint A — Dogfood the routing brain (gate before Phase 4)

> Not a code task — a reality gate. The single biggest risk in this whole plan
> is "can a 4b local brain actually route well?" Task 9 only proves the brain
> agrees with the 22 utterances *you wrote*. Prove it on inputs you did not
> author before building threads/REPL on top of it.

- [x] **Expand the fixture from real usage.** Add 25–40 utterances drawn from
  how you actually talk to a dev assistant (skim recent shell history / Claude
  Code sessions) to `testdata/brain/utterances.json`, labeling
  `want_action`/`want_thread`/`want_pipeline` honestly *before* running the brain.
  *(Done: fixture set is now 99 labeled utterances.)*
- [x] **Run for real and read every miss:**
  `ollama pull llama3.2:3b && STYX_BRAIN_IT=1 go test ./internal/brain/ -run TestRoutingAccuracy -v`
  Read each `MISS` line — do not just look at the ratio.
- [x] **Triage misses** into: prompt-fixable (tune `systemPreamble`),
  card-fixable (sharpen a capability card), or genuinely ambiguous (acceptable —
  the REPL escalates low-confidence turns to haiku anyway).
  *(Done: see `eval/promptfoo/RESULTS.md` for the per-miss triage.)*
- [x] **Decision gate.** If accuracy on the *expanded* set stays < 80% after
  tuning, STOP and reconsider before Phase 4: raise `confidence_threshold` so
  more turns escalate to haiku, try a larger brain model, or accept a
  confirm-the-route UX. Record the achieved accuracy and the decision in the
  decisions-log addendum at the end of this plan.
  *(Done: 96% — comfortably clears the gate; see the addendum follow-on entry.)*
- [x] **Commit** only the expanded fixtures + any prompt/card tuning:

```bash
git add testdata/brain/ internal/brain/
git commit -m "test(brain): expand routing fixtures from real usage; tune prompt"
```

---

## Phase 4 — Agent threads

### Task 10: claude stream-json event parser

Claude's headless protocol (`claude -p --output-format stream-json --verbose`) emits one JSON object per line. The three line shapes styx cares about:

```json
{"type":"system","subtype":"init","session_id":"abc-123", ...}
{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}, ...}
{"type":"result","subtype":"success","result":"final answer","session_id":"abc-123","is_error":false,"usage":{"input_tokens":5,"cache_creation_input_tokens":2000,"cache_read_input_tokens":80000,"output_tokens":350}}
```

Context size = sum of all input token fields (cache reads count toward the context window).

**Files:**
- Create: `internal/agent/event.go`
- Test: `internal/agent/event_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/agent/event_test.go`:

```go
package agent

import "testing"

func TestParseClaudeEvent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Event
		ok   bool
	}{
		{
			name: "init",
			line: `{"type":"system","subtype":"init","session_id":"abc-123"}`,
			want: Event{Type: EventInit, SessionID: "abc-123"},
			ok:   true,
		},
		{
			name: "assistant text",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}`,
			want: Event{Type: EventText, Text: "working on it"},
			ok:   true,
		},
		{
			name: "result with usage",
			line: `{"type":"result","subtype":"success","result":"done","session_id":"abc-123","is_error":false,"usage":{"input_tokens":5,"cache_creation_input_tokens":2000,"cache_read_input_tokens":80000,"output_tokens":350}}`,
			want: Event{Type: EventResult, Text: "done", SessionID: "abc-123", InputTokens: 82005, OutputTokens: 350},
			ok:   true,
		},
		{
			name: "error result",
			line: `{"type":"result","subtype":"error_during_execution","result":"boom","is_error":true,"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: Event{Type: EventResult, Text: "boom", InputTokens: 1, OutputTokens: 1, IsError: true},
			ok:   true,
		},
		{
			name: "other system event ignored",
			line: `{"type":"system","subtype":"hook_started"}`,
			ok:   false,
		},
		{
			name: "garbage ignored",
			line: `not json`,
			ok:   false,
		},
		{
			name: "assistant tool-use only (no text) ignored",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`,
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseClaudeEvent([]byte(tt.line))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Errorf("event = %+v, want %+v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -v`
Expected: FAIL — package does not exist

- [ ] **Step 3: Implement** — create `internal/agent/event.go`:

```go
// Package agent implements persistent agent threads: durable named
// conversations with CLI agents (claude/codex/agy), resumed per turn via the
// CLI's own session store. Styx never grows its own tool loop — the CLIs are
// the agents; this package aims them and tracks their lifecycle.
package agent

import "encoding/json"

// EventType labels a streamed agent event.
type EventType string

const (
	EventInit   EventType = "init"   // session started; SessionID set
	EventText   EventType = "text"   // intermediate assistant text
	EventResult EventType = "result" // final result; Text + token usage set
)

// Event is one parsed line of an agent's stream output.
type Event struct {
	Type         EventType
	SessionID    string
	Text         string
	InputTokens  int // total context tokens (input + cache creation + cache reads)
	OutputTokens int
	IsError      bool
}

// claudeLine mirrors the subset of claude's stream-json protocol styx reads.
type claudeLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Message   struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
}

// ParseClaudeEvent parses one stream-json line. ok is false for lines styx
// does not care about (tool use, hooks, malformed input).
func ParseClaudeEvent(line []byte) (Event, bool) {
	var l claudeLine
	if err := json.Unmarshal(line, &l); err != nil {
		return Event{}, false
	}
	switch l.Type {
	case "system":
		if l.Subtype != "init" || l.SessionID == "" {
			return Event{}, false
		}
		return Event{Type: EventInit, SessionID: l.SessionID}, true
	case "assistant":
		text := ""
		for _, c := range l.Message.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		if text == "" {
			return Event{}, false
		}
		return Event{Type: EventText, Text: text}, true
	case "result":
		return Event{
			Type:         EventResult,
			SessionID:    l.SessionID,
			Text:         l.Result,
			InputTokens:  l.Usage.InputTokens + l.Usage.CacheCreationInputTokens + l.Usage.CacheReadInputTokens,
			OutputTokens: l.Usage.OutputTokens,
			IsError:      l.IsError,
		}, true
	}
	return Event{}, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/
git commit -m "feat(agent): claude stream-json event parser"
```

### Task 11: adapters + thread store

**Files:**
- Create: `internal/agent/adapter.go`
- Create: `internal/agent/thread.go`
- Test: `internal/agent/adapter_test.go`
- Test: `internal/agent/thread_test.go`

- [ ] **Step 1: Write the failing adapter test** — create `internal/agent/adapter_test.go`:

```go
package agent

import (
	"reflect"
	"testing"
)

func TestClaudeAdapterBuildArgs(t *testing.T) {
	a := NewClaudeAdapter()
	tests := []struct {
		name      string
		msg       string
		sessionID string
		model     string
		extra     []string
		want      []string
	}{
		{
			name: "fresh session",
			msg:  "hello", model: "sonnet",
			want: []string{"--model", "sonnet", "-p", "hello",
				"--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"},
		},
		{
			name: "resume with extra options",
			msg:  "continue", sessionID: "abc", model: "fable", extra: []string{"--add-dir", "../other"},
			want: []string{"--resume", "abc", "--model", "fable", "--add-dir", "../other",
				"-p", "continue", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"},
		},
		{
			name: "no model",
			msg:  "hi",
			want: []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.BuildArgs(tt.msg, tt.sessionID, tt.model, tt.extra)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("args = %v\nwant  %v", got, tt.want)
			}
		})
	}
	if !a.SupportsResume() || !a.SupportsStream() {
		t.Error("claude adapter must support resume and stream")
	}
	if a.ContextWindow() != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", a.ContextWindow())
	}
}

func TestPlainAdapters(t *testing.T) {
	cx := NewCodexAdapter()
	got := cx.BuildArgs("check this", "", "gpt-5", nil)
	want := []string{"--model", "gpt-5", "exec", "check this"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("codex args = %v, want %v", got, want)
	}
	if cx.SupportsResume() || cx.SupportsStream() {
		t.Error("codex adapter is plain in v1: no resume, no stream")
	}

	ag := NewAgyAdapter()
	got = ag.BuildArgs("summarize", "", "", nil)
	want = []string{"-p", "summarize", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agy args = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Write the failing thread-store test** — create `internal/agent/thread_test.go`:

```go
package agent

import (
	"path/filepath"
	"testing"
)

func TestThreadStoreRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "proj.json")
	ts, err := LoadThreadsFrom(p)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	th := ts.Get("claude", "claude")
	if th == nil || th.CLI != "claude" {
		t.Fatalf("Get created %+v", th)
	}
	th.SessionID = "sess-9"
	th.ContextTokens = 12345
	th.Turns = 3
	th.LastDistillation = "we decided X"
	if err := ts.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	ts2, err := LoadThreadsFrom(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := ts2.Get("claude", "claude")
	if got.SessionID != "sess-9" || got.ContextTokens != 12345 || got.Turns != 3 || got.LastDistillation != "we decided X" {
		t.Errorf("round-trip = %+v", got)
	}
	// Get is idempotent: same pointer for same name.
	if ts2.Get("claude", "claude") != got {
		t.Error("Get returned a different instance for the same name")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/agent/ -v`
Expected: FAIL — `undefined: NewClaudeAdapter`, `undefined: LoadThreadsFrom`

- [ ] **Step 4: Implement adapters** — create `internal/agent/adapter.go`:

```go
package agent

// Adapter encodes how to drive one CLI agent: argument construction, stream
// parsing, and what session features it supports. `styx doctor` reports which
// mode (native-resume vs styx-maintained continuity) each adapter runs in.
type Adapter interface {
	CLI() string
	Bin() string
	SupportsResume() bool // CLI persists sessions; per-turn --resume works
	SupportsStream() bool // CLI emits parseable per-line JSON events
	ContextWindow() int   // tokens; drives the distill threshold
	BuildArgs(msg, sessionID, model string, extra []string) []string
	ParseEvent(line []byte) (Event, bool)
}

// ClaudeAdapter drives the claude CLI in headless stream-json mode.
// Headless dispatches run with permissions pre-granted, matching the existing
// `execute` verb behavior; interactive handoff keeps native prompts.
type ClaudeAdapter struct {
	BinPath string // override for tests; "" means "claude" on PATH
	Window  int    // override for tests; 0 means 200000
}

// NewClaudeAdapter returns the production claude adapter.
func NewClaudeAdapter() *ClaudeAdapter { return &ClaudeAdapter{} }

func (a *ClaudeAdapter) CLI() string { return "claude" }

func (a *ClaudeAdapter) Bin() string {
	if a.BinPath != "" {
		return a.BinPath
	}
	return "claude"
}

func (a *ClaudeAdapter) SupportsResume() bool { return true }
func (a *ClaudeAdapter) SupportsStream() bool { return true }

func (a *ClaudeAdapter) ContextWindow() int {
	if a.Window > 0 {
		return a.Window
	}
	return 200000
}

func (a *ClaudeAdapter) BuildArgs(msg, sessionID, model string, extra []string) []string {
	args := []string{}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, extra...)
	return append(args, "-p", msg, "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions")
}

func (a *ClaudeAdapter) ParseEvent(line []byte) (Event, bool) { return ParseClaudeEvent(line) }

// PlainAdapter drives CLIs without session resume or stream-json (codex, agy
// in v1). The whole stdout becomes one result; conversation continuity is
// styx-maintained via the thread's rolling summary.
type PlainAdapter struct {
	CLIName string
	BinPath string
	Window  int
	ArgsFn  func(msg, model string, extra []string) []string
}

func (a *PlainAdapter) CLI() string                            { return a.CLIName }
func (a *PlainAdapter) Bin() string                            { return a.BinPath }
func (a *PlainAdapter) SupportsResume() bool                   { return false }
func (a *PlainAdapter) SupportsStream() bool                   { return false }
func (a *PlainAdapter) ContextWindow() int                     { return a.Window }
func (a *PlainAdapter) ParseEvent(line []byte) (Event, bool)   { return Event{}, false }
func (a *PlainAdapter) BuildArgs(msg, sessionID, model string, extra []string) []string {
	return a.ArgsFn(msg, model, extra)
}

// NewCodexAdapter drives `codex exec`.
func NewCodexAdapter() *PlainAdapter {
	return &PlainAdapter{
		CLIName: "codex",
		BinPath: "codex",
		Window:  200000,
		ArgsFn: func(msg, model string, extra []string) []string {
			args := []string{}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, extra...)
			return append(args, "exec", msg)
		},
	}
}

// NewAgyAdapter drives `agy -p` (Antigravity). Always headless-permissive,
// matching the existing agy channel.
func NewAgyAdapter() *PlainAdapter {
	return &PlainAdapter{
		CLIName: "agy",
		BinPath: "agy",
		Window:  1000000,
		ArgsFn: func(msg, model string, extra []string) []string {
			args := []string{"-p", msg, "--dangerously-skip-permissions"}
			return append(args, extra...)
		},
	}
}
```

- [ ] **Step 5: Implement thread store** — create `internal/agent/thread.go`:

```go
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Thread is a named, durable conversation with one agent, independent of any
// OS process. Conversation state lives in the CLI's session store (resume-
// capable CLIs) or in Summary (styx-maintained continuity).
type Thread struct {
	Name             string    `json:"name"`
	CLI              string    `json:"cli"`
	SessionID        string    `json:"session_id,omitempty"`
	Summary          string    `json:"summary,omitempty"`           // rolling summary for non-resume CLIs
	LastDistillation string    `json:"last_distillation,omitempty"` // checkpoint for restart/recovery
	ContextTokens    int       `json:"context_tokens"`
	Turns            int       `json:"turns"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ThreadStore persists one project's threads as JSON.
type ThreadStore struct {
	path    string
	Threads map[string]*Thread
}

// LoadThreads opens the thread store for a project by name.
func LoadThreads(projectName string) (*ThreadStore, error) {
	dir, err := paths.ThreadsDir()
	if err != nil {
		return nil, err
	}
	return LoadThreadsFrom(filepath.Join(dir, projectName+".json"))
}

// LoadThreadsFrom opens a thread store at an explicit path (tests).
func LoadThreadsFrom(path string) (*ThreadStore, error) {
	ts := &ThreadStore{path: path, Threads: map[string]*Thread{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ts, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read threads %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &ts.Threads); err != nil {
		return nil, fmt.Errorf("parse threads %s: %w", path, err)
	}
	return ts, nil
}

// Get returns the named thread, creating it (lazily, unsaved) if missing.
func (ts *ThreadStore) Get(name, cli string) *Thread {
	if th, ok := ts.Threads[name]; ok {
		return th
	}
	th := &Thread{Name: name, CLI: cli, UpdatedAt: time.Now()}
	ts.Threads[name] = th
	return th
}

// Save writes the store atomically (tmp + rename).
func (ts *ThreadStore) Save() error {
	if err := paths.EnsureDir(filepath.Dir(ts.path)); err != nil {
		return fmt.Errorf("ensure threads dir: %w", err)
	}
	b, err := json.MarshalIndent(ts.Threads, "", "  ")
	if err != nil {
		return fmt.Errorf("encode threads: %w", err)
	}
	tmp := ts.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write threads tmp: %w", err)
	}
	if err := os.Rename(tmp, ts.path); err != nil {
		return fmt.Errorf("rename threads file: %w", err)
	}
	return nil
}
```

- [ ] **Step 6: Run tests, then commit**

Run: `go test ./internal/agent/ -v`
Expected: PASS

```bash
git add internal/agent/
git commit -m "feat(agent): CLI adapters and persistent thread store"
```

### Task 12: fake agent CLI + runner

**Files:**
- Create: `testdata/fakeagent` (executable shell script)
- Create: `internal/agent/runner.go`
- Test: `internal/agent/runner_test.go`

- [ ] **Step 1: Create the fake agent CLI** — `testdata/fakeagent`:

```bash
#!/bin/bash
# Fake agent CLI speaking claude's stream-json protocol. Drives thread
# lifecycle tests without real CLIs. Knobs (env):
#   FAKEAGENT_SESSION      session id to emit            (default sess-1)
#   FAKEAGENT_TEXT         assistant + result text       (default ok)
#   FAKEAGENT_IN           input_tokens in result usage  (default 100)
#   FAKEAGENT_OUT          output_tokens in result usage (default 20)
#   FAKEAGENT_FAIL_RESUME  "1": exit 1 if --resume present (crash-recovery tests)
#   FAKEAGENT_ARGS_LOG     append "$@" to this file (arg assertions)
if [ -n "$FAKEAGENT_ARGS_LOG" ]; then
  echo "$@" >> "$FAKEAGENT_ARGS_LOG"
fi
if [ "$FAKEAGENT_FAIL_RESUME" = "1" ]; then
  for a in "$@"; do
    if [ "$a" = "--resume" ]; then
      echo "No conversation found with session ID" >&2
      exit 1
    fi
  done
fi
S=${FAKEAGENT_SESSION:-sess-1}
T=${FAKEAGENT_TEXT:-ok}
IN=${FAKEAGENT_IN:-100}
OUT=${FAKEAGENT_OUT:-20}
echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"$S\"}"
echo "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"$T\"}]}}"
echo "{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"$T\",\"session_id\":\"$S\",\"is_error\":false,\"usage\":{\"input_tokens\":$IN,\"output_tokens\":$OUT}}"
```

Run: `chmod +x testdata/fakeagent`

- [ ] **Step 2: Write the failing runner test** — create `internal/agent/runner_test.go`:

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBin(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunnerSendStreamsAndCapturesSession(t *testing.T) {
	t.Setenv("FAKEAGENT_SESSION", "sess-42")
	t.Setenv("FAKEAGENT_TEXT", "did the thing")
	t.Setenv("FAKEAGENT_IN", "1234")
	t.Setenv("FAKEAGENT_OUT", "56")

	th := &Thread{Name: "claude", CLI: "claude"}
	var events []Event
	r := &Runner{
		Adapter: &ClaudeAdapter{BinPath: fakeBin(t)},
		Thread:  th,
		OnEvent: func(e Event) { events = append(events, e) },
		Timeout: 10 * time.Second,
	}
	res, err := r.Send(context.Background(), "do the thing", "sonnet", nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Text != "did the thing" || res.InputTokens != 1234 || res.OutputTokens != 56 {
		t.Errorf("result = %+v", res)
	}
	if th.SessionID != "sess-42" {
		t.Errorf("SessionID = %q, want sess-42", th.SessionID)
	}
	if th.ContextTokens != 1234+56 || th.Turns != 1 {
		t.Errorf("thread meter = tokens %d turns %d", th.ContextTokens, th.Turns)
	}
	if len(events) < 3 {
		t.Fatalf("got %d events, want >= 3 (init, text, result)", len(events))
	}
	if events[0].Type != EventInit || events[len(events)-1].Type != EventResult {
		t.Errorf("event order: first=%s last=%s", events[0].Type, events[len(events)-1].Type)
	}
}

func TestRunnerSendPassesResumeArg(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKEAGENT_ARGS_LOG", log)

	th := &Thread{Name: "claude", CLI: "claude", SessionID: "sess-7"}
	r := &Runner{Adapter: &ClaudeAdapter{BinPath: fakeBin(t)}, Thread: th, Timeout: 10 * time.Second}
	if _, err := r.Send(context.Background(), "continue", "haiku", nil); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--resume sess-7") {
		t.Errorf("args log missing resume: %s", b)
	}
}

func TestRunnerSendFailsOnResumeError(t *testing.T) {
	t.Setenv("FAKEAGENT_FAIL_RESUME", "1")
	th := &Thread{Name: "claude", CLI: "claude", SessionID: "gone"}
	r := &Runner{Adapter: &ClaudeAdapter{BinPath: fakeBin(t)}, Thread: th, Timeout: 10 * time.Second}
	if _, err := r.Send(context.Background(), "continue", "", nil); err == nil {
		t.Fatal("want error when resume fails, got nil")
	}
}

func TestRunnerPlainAdapter(t *testing.T) {
	// Plain adapters capture whole stdout as the result (no stream parsing).
	// echo prints its args, simulating a plain CLI.
	th := &Thread{Name: "codex", CLI: "codex"}
	r := &Runner{
		Adapter: &PlainAdapter{
			CLIName: "codex", BinPath: "echo", Window: 200000,
			ArgsFn: func(msg, model string, extra []string) []string { return []string{msg} },
		},
		Thread:  th,
		Timeout: 10 * time.Second,
	}
	res, err := r.Send(context.Background(), "hello plain", "", nil)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Text != "hello plain" {
		t.Errorf("text = %q", res.Text)
	}
	if th.Turns != 1 {
		t.Errorf("turns = %d", th.Turns)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run TestRunner -v`
Expected: FAIL — `undefined: Runner`

- [ ] **Step 4: Implement** — create `internal/agent/runner.go`:

```go
package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// TurnResult is the outcome of one agent turn.
type TurnResult struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// Runner executes one turn of one thread: invoke the CLI fresh (per-turn
// resume), stream events, capture session id and real token usage.
type Runner struct {
	Adapter Adapter
	Thread  *Thread
	WorkDir string
	Timeout time.Duration // 0 = no timeout
	OnEvent func(Event)   // streaming callback (REPL prints); may be nil
}

// Send runs one turn. The thread's SessionID and context meter are updated
// in place; the caller is responsible for persisting the ThreadStore.
func (r *Runner) Send(ctx context.Context, msg, model string, extra []string) (TurnResult, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	args := r.Adapter.BuildArgs(msg, r.Thread.SessionID, model, extra)
	cmd := exec.CommandContext(ctx, r.Adapter.Bin(), args...)
	if r.WorkDir != "" {
		cmd.Dir = r.WorkDir
	}

	if !r.Adapter.SupportsStream() {
		out, err := cmd.Output()
		if err != nil {
			return TurnResult{}, classifyTurnError(r.Adapter.CLI(), err)
		}
		text := strings.TrimRight(string(out), "\n")
		res := TurnResult{Text: text, InputTokens: len(msg) / 4, OutputTokens: len(text) / 4}
		r.finish(res)
		return res, nil
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{}, fmt.Errorf("pipe %s stdout: %w", r.Adapter.CLI(), err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return TurnResult{}, classifyTurnError(r.Adapter.CLI(), err)
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // results can be large
	var res TurnResult
	var resultErr bool
	for sc.Scan() {
		ev, ok := r.Adapter.ParseEvent(sc.Bytes())
		if !ok {
			continue
		}
		if r.OnEvent != nil {
			r.OnEvent(ev)
		}
		switch ev.Type {
		case EventInit:
			r.Thread.SessionID = ev.SessionID
		case EventResult:
			res.Text = ev.Text
			res.InputTokens = ev.InputTokens
			res.OutputTokens = ev.OutputTokens
			resultErr = ev.IsError
			if ev.SessionID != "" {
				r.Thread.SessionID = ev.SessionID
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return TurnResult{}, fmt.Errorf("%s turn failed: %w: %s",
			r.Adapter.CLI(), err, strings.TrimSpace(stderr.String()))
	}
	if scanErr := sc.Err(); scanErr != nil {
		return TurnResult{}, fmt.Errorf("read %s stream: %w", r.Adapter.CLI(), scanErr)
	}
	if resultErr {
		r.finish(res) // usage is still real; meter it
		return res, fmt.Errorf("%s reported an error result: %s", r.Adapter.CLI(), res.Text)
	}
	r.finish(res)
	return res, nil
}

func (r *Runner) finish(res TurnResult) {
	r.Thread.ContextTokens = res.InputTokens + res.OutputTokens
	r.Thread.Turns++
	r.Thread.UpdatedAt = time.Now()
}

func classifyTurnError(cli string, err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Errorf("%s exited %d: %s", cli, ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("run %s: %w", cli, err)
}
```

- [ ] **Step 5: Run tests, then commit**

Run: `go test ./internal/agent/ -v`
Expected: PASS

```bash
git add internal/agent/ testdata/fakeagent
git commit -m "feat(agent): per-turn runner with fake-CLI lifecycle tests"
```

### Task 13: manager — dispatch, distill-and-restart, crash recovery, handoff

**Files:**
- Create: `internal/agent/manager.go`
- Test: `internal/agent/manager_test.go`

- [ ] **Step 1: Write the failing test** — create `internal/agent/manager_test.go`:

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
)

type managerFixture struct {
	m   *Manager
	bud *budget.Tracker
	mem *memory.Store
}

// fixedEmbedder always returns the same vector so memory writes succeed.
type fixedEmbedder struct{}

func (fixedEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func newManagerFixture(t *testing.T, window int) *managerFixture {
	t.Helper()
	dir := t.TempDir()
	ts, err := LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	if err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	mem, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })

	return &managerFixture{
		m: &Manager{
			Project:      config.Project{Name: "testproj", Path: dir},
			Threads:      ts,
			Adapters:     map[string]Adapter{"claude": &ClaudeAdapter{BinPath: fakeBin(t), Window: window}},
			Budget:       bud,
			Mem:          mem,
			Emb:          fixedEmbedder{},
			Summarize:    func(_ context.Context, text string) (string, error) { return "summary: " + text[:min13(20, len(text))], nil },
			ThresholdPct: 70,
			DistillModel: "haiku",
			Timeout:      10 * time.Second,
		},
		bud: bud,
		mem: mem,
	}
}

func min13(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestDispatchRecordsRealUsage(t *testing.T) {
	t.Setenv("FAKEAGENT_IN", "5000")
	t.Setenv("FAKEAGENT_OUT", "300")
	f := newManagerFixture(t, 200000)

	res, err := f.m.Dispatch(context.Background(), DispatchSpec{
		CLI: "claude", Model: "sonnet", Message: "implement it",
	}, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.InputTokens != 5000 || res.OutputTokens != 300 {
		t.Errorf("usage = %+v", res)
	}
	st, err := f.bud.State(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionCount != 1 {
		t.Errorf("budget rows = %d, want 1 (real usage recorded)", st.SessionCount)
	}
}

func TestDispatchDistillsAtThreshold(t *testing.T) {
	// Window 10000, threshold 70% -> 120+20=140... use big usage instead:
	// emit 9000 input tokens so 9000+200 > 7000 triggers distillation.
	t.Setenv("FAKEAGENT_IN", "9000")
	t.Setenv("FAKEAGENT_OUT", "200")
	t.Setenv("FAKEAGENT_TEXT", "handoff: decisions, files, in-flight")
	f := newManagerFixture(t, 10000)

	compacted := ""
	f.m.OnCompact = func(name string) { compacted = name }

	_, err := f.m.Dispatch(context.Background(), DispatchSpec{
		CLI: "claude", Model: "sonnet", Message: "big work",
	}, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	th := f.m.Threads.Get("claude", "claude")
	if th.SessionID != "" {
		t.Errorf("SessionID = %q, want cleared after distill", th.SessionID)
	}
	if th.LastDistillation == "" {
		t.Error("LastDistillation empty after distill")
	}
	if compacted != "claude" {
		t.Errorf("OnCompact got %q", compacted)
	}
	// Distillation saved to memory.
	items, err := f.mem.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.Kind == memory.KindDistillation {
			found = true
		}
	}
	if !found {
		t.Error("no distillation item written to memory")
	}
}

func TestDispatchRecoversFromDeadSession(t *testing.T) {
	t.Setenv("FAKEAGENT_FAIL_RESUME", "1")
	f := newManagerFixture(t, 200000)
	th := f.m.Threads.Get("claude", "claude")
	th.SessionID = "dead-session"
	th.LastDistillation = "we had decided to use sqlite"

	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKEAGENT_ARGS_LOG", log)

	res, err := f.m.Dispatch(context.Background(), DispatchSpec{
		CLI: "claude", Model: "sonnet", Message: "keep going",
	}, nil)
	if err != nil {
		t.Fatalf("Dispatch should recover, got: %v", err)
	}
	if res.Text == "" {
		t.Error("empty result after recovery")
	}
	b, _ := os.ReadFile(log)
	calls := strings.TrimSpace(string(b))
	if !strings.Contains(calls, "--resume dead-session") {
		t.Errorf("first call did not try resume:\n%s", calls)
	}
	if !strings.Contains(calls, "we had decided to use sqlite") {
		t.Errorf("recovery call not seeded with last distillation:\n%s", calls)
	}
}

func TestSeedMessage(t *testing.T) {
	f := newManagerFixture(t, 200000)
	ad := f.m.Adapters["claude"]

	t.Run("fresh thread gets role line", func(t *testing.T) {
		th := &Thread{Name: "claude", CLI: "claude"}
		got := f.m.seedMessage(th, ad, "hello")
		if !strings.Contains(got, "testproj") || !strings.Contains(got, "hello") {
			t.Errorf("seed = %q", got)
		}
	})
	t.Run("live session passes through", func(t *testing.T) {
		th := &Thread{Name: "claude", CLI: "claude", SessionID: "live", Turns: 2}
		if got := f.m.seedMessage(th, ad, "hello"); got != "hello" {
			t.Errorf("seed = %q, want passthrough", got)
		}
	})
	t.Run("restarted thread seeded with distillation", func(t *testing.T) {
		th := &Thread{Name: "claude", CLI: "claude", Turns: 5, LastDistillation: "decided X"}
		got := f.m.seedMessage(th, ad, "continue")
		if !strings.Contains(got, "decided X") {
			t.Errorf("seed = %q", got)
		}
	})
	t.Run("plain adapter seeded with rolling summary", func(t *testing.T) {
		plain := NewCodexAdapter()
		th := &Thread{Name: "codex", CLI: "codex", Summary: "earlier we tried Y"}
		got := f.m.seedMessage(th, plain, "next step")
		if !strings.Contains(got, "earlier we tried Y") || !strings.Contains(got, "next step") {
			t.Errorf("seed = %q", got)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run 'TestDispatch|TestSeed' -v`
Expected: FAIL — `undefined: Manager`, `undefined: DispatchSpec`

- [ ] **Step 3: Implement** — create `internal/agent/manager.go`:

```go
package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
)

// distillPrompt asks a thread for a structured handoff before restart.
const distillPrompt = `Summarize the state of this work as a structured handoff for a fresh session: decisions made, files touched, work in flight, and dead ends to avoid. Short bullet lists; be dense.`

// DispatchSpec is one routed message to an agent thread.
type DispatchSpec struct {
	Thread  string   // thread name; "" defaults to the CLI name
	CLI     string   // claude | codex | agy
	Model   string   // resolved model id (tier mapping already applied)
	Message string
	Extra   []string // extra CLI options from the brain (e.g. --add-dir)
}

// Manager owns a project's agent threads: lazy start, context metering,
// distill-and-restart, crash recovery, budget recording, interactive handoff.
type Manager struct {
	Project      config.Project
	Threads      *ThreadStore
	Adapters     map[string]Adapter
	Budget       *budget.Tracker // nil ok (tests)
	Mem          *memory.Store   // nil ok; distillations land here
	Emb          memory.Embedder // nil ok
	Summarize    func(ctx context.Context, text string) (string, error) // cheap local summarizer
	ThresholdPct float64 // distill when context exceeds this percent of window
	DistillModel string  // model for distill turns (haiku tier)
	Timeout      time.Duration
	OnCompact    func(threadName string) // REPL shows "thread compacted"; may be nil
}

// Dispatch sends one message to a thread, lazily creating it, and handles
// the full lifecycle: seeding, crash recovery, real-usage budget recording,
// and distill-and-restart at the context threshold.
func (m *Manager) Dispatch(ctx context.Context, spec DispatchSpec, onEvent func(Event)) (TurnResult, error) {
	ad, ok := m.Adapters[spec.CLI]
	if !ok {
		return TurnResult{}, fmt.Errorf("no adapter for CLI %q", spec.CLI)
	}
	name := spec.Thread
	if name == "" {
		name = spec.CLI
	}
	th := m.Threads.Get(name, spec.CLI)
	run := &Runner{Adapter: ad, Thread: th, WorkDir: m.Project.Path, Timeout: m.Timeout, OnEvent: onEvent}

	msg := m.seedMessage(th, ad, spec.Message)
	res, err := run.Send(ctx, msg, spec.Model, spec.Extra)
	if err != nil && th.SessionID != "" && ad.SupportsResume() {
		// Crash recovery: the CLI's session may be gone. Roll back to the last
		// checkpoint — rebuild from distillation + rolling summary.
		th.SessionID = ""
		msg = m.seedMessage(th, ad, spec.Message)
		res, err = run.Send(ctx, msg, spec.Model, spec.Extra)
	}
	m.record(ctx, spec, res, err)
	if err != nil {
		_ = m.Threads.Save()
		return TurnResult{}, err
	}
	if !ad.SupportsResume() {
		m.updateRollingSummary(ctx, th, spec.Message, res.Text)
	}
	m.maybeDistill(ctx, th, ad)
	if err := m.Threads.Save(); err != nil {
		return res, fmt.Errorf("save threads: %w", err)
	}
	return res, nil
}

// seedMessage prepares the outbound message according to the thread's state:
// fresh threads get a role line, restarted threads get the last distillation,
// non-resume threads get the rolling summary.
func (m *Manager) seedMessage(th *Thread, ad Adapter, msg string) string {
	if ad.SupportsResume() {
		if th.SessionID != "" {
			return msg
		}
		var parts []string
		if th.LastDistillation != "" {
			parts = append(parts, "Handoff from the previous session of this thread:\n"+th.LastDistillation)
		} else if th.Turns == 0 {
			parts = append(parts, fmt.Sprintf(
				"You are the long-running %q agent thread of styx for project %s. Project context auto-loads from .claude/context.md when present.",
				th.Name, m.Project.Name))
		}
		parts = append(parts, msg)
		return strings.Join(parts, "\n\n")
	}
	if th.Summary == "" {
		return msg
	}
	return "Context from earlier in this conversation:\n" + th.Summary + "\n\nUser: " + msg
}

// record logs real token usage (from stream-json events) to the budget DB,
// replacing len/4 estimates for cloud channels.
func (m *Manager) record(ctx context.Context, spec DispatchSpec, res TurnResult, sendErr error) {
	if m.Budget == nil {
		return
	}
	kind := ""
	if sendErr != nil {
		kind = "other"
	}
	_ = m.Budget.Record(ctx, budget.Event{
		Channel:   spec.CLI,
		Verb:      "thread",
		Model:     spec.Model,
		TokensIn:  res.InputTokens,
		TokensOut: res.OutputTokens,
		Success:   sendErr == nil,
		ErrorKind: kind,
	})
}

// maybeDistill restarts a resume-capable thread when its context crosses the
// threshold: ask the session itself for a handoff (cheap tier), save it to
// memory, and clear the session so the next turn seeds fresh.
func (m *Manager) maybeDistill(ctx context.Context, th *Thread, ad Adapter) {
	if !ad.SupportsResume() || th.SessionID == "" || ad.ContextWindow() <= 0 || m.ThresholdPct <= 0 {
		return
	}
	pct := float64(th.ContextTokens) / float64(ad.ContextWindow()) * 100
	if pct < m.ThresholdPct {
		return
	}
	run := &Runner{Adapter: ad, Thread: th, WorkDir: m.Project.Path, Timeout: m.Timeout}
	res, err := run.Send(ctx, distillPrompt, m.DistillModel, nil)
	if err != nil || res.Text == "" {
		return // best-effort; the next turn will retry
	}
	th.LastDistillation = res.Text
	th.SessionID = ""
	th.ContextTokens = 0
	if m.OnCompact != nil {
		m.OnCompact(th.Name)
	}
	m.saveMemory(ctx, memory.KindDistillation, res.Text, "thread/"+th.Name)
}

// updateRollingSummary maintains styx-side continuity for non-resume CLIs.
func (m *Manager) updateRollingSummary(ctx context.Context, th *Thread, userMsg, reply string) {
	if m.Summarize == nil {
		return
	}
	convo := th.Summary + "\nUser: " + userMsg + "\nAgent: " + reply
	if sum, err := m.Summarize(ctx, convo); err == nil && sum != "" {
		th.Summary = sum
	}
}

// saveMemory embeds and stores text; failures are non-fatal (memory is an
// enhancement, never a blocker).
func (m *Manager) saveMemory(ctx context.Context, kind memory.Kind, text, source string) {
	if m.Mem == nil || m.Emb == nil {
		return
	}
	vec, err := m.Emb.Embed(ctx, text)
	if err != nil {
		return
	}
	_, _ = m.Mem.Add(ctx, memory.Item{Kind: kind, Text: text, Source: source, Embedding: vec})
}

// StatusLines renders one line per thread for the brain and /status.
func (m *Manager) StatusLines() []string {
	names := make([]string, 0, len(m.Threads.Threads))
	for n := range m.Threads.Threads {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		th := m.Threads.Threads[n]
		win := 200000
		if ad, ok := m.Adapters[th.CLI]; ok && ad.ContextWindow() > 0 {
			win = ad.ContextWindow()
		}
		pct := float64(th.ContextTokens) / float64(win) * 100
		out = append(out, fmt.Sprintf("%s (%s): %d turns, context %.0f%%", n, th.CLI, th.Turns, pct))
	}
	return out
}

// Handoff opens interactive claude on the thread's session (zoom-in), then
// ingests a summary back into the thread and memory on exit.
//
// Note: claude's interactive --resume forks the session, so the post-handoff
// summary turn sees the pre-handoff context; the ingest is best-effort.
func (m *Manager) Handoff(ctx context.Context, threadName string) error {
	th, ok := m.Threads.Threads[threadName]
	if !ok || th.CLI != "claude" {
		return fmt.Errorf("handoff requires an existing claude thread (got %q)", threadName)
	}
	ad := m.Adapters["claude"]
	args := []string{}
	if th.SessionID != "" {
		args = append(args, "--resume", th.SessionID)
	}
	cmd := exec.CommandContext(ctx, ad.Bin(), args...)
	cmd.Dir = m.Project.Path
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("interactive claude: %w", err)
	}
	run := &Runner{Adapter: ad, Thread: th, WorkDir: m.Project.Path, Timeout: m.Timeout}
	res, err := run.Send(ctx,
		"An interactive working session on this thread just ended. Summarize what was likely accomplished and what follow-ups remain, based on this conversation so far.",
		m.DistillModel, nil)
	if err == nil && res.Text != "" {
		th.Summary = res.Text
		m.saveMemory(ctx, memory.KindDistillation, res.Text, "handoff/"+threadName)
	}
	return m.Threads.Save()
}
```

**Note:** this file references `budget.Event{... Model: ...}` — the `Model` field lands in Task 14. If implementing strictly in order, Task 14's budget change is a prerequisite for *compiling* this task; do Task 14's Steps 1–4 first if the build breaks, or temporarily omit the `Model:` line and restore it in Task 14.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/
git commit -m "feat(agent): thread manager with distill-and-restart and crash recovery"
```

---

## Phase 5 — Budget & reliability

### Task 14: budget — Event.Model, model column, ModelCount

**Files:**
- Modify: `internal/budget/budget.go`
- Test: `internal/budget/budget_test.go` (append; create if absent)

- [ ] **Step 1: Write the failing test** — append to `internal/budget/budget_test.go` (create the file with `package budget` + imports `context`, `path/filepath`, `testing`, `time` if it doesn't exist):

```go
func TestModelCount(t *testing.T) {
	tr, err := New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	ctx := context.Background()
	for _, m := range []string{"fable", "fable", "sonnet"} {
		if err := tr.Record(ctx, Event{Channel: "claude", Verb: "thread", Model: m, Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Record(ctx, Event{Channel: "codex", Verb: "thread", Model: "gpt-5", Success: true}); err != nil {
		t.Fatal(err)
	}
	n, err := tr.ModelCount(ctx, "claude", "fable", WindowWeek)
	if err != nil {
		t.Fatalf("ModelCount: %v", err)
	}
	if n != 2 {
		t.Errorf("fable count = %d, want 2", n)
	}
	n, err = tr.ModelCount(ctx, "claude", "sonnet", WindowWeek)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sonnet count = %d, want 1", n)
	}
}

func TestModelColumnMigratesExistingDB(t *testing.T) {
	// Open once (creates schema), close, reopen — the ALTER must be idempotent.
	p := filepath.Join(t.TempDir(), "usage.db")
	tr, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	tr.Close()
	tr2, err := New(p)
	if err != nil {
		t.Fatalf("reopen after migration: %v", err)
	}
	defer tr2.Close()
	if err := tr2.Record(context.Background(), Event{Channel: "claude", Verb: "x", Model: "haiku", Success: true}); err != nil {
		t.Fatalf("record after reopen: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/budget/ -run TestModel -v`
Expected: FAIL — `unknown field Model` / `undefined: tr.ModelCount`

- [ ] **Step 3: Implement** — in `internal/budget/budget.go`:

Add `Model` to `Event`:

```go
// Event is a single usage record.
type Event struct {
	Channel   string
	Verb      string
	Model     string // model/tier used, e.g. "fable", "sonnet", "qwen2.5-coder:14b"
	TokensIn  int
	TokensOut int
	Success   bool
	ErrorKind string // "", "timeout", "429", "5xx", "other"
}
```

In `New`, after the schema exec, add the idempotent migration:

```go
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	// v0.3 migration: per-model message counters for tier-aware degradation.
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE usage ADD COLUMN model TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, fmt.Errorf("migrate usage.model column: %w", err)
		}
	}
```

(Add `"strings"` to the imports.)

Update `Record` to write the column:

```go
	_, err := t.db.ExecContext(ctx,
		`INSERT INTO usage (ts, channel, verb, model, tokens_in, tokens_out, success, error_kind) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), e.Channel, e.Verb, e.Model, e.TokensIn, e.TokensOut, successInt, e.ErrorKind)
```

Add the counter:

```go
// ModelCount returns the number of usage rows for (channel, model) within
// window. The REPL uses this for tier-aware degradation (fable -> opus).
func (t *Tracker) ModelCount(ctx context.Context, channel, model string, window time.Duration) (int, error) {
	cutoff := time.Now().Add(-window).Unix()
	row := t.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage WHERE channel = ? AND model = ? AND ts >= ?`,
		channel, model, cutoff)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count model messages for %s/%s: %w", channel, model, err)
	}
	return n, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/budget/ ./internal/agent/ -v`
Expected: PASS (agent tests now compile with `Model:` field)

- [ ] **Step 5: Commit**

```bash
git add internal/budget/
git commit -m "feat(budget): per-model message counters for tier degradation"
```

### Task 15: WithTimeout decorator + circuit breaker wiring

**Files:**
- Modify: `internal/channel/decorator.go` (append `WithTimeout`)
- Modify: `internal/router/router.go` (BreakerSource)
- Modify: `cmd/styx/dispatch.go` (wire both)
- Test: `internal/channel/decorator_test.go` (append)
- Test: `internal/router/breaker_test.go` (create)

- [ ] **Step 1: Write the failing decorator test** — append to `internal/channel/decorator_test.go`:

```go
// slowChannel blocks until its context is cancelled.
type slowChannel struct{}

func (slowChannel) Name() string { return "slow" }
func (slowChannel) BudgetState(context.Context) (Budget, error) { return Budget{}, nil }
func (slowChannel) Send(ctx context.Context, _ Request) (Response, error) {
	<-ctx.Done()
	return Response{}, ctx.Err()
}

func TestWithTimeoutCancelsSlowSend(t *testing.T) {
	w := &WithTimeout{Inner: slowChannel{}, D: 50 * time.Millisecond}
	start := time.Now()
	_, err := w.Send(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not fire promptly")
	}
}

func TestWithTimeoutSkipsInteractive(t *testing.T) {
	// Interactive sends hand over the terminal; they must never be timed out.
	w := &WithTimeout{Inner: &fakeOK{}, D: time.Nanosecond}
	if _, err := w.Send(context.Background(), Request{Interactive: true}); err != nil {
		t.Fatalf("interactive send timed out: %v", err)
	}
}

// fakeOK returns immediately.
type fakeOK struct{}

func (*fakeOK) Name() string { return "ok" }
func (*fakeOK) BudgetState(context.Context) (Budget, error) { return Budget{}, nil }
func (*fakeOK) Send(context.Context, Request) (Response, error) { return Response{Text: "ok"}, nil }
```

(Add `"time"` to the test imports if missing.)

- [ ] **Step 2: Implement the decorator** — append to `internal/channel/decorator.go`:

```go
// WithTimeout decorates a Channel so every non-interactive Send gets a
// deadline. Interactive sends hand the terminal to the child process and are
// never timed out.
type WithTimeout struct {
	Inner Channel
	D     time.Duration
}

func (w *WithTimeout) Name() string { return w.Inner.Name() }

func (w *WithTimeout) BudgetState(ctx context.Context) (Budget, error) {
	return w.Inner.BudgetState(ctx)
}

func (w *WithTimeout) Send(ctx context.Context, req Request) (Response, error) {
	if w.D <= 0 || req.Interactive {
		return w.Inner.Send(ctx, req)
	}
	ctx, cancel := context.WithTimeout(ctx, w.D)
	defer cancel()
	return w.Inner.Send(ctx, req)
}
```

(Add `"time"` to the file's imports.)

Run: `go test ./internal/channel/ -v` — Expected: PASS

- [ ] **Step 3: Write the failing breaker test** — create `internal/router/breaker_test.go`:

```go
package router

import (
	"context"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
)

type stubBreaker struct{ broken map[string]bool }

func (s stubBreaker) Broken(_ context.Context, ch string) bool { return s.broken[ch] }

func TestRouteSkipsBrokenChannel(t *testing.T) {
	r := &Router{
		Rules: []config.Rule{{
			Verb:     "plan",
			Use:      "claude:sonnet-4-6",
			Fallback: []string{"codex:gpt-5"},
		}},
		Breaker: stubBreaker{broken: map[string]bool{"claude": true}},
	}
	d, err := r.Route(context.Background(), Request{Verb: "plan"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Channel != "codex" {
		t.Errorf("channel = %s, want codex (claude circuit open)", d.Channel)
	}
	if !d.Degraded {
		t.Error("Degraded should be true when breaker forces fallback")
	}
}

func TestRouteNilBreakerUnchanged(t *testing.T) {
	r := &Router{
		Rules: []config.Rule{{Verb: "plan", Use: "claude:sonnet-4-6"}},
	}
	d, err := r.Route(context.Background(), Request{Verb: "plan"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Channel != "claude" || d.Degraded {
		t.Errorf("decision = %+v", d)
	}
}
```

- [ ] **Step 4: Implement breaker wiring** — in `internal/router/router.go`:

Add the interface and field:

```go
// BreakerSource reports whether a channel's circuit is open (too many recent
// failures). The router routes around broken channels like over-cap ones.
type BreakerSource interface {
	Broken(ctx context.Context, channel string) bool
}
```

```go
// Router evaluates rules + budget state to produce a Decision.
type Router struct {
	Rules   []config.Rule
	Caps    config.BudgetCaps
	Budget  BudgetSource
	Breaker BreakerSource // optional
}
```

Add the combined check and use it in `Route` (replace both `r.overCap(ctx, ...)` calls in the degradation block):

```go
// unavailable reports whether a channel should be routed around: over its
// budget cap or with an open failure circuit.
func (r *Router) unavailable(ctx context.Context, ch string) bool {
	if r.overCap(ctx, ch) {
		return true
	}
	return r.Breaker != nil && r.Breaker.Broken(ctx, ch)
}
```

In `Route`, the degradation block becomes:

```go
	chosen := primary
	degraded := false
	reason := fmt.Sprintf("matched rule #%d -> %s:%s", idx, chosen.Channel, chosen.Model)
	if r.unavailable(ctx, chosen.Channel) {
		degraded = true
		for _, f := range fallback {
			if !r.unavailable(ctx, f.Channel) {
				reason = fmt.Sprintf("rule #%d primary (%s:%s) unavailable (over cap or circuit open); degraded to %s:%s",
					idx, primary.Channel, primary.Model, f.Channel, f.Model)
				chosen = f
				break
			}
		}
	}
```

- [ ] **Step 5: Wire both into the app** — in `cmd/styx/dispatch.go`:

Give `budgetSource` the breaker method (3 failures in 10 minutes opens the circuit):

```go
func (b *budgetSource) Broken(ctx context.Context, ch string) bool {
	broken, err := b.t.ShouldCircuitBreak(ctx, ch, 3, 10*time.Minute)
	return err == nil && broken
}
```

(Add `"time"` to imports.) In `loadApp`, set it:

```go
	bs := &budgetSource{t: t}
	rt := router.FromConfig(r, bs)
	rt.Breaker = bs
```

Wire timeouts into `defaultChannels` — change its signature and the `loadApp` call site:

```go
func defaultChannels(prog *progress.Tracker, r config.Routing) map[string]channel.Channel {
	a := agy.New()
	raw := map[string]channel.Channel{
		"claude": claude.New(),
		"codex":  codex.New(),
		"agy":    a,
		"gemini": a, // alias for backward-compatible routing rules
		"ollama": ollama.New(),
	}
	timeouts := map[string]int{
		"claude": r.Budget.Claude.TimeoutMinutes,
		"codex":  r.Budget.Codex.TimeoutMinutes,
		"agy":    r.Budget.Agy.TimeoutMinutes,
		"gemini": r.Budget.Agy.TimeoutMinutes,
	}
	wrapped := make(map[string]channel.Channel, len(raw))
	for name, ch := range raw {
		inner := ch
		if mins, ok := timeouts[name]; ok {
			if mins <= 0 {
				mins = 10 // claude/codex previously had NO timeout at all
			}
			inner = &channel.WithTimeout{Inner: inner, D: time.Duration(mins) * time.Minute}
		}
		wrapped[name] = &channel.WithProgress{Inner: inner, Tracker: prog, Label: name}
	}
	return wrapped
}
```

In `loadApp`: `channels: defaultChannels(p, r),`

**Check:** `rawChannel` in dispatch.go unwraps `WithProgress` only — after this change it returns the `WithTimeout` wrapper, which is correct (orchestration verbs keep timeout protection, lose only the narration).

- [ ] **Step 6: Run everything, then commit**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS

```bash
git add internal/channel/ internal/router/ cmd/styx/dispatch.go
git commit -m "feat(reliability): subprocess timeouts and circuit-breaker routing"
```

### Task 16: surface swallowed parse errors

`research.Parse` has a built-in garbage fallback (it returns the raw text as one IMPORTANT finding), so the swallowed errors today mostly hide *which* rounds fell back. Make them visible.

**Files:**
- Modify: `internal/research/loop.go:76`
- Modify: `cmd/styx/auto.go:274`

- [ ] **Step 1: Fix the research loop** — in `internal/research/loop.go`, replace:

```go
		c, _ := Parse(critRaw)
```

with:

```go
		c, err := Parse(critRaw)
		if err != nil {
			stCrit.Info("critique parse degraded: %v (raw text treated as one IMPORTANT finding)", err)
		}
```

- [ ] **Step 2: Fix the auto pipeline** — in `cmd/styx/auto.go`, replace:

```go
		c, _ := research.Parse(text)
		return len(c.Blocking), len(c.Important), text, nil
```

with:

```go
		c, err := research.Parse(text)
		if err != nil {
			logStatus("review parse degraded: %v (raw text treated as one IMPORTANT finding)", err)
		}
		return len(c.Blocking), len(c.Important), text, nil
```

- [ ] **Step 3: Run tests, build, commit**

Run: `go build ./... && go test ./internal/research/ ./cmd/...`
Expected: PASS

```bash
git add internal/research/loop.go cmd/styx/auto.go
git commit -m "fix: surface degraded critique/review parses instead of swallowing"
```

---

## Phase 6 — Doctor & REPL

### Task 17: `styx doctor`

**Files:**
- Create: `cmd/styx/doctor.go`
- Test: `cmd/styx/doctor_test.go`

- [ ] **Step 1: Write the failing test** — create `cmd/styx/doctor_test.go`:

```go
package main

import (
	"reflect"
	"testing"

	"github.com/ishaanbatra/styx/internal/brain"
)

func TestMissingFlags(t *testing.T) {
	card := brain.Card{
		CLI:           "claude",
		ExpectedFlags: []string{"--resume", "--output-format", "--model"},
	}
	tests := []struct {
		name string
		help string
		want []string
	}{
		{
			name: "all present",
			help: "Usage: claude [options]\n  --resume <id>\n  --output-format <fmt>\n  --model <m>\n",
			want: nil,
		},
		{
			name: "one missing",
			help: "Usage: claude [options]\n  --resume <id>\n  --model <m>\n",
			want: []string{"--output-format"},
		},
		{
			name: "all missing",
			help: "Usage: totally-different-tool\n",
			want: []string{"--resume", "--output-format", "--model"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingFlags(tt.help, card); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missingFlags = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOllamaModelsMissing(t *testing.T) {
	tags := `{"models":[{"name":"qwen3:4b"},{"name":"qwen2.5-coder:14b"}]}`
	got := ollamaModelsMissing(tags, []string{"qwen3:4b", "nomic-embed-text"})
	want := []string{"nomic-embed-text"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("missing = %v, want %v", got, want)
	}
	// Tag-suffix tolerance: "nomic-embed-text:latest" satisfies "nomic-embed-text".
	tags = `{"models":[{"name":"nomic-embed-text:latest"}]}`
	if got := ollamaModelsMissing(tags, []string{"nomic-embed-text"}); got != nil {
		t.Errorf("missing = %v, want nil (latest tag should match)", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run 'TestMissingFlags|TestOllamaModels' -v`
Expected: FAIL — `undefined: missingFlags`

- [ ] **Step 3: Implement** — create `cmd/styx/doctor.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brain"
	"github.com/ishaanbatra/styx/internal/config"
)

// cmdDoctor preflights the orchestrator: CLI presence and versions, capability
// card drift (--help vs ExpectedFlags), resume support, and ollama model
// availability. `styx doctor --fix` pulls missing ollama models.
func cmdDoctor(args []string) error {
	fix := false
	for _, a := range args {
		if a == "--fix" {
			fix = true
		}
	}
	r, err := config.LoadRouting()
	if err != nil {
		return fmt.Errorf("load routing: %w", err)
	}
	healthy := true

	for _, card := range brain.Cards {
		if card.Bin == "" {
			continue // ollama probed via HTTP below
		}
		if _, err := exec.LookPath(card.Bin); err != nil {
			fmt.Printf("✗ %s not found on PATH\n", card.Bin)
			healthy = false
			continue
		}
		version := probeOutput(card.Bin, "--version")
		help := probeOutput(card.Bin, "--help")
		missing := missingFlags(help, card)
		mode := "native resume"
		if card.ResumeProbe == "" || !strings.Contains(help, card.ResumeProbe) {
			mode = "styx-maintained continuity"
		}
		if len(missing) > 0 {
			fmt.Printf("⚠ %s %s — knowledge stale: --help missing %v (CLI updated? refresh internal/brain/cards.go) — %s\n",
				card.Bin, firstLine(version), missing, mode)
			healthy = false
		} else {
			fmt.Printf("✓ %s %s — card current — %s\n", card.Bin, firstLine(version), mode)
		}
	}

	// Ollama: server reachable + required models pulled.
	required := []string{r.Brain.Model, r.Brain.EmbedModel}
	tags, err := fetchOllamaTags("http://localhost:11434")
	if err != nil {
		fmt.Printf("✗ ollama unreachable: %v (REPL will degrade to ask-the-user routing)\n", err)
		return reportDoctor(false)
	}
	missing := ollamaModelsMissing(tags, required)
	if len(missing) == 0 {
		fmt.Printf("✓ ollama up — models present: %s\n", strings.Join(required, ", "))
	} else if fix {
		for _, m := range missing {
			fmt.Printf("… pulling %s\n", m)
			cmd := exec.Command("ollama", "pull", m)
			cmd.Stdout, cmd.Stderr = ioDiscardIfQuiet(), ioDiscardIfQuiet()
			if err := cmd.Run(); err != nil {
				fmt.Printf("✗ pull %s failed: %v\n", m, err)
				healthy = false
			} else {
				fmt.Printf("✓ pulled %s\n", m)
			}
		}
	} else {
		fmt.Printf("⚠ ollama up but missing models %v — run `styx doctor --fix` or `ollama pull <model>`\n", missing)
		healthy = false
	}
	return reportDoctor(healthy)
}

func reportDoctor(healthy bool) error {
	if healthy {
		fmt.Println("doctor: all clear")
		return nil
	}
	return fmt.Errorf("doctor found issues (see above)")
}

// probeOutput runs `bin arg` with a short timeout and returns combined output
// ("" on failure — absence of output is handled by the card checks).
func probeOutput(bin, arg string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, bin, arg).CombinedOutput()
	return string(out)
}

// missingFlags returns card.ExpectedFlags absent from the CLI's help text.
func missingFlags(help string, card brain.Card) []string {
	var missing []string
	for _, f := range card.ExpectedFlags {
		if !strings.Contains(help, f) {
			missing = append(missing, f)
		}
	}
	return missing
}

func fetchOllamaTags(baseURL string) (string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ollamaModelsMissing parses /api/tags JSON and returns required models not
// present. "name" or "name:tag" both satisfy a required bare name.
func ollamaModelsMissing(tagsJSON string, required []string) []string {
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	_ = json.Unmarshal([]byte(tagsJSON), &tags)
	have := map[string]bool{}
	for _, m := range tags.Models {
		have[m.Name] = true
		if i := strings.Index(m.Name, ":"); i > 0 {
			have[m.Name[:i]] = true
		}
	}
	var missing []string
	for _, r := range required {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	return missing
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func ioDiscardIfQuiet() io.Writer {
	if globalQuiet {
		return io.Discard
	}
	return nil // nil keeps the child's default (inherit), but exec needs a writer; use os.Stdout
}
```

**Correction to the last helper** (keep it simple — exec needs concrete writers):

```go
func ioDiscardIfQuiet() io.Writer {
	if globalQuiet {
		return io.Discard
	}
	return os.Stdout
}
```

(Add `"os"` to imports.)

- [ ] **Step 3b: Probe model-tier availability** — a model can be pulled out from under styx even when the CLI is healthy (e.g. the 2026-06-12 worldwide suspension of Fable 5 / Mythos 5 under a US export directive). Each distinct claude `--model` alias in `[tiers]` is probed with a one-token call; an unavailable alias is flagged so the user remaps it in `routing.toml`.

Append to `cmd/styx/doctor.go`:

```go
// claudeModelOK reports whether `claude --model <alias>` can serve a trivial
// request. Used to catch models that exist in the card but are not currently
// callable (suspended, deprecated, or absent from the user's subscription).
func claudeModelOK(alias string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// -p one-shot, cheapest possible: a single token, permissions pre-granted.
	cmd := exec.CommandContext(ctx, "claude", "-p", "ok",
		"--model", alias, "--dangerously-skip-permissions")
	return cmd.Run() == nil
}

// checkTiers probes each distinct tier->alias mapping for a callable claude
// model. Returns false if any mapped alias is unavailable.
func checkTiers(tiers map[string]string) bool {
	if _, err := exec.LookPath("claude"); err != nil {
		return true // claude absence already reported by the card loop
	}
	seen := map[string]bool{}
	ok := true
	for tier, alias := range tiers {
		if seen[alias] {
			continue
		}
		seen[alias] = true
		if claudeModelOK(alias) {
			fmt.Printf("✓ tier %s -> claude --model %s — callable\n", tier, alias)
		} else {
			fmt.Printf("✗ tier %s -> claude --model %s — NOT callable (suspended/deprecated/not on your plan); remap it in ~/.config/styx/routing.toml [tiers]\n", tier, alias)
			ok = false
		}
	}
	return ok
}
```

Call it from `cmdDoctor` just before `return reportDoctor(healthy)`:

```go
	if !checkTiers(r.Tiers) {
		healthy = false
	}
	return reportDoctor(healthy)
```

Add a test to `cmd/styx/doctor_test.go` that exercises the dedup/aggregation logic against a fake `claudeModelOK` (inject it as a package var or pass a probe func) so the suite stays CLI-free; the live probe runs only under the manual smoke test below.

> Note: with the default `[tiers]` (`fable -> opus`), doctor probes `opus`, `sonnet`, `haiku` — it will not flag the suspended `fable` model because the tier no longer maps to it. If a user restores `fable = "fable"` while the suspension is in effect, doctor will correctly flag it as not callable.

- [ ] **Step 4: Wire the verb** — in `cmd/styx/dispatch.go`, add to the first (no-app) switch in `dispatch`:

```go
	case "doctor":
		return cmdDoctor(args)
```

- [ ] **Step 5: Run tests, smoke it, commit**

Run: `go test ./cmd/styx/ -v && go build ./... && go run ./cmd/styx doctor || true`
Expected: tests PASS; doctor prints a real report for your machine (exit 1 is fine if something's missing)

```bash
git add cmd/styx/
git commit -m "feat(doctor): CLI capability probing, card drift detection, ollama model checks"
```

### Task 18: REPL session core

**Files:**
- Create: `cmd/styx/repl.go`
- Test: `cmd/styx/repl_test.go`

- [ ] **Step 1: Write the failing test** — create `cmd/styx/repl_test.go`:

```go
package main

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/brain"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
)

// scriptedBrain returns queued actions (or an error) in order.
type scriptedBrain struct {
	actions []brain.Action
	err     error
	i       int
}

func (s *scriptedBrain) Decide(context.Context, brain.Turn) (brain.Action, error) {
	if s.err != nil {
		return brain.Action{}, s.err
	}
	a := s.actions[s.i]
	if s.i < len(s.actions)-1 {
		s.i++
	}
	return a, nil
}

type replEmbedder struct{}

func (replEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func newTestSession(t *testing.T, b brain.Brain, input string) (*replSession, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	mem, err := memory.Open(filepath.Join(dir, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	glob, err := memory.Open(filepath.Join(dir, "glob.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { glob.Close() })
	bud, err := budget.New(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	threads, err := agent.LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	if err != nil {
		t.Fatal(err)
	}
	fake, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	s := &replSession{
		proj:  config.Project{Name: "testproj", Path: dir},
		brain: b,
		mgr: &agent.Manager{
			Project:      config.Project{Name: "testproj", Path: dir},
			Threads:      threads,
			Adapters:     map[string]agent.Adapter{"claude": &agent.ClaudeAdapter{BinPath: fake}},
			Budget:       bud,
			Mem:          mem,
			Emb:          replEmbedder{},
			ThresholdPct: 70,
			DistillModel: "haiku",
			Timeout:      10 * time.Second,
		},
		mem:      mem,
		glob:     glob,
		emb:      replEmbedder{},
		tiers:    map[string]string{"fable": "fable", "opus": "opus", "sonnet": "sonnet", "haiku": "haiku"},
		fableCap: 2,
		tracker:  bud,
		pipelines: map[string]func(context.Context, string) error{
			"research": func(context.Context, string) error { out.WriteString("[pipeline research ran]\n"); return nil },
		},
		in:  bufio.NewReader(strings.NewReader(input)),
		out: out,
	}
	return s, out
}

func TestTurnReply(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionReply, Reply: "two threads are live", Confidence: 0.9}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "what's running?"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !strings.Contains(out.String(), "two threads are live") {
		t.Errorf("output = %q", out.String())
	}
}

func TestTurnDispatchPrintsRoutingLineAndResult(t *testing.T) {
	t.Setenv("FAKEAGENT_TEXT", "refactor done")
	b := &scriptedBrain{actions: []brain.Action{{
		Action:     brain.ActionDispatch,
		Dispatches: []brain.Dispatch{{Thread: "claude", Model: "sonnet", Message: "refactor it", Rationale: "implementation work"}},
		Confidence: 0.9,
	}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "refactor the loader"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "◆ claude·sonnet › implementation work") {
		t.Errorf("missing routing line:\n%s", got)
	}
	if !strings.Contains(got, "refactor done") {
		t.Errorf("missing agent result:\n%s", got)
	}
}

func TestTurnRememberStoresRoutingPreference(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{
		Action: brain.ActionRemember, Remember: "routing-preference: codex handles reviews", Confidence: 1,
	}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "no, codex should review"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	items, err := s.mem.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != memory.KindRoutingPreference {
		t.Errorf("memory = %+v", items)
	}
	if !strings.Contains(out.String(), "remembered") {
		t.Errorf("no confirmation printed: %q", out.String())
	}
}

func TestTurnPipeline(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionPipeline, Pipeline: "research", Confidence: 0.9}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "research sync engines"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !strings.Contains(out.String(), "[pipeline research ran]") {
		t.Errorf("pipeline not invoked: %q", out.String())
	}
}

func TestTurnBrainDownAsksUser(t *testing.T) {
	t.Setenv("FAKEAGENT_TEXT", "manual route ok")
	b := &scriptedBrain{err: brain.ErrNeedUser}
	s, out := newTestSession(t, b, "claude\n")
	if err := s.turn(context.Background(), "do the thing"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "which thread") {
		t.Errorf("REPL did not ask the user:\n%s", got)
	}
	if !strings.Contains(got, "manual route ok") {
		t.Errorf("manual dispatch did not run:\n%s", got)
	}
}

func TestResolveModelFableDegradation(t *testing.T) {
	b := &scriptedBrain{}
	s, _ := newTestSession(t, b, "")
	// Below cap: fable passes through.
	if m, degraded := s.resolveModel("fable"); m != "fable" || degraded {
		t.Errorf("cold: model=%q degraded=%v", m, degraded)
	}
	// Record fableCap (=2) fable messages this week.
	for i := 0; i < 2; i++ {
		if err := s.tracker.Record(context.Background(), budget.Event{Channel: "claude", Verb: "thread", Model: "fable", Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	if m, degraded := s.resolveModel("fable"); m != "opus" || !degraded {
		t.Errorf("hot: model=%q degraded=%v, want opus/true", m, degraded)
	}
	// Non-tier strings pass through untouched (ollama model names).
	if m, _ := s.resolveModel("qwen2.5-coder:14b"); m != "qwen2.5-coder:14b" {
		t.Errorf("passthrough = %q", m)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run 'TestTurn|TestResolveModel' -v`
Expected: FAIL — `undefined: replSession`

- [ ] **Step 3: Implement the session core** — create `cmd/styx/repl.go`:

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/brain"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
)

const maxRecentTurns = 8

// replSession is one conversational styx session for one project. The brain
// routes each utterance; agent threads, pipelines, and memory do the work.
type replSession struct {
	proj       config.Project
	brain      brain.Brain
	mgr        *agent.Manager
	mem        *memory.Store // per-project
	glob       *memory.Store // cross-project
	emb        memory.Embedder
	tiers      map[string]string
	fableCap   int
	tracker    *budget.Tracker
	pipelines  map[string]func(ctx context.Context, arg string) error
	ollamaSend func(ctx context.Context, model, prompt string) (string, error)
	in         *bufio.Reader
	out        io.Writer
	outMu      sync.Mutex
	summary    string
	recent     []string
	lastAction *brain.Action
}

// turn runs one full loop iteration: recall -> decide -> act.
func (s *replSession) turn(ctx context.Context, utterance string) error {
	hits, err := memory.Recall(ctx, s.emb, utterance, 5, s.mem, s.glob)
	if err != nil {
		hits = nil // recall is an enhancement, never a blocker
	}
	t := brain.Turn{
		Utterance:    utterance,
		Summary:      s.summary,
		RecentTurns:  s.recent,
		ThreadStatus: s.mgr.StatusLines(),
		MemoryHits:   renderHits(hits),
	}
	act, err := s.brain.Decide(ctx, t)
	if err != nil {
		if errors.Is(err, brain.ErrNeedUser) {
			return s.askUserRoute(ctx, utterance)
		}
		return err
	}
	s.lastAction = &act
	return s.execute(ctx, utterance, act)
}

func (s *replSession) execute(ctx context.Context, utterance string, act brain.Action) error {
	switch act.Action {
	case brain.ActionReply:
		s.println(act.Reply)
		s.pushRecent(utterance, act.Reply)
		return nil
	case brain.ActionDispatch, brain.ActionParallelDispatch:
		return s.runDispatches(ctx, utterance, act.Dispatches)
	case brain.ActionPipeline:
		s.println(fmt.Sprintf("◆ pipeline › %s", act.Pipeline))
		fn, ok := s.pipelines[act.Pipeline]
		if !ok {
			return fmt.Errorf("no pipeline %q wired", act.Pipeline)
		}
		err := fn(ctx, utterance)
		s.pushRecent(utterance, "(ran "+act.Pipeline+" pipeline)")
		return err
	case brain.ActionHandoff:
		thread := "claude"
		if len(act.Dispatches) > 0 && act.Dispatches[0].Thread != "" {
			thread = act.Dispatches[0].Thread
		}
		s.println(fmt.Sprintf("◆ handoff › opening interactive %s (exit to return to styx)", thread))
		// Lazily create the thread so first-ever handoffs work too.
		s.mgr.Threads.Get(thread, thread)
		err := s.mgr.Handoff(ctx, thread)
		s.pushRecent(utterance, "(interactive handoff)")
		return err
	case brain.ActionRemember:
		return s.saveMemoryText(ctx, act.Remember)
	default: // escalate with no escalator, or anything unexpected
		return s.askUserRoute(ctx, utterance)
	}
}

// runDispatches executes one or more dispatches; multiple run concurrently
// with output serialized through s.println.
func (s *replSession) runDispatches(ctx context.Context, utterance string, ds []brain.Dispatch) error {
	if len(ds) == 0 {
		return errors.New("brain returned a dispatch with no dispatches")
	}
	var wg sync.WaitGroup
	errs := make([]error, len(ds))
	for i, d := range ds {
		model, degraded := s.resolveModel(s.defaultModel(d))
		line := fmt.Sprintf("◆ %s·%s › %s", d.Thread, s.defaultModel(d), d.Rationale)
		if degraded {
			line += " (fable hot this week → opus)"
		}
		s.println(line)
		wg.Add(1)
		go func(i int, d brain.Dispatch, model string) {
			defer wg.Done()
			errs[i] = s.runOneDispatch(ctx, d, model)
		}(i, d, model)
	}
	wg.Wait()
	s.pushRecent(utterance, fmt.Sprintf("(dispatched to %d thread(s))", len(ds)))
	return errors.Join(errs...)
}

func (s *replSession) runOneDispatch(ctx context.Context, d brain.Dispatch, model string) error {
	if d.Thread == "ollama" {
		if s.ollamaSend == nil {
			return errors.New("ollama dispatch not wired")
		}
		text, err := s.ollamaSend(ctx, model, d.Message)
		if err != nil {
			return fmt.Errorf("ollama: %w", err)
		}
		s.println(text)
		return nil
	}
	res, err := s.mgr.Dispatch(ctx, agent.DispatchSpec{
		Thread: d.Thread, CLI: d.Thread, Model: model, Message: d.Message, Extra: d.CLIOptions,
	}, s.printEvent)
	if err != nil {
		return fmt.Errorf("%s: %w", d.Thread, err)
	}
	// Streaming adapters already printed text events; plain ones did not.
	if ad, ok := s.mgr.Adapters[d.Thread]; ok && !ad.SupportsStream() {
		s.println(res.Text)
	}
	return nil
}

// printEvent renders streamed agent events into the REPL.
func (s *replSession) printEvent(e agent.Event) {
	switch e.Type {
	case agent.EventText:
		s.println(e.Text)
	case agent.EventResult:
		// Final text already streamed as EventText for claude; print nothing.
	}
}

// defaultModel fills d.Model when the brain omitted it.
func (s *replSession) defaultModel(d brain.Dispatch) string {
	if d.Model != "" {
		return d.Model
	}
	if d.Thread == "ollama" {
		return "qwen2.5-coder:14b"
	}
	return "sonnet"
}

// resolveModel maps a tier to a CLI model id, degrading fable -> opus when
// the weekly fable budget runs hot. Non-tier strings pass through.
func (s *replSession) resolveModel(tier string) (string, bool) {
	m, ok := s.tiers[tier]
	if !ok {
		return tier, false
	}
	if tier == "fable" && s.fableHot() {
		if opus, ok := s.tiers["opus"]; ok {
			return opus, true
		}
	}
	return m, false
}

func (s *replSession) fableHot() bool {
	if s.fableCap <= 0 || s.tracker == nil {
		return false
	}
	n, err := s.tracker.ModelCount(context.Background(), "claude", s.tiers["fable"], budget.WindowWeek)
	return err == nil && n >= s.fableCap
}

// askUserRoute is the never-brick path: ollama is down or the brain emitted
// garbage twice, so the user routes this turn manually.
func (s *replSession) askUserRoute(ctx context.Context, utterance string) error {
	s.print("brain unavailable — which thread? [claude/codex/agy/ollama/skip]: ")
	line, err := s.in.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read routing choice: %w", err)
	}
	choice := strings.TrimSpace(line)
	switch choice {
	case "", "skip":
		s.println("skipped")
		return nil
	case "claude", "codex", "agy", "ollama":
		d := brain.Dispatch{Thread: choice, Message: utterance, Rationale: "manual route"}
		return s.runDispatches(ctx, utterance, []brain.Dispatch{d})
	default:
		s.println("unknown thread " + choice + "; skipped")
		return nil
	}
}

// saveMemoryText stores an explicit remember action. Routing corrections
// (prefixed "routing-preference: " by the brain) get their own kind so recall
// can teach the brain this user's preferences.
func (s *replSession) saveMemoryText(ctx context.Context, text string) error {
	kind := memory.KindFact
	if strings.HasPrefix(text, "routing-preference:") {
		kind = memory.KindRoutingPreference
	}
	vec, err := s.emb.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed memory: %w", err)
	}
	if _, err := s.mem.Add(ctx, memory.Item{Kind: kind, Text: text, Source: "repl", Embedding: vec}); err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	s.println("◆ remembered")
	return nil
}

func (s *replSession) pushRecent(utterance, outcome string) {
	s.recent = append(s.recent, "user: "+utterance, "styx: "+outcome)
	if len(s.recent) > maxRecentTurns {
		s.recent = s.recent[len(s.recent)-maxRecentTurns:]
	}
}

func (s *replSession) println(line string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	fmt.Fprintln(s.out, line)
}

func (s *replSession) print(text string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	fmt.Fprint(s.out, text)
}

func renderHits(hits []memory.Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, fmt.Sprintf("[%s] %s", h.Item.Kind, h.Item.Text))
	}
	return out
}

// lastActionJSON renders the previous routing decision for /why.
func (s *replSession) lastActionJSON() string {
	if s.lastAction == nil {
		return "(no decision yet)"
	}
	b, err := json.MarshalIndent(s.lastAction, "", "  ")
	if err != nil {
		return fmt.Sprintf("(%v)", err)
	}
	return string(b)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/styx/repl.go cmd/styx/repl_test.go
git commit -m "feat(repl): session core — recall, decide, act, never-brick fallback"
```

### Task 19: REPL frontend — bare `styx`, `styx "..."`, slash commands

**Files:**
- Modify: `cmd/styx/repl.go` (append the frontend)
- Modify: `cmd/styx/main.go`
- Modify: `cmd/styx/dispatch.go`
- Modify: `cmd/styx/help.go`

- [ ] **Step 1: Implement session construction** — append to `cmd/styx/repl.go`:

```go
// newREPLSession wires a production session for the current project.
// The returned cleanup closes the memory stores.
func newREPLSession(a *app) (*replSession, func(), error) {
	proj, err := project.Current()
	if err != nil {
		return nil, nil, err
	}
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, nil, err
	}
	mem, err := memory.Open(filepath.Join(memDir, proj.Name+".db"))
	if err != nil {
		return nil, nil, fmt.Errorf("open project memory: %w", err)
	}
	glob, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		mem.Close()
		return nil, nil, fmt.Errorf("open global memory: %w", err)
	}
	cleanup := func() { mem.Close(); glob.Close() }

	emb := memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel)
	threads, err := agent.LoadThreads(proj.Name)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	och := rawChannel(a.channels["ollama"])
	summarize := func(ctx context.Context, text string) (string, error) {
		resp, err := och.Send(ctx, channel.Request{
			Model:  a.routing.Brain.Model,
			Prompt: "Compress this conversation state into a dense summary preserving decisions, open questions, and file references:\n\n" + text,
		})
		return resp.Text, err
	}

	timeout := 10 * time.Minute
	if a.routing.Budget.Claude.TimeoutMinutes > 0 {
		timeout = time.Duration(a.routing.Budget.Claude.TimeoutMinutes) * time.Minute
	}
	mgr := &agent.Manager{
		Project: proj,
		Threads: threads,
		Adapters: map[string]agent.Adapter{
			"claude": agent.NewClaudeAdapter(),
			"codex":  agent.NewCodexAdapter(),
			"agy":    agent.NewAgyAdapter(),
		},
		Budget:       a.tracker,
		Mem:          mem,
		Emb:          emb,
		Summarize:    summarize,
		ThresholdPct: a.routing.Brain.ContextThresholdPct,
		DistillModel: a.routing.Tiers["haiku"],
		Timeout:      timeout,
	}

	b := &brain.Ollama{
		BaseURL:             "http://localhost:11434",
		Model:               a.routing.Brain.Model,
		ConfidenceThreshold: a.routing.Brain.ConfidenceThreshold,
		Escalator: &brain.ClaudeEscalator{
			Channel: rawChannel(a.channels["claude"]),
			Model:   a.routing.Tiers["haiku"],
		},
	}

	s := &replSession{
		proj:     proj,
		brain:    b,
		mgr:      mgr,
		mem:      mem,
		glob:     glob,
		emb:      emb,
		tiers:    a.routing.Tiers,
		fableCap: a.routing.Brain.FableWeeklyCap,
		tracker:  a.tracker,
		pipelines: map[string]func(ctx context.Context, arg string) error{
			"research": func(_ context.Context, arg string) error {
				err := cmdResearch(a, []string{arg})
				if err == nil {
					indexNewestBrief(context.Background(), mem, emb, filepath.Join(proj.Path, proj.ResearchDir))
				}
				return err
			},
			"auto":   func(_ context.Context, arg string) error { return cmdAuto(a, []string{arg}) },
			"review": func(_ context.Context, _ string) error { return cmdReview(a, nil) },
			"intel":  func(_ context.Context, _ string) error { return cmdIntel(a, nil) },
		},
		ollamaSend: func(ctx context.Context, model, prompt string) (string, error) {
			resp, err := a.channels["ollama"].Send(ctx, channel.Request{Model: model, Prompt: prompt})
			return resp.Text, err
		},
		in:  bufio.NewReader(os.Stdin),
		out: os.Stdout,
	}
	mgr.OnCompact = func(name string) { s.println("↻ " + name + " thread compacted") }
	return s, cleanup, nil
}

// indexNewestBrief stores the freshest research brief in memory (best-effort).
func indexNewestBrief(ctx context.Context, mem *memory.Store, emb memory.Embedder, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return
	}
	var newest os.DirEntry
	var newestTime time.Time
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		if info.ModTime().After(newestTime) {
			newest, newestTime = e, info.ModTime()
		}
	}
	if newest == nil {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, newest.Name()))
	if err != nil {
		return
	}
	text := string(b)
	if len(text) > 4000 {
		text = text[:4000]
	}
	vec, err := emb.Embed(ctx, text)
	if err != nil {
		return
	}
	_, _ = mem.Add(ctx, memory.Item{
		Kind: memory.KindBrief, Text: text, Source: "pipeline/research:" + newest.Name(), Embedding: vec,
	})
}

// cmdREPL is bare `styx`: the persistent conversational session.
func cmdREPL(a *app) error {
	s, cleanup, err := newREPLSession(a)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Printf("styx — %s · /status /budget /threads /why /quit\n", s.proj.Name)
	for {
		fmt.Print("styx› ")
		line, err := s.in.ReadString('\n')
		if err != nil { // EOF (Ctrl-D)
			s.endSession()
			fmt.Println()
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if quit := s.slash(line); quit {
				s.endSession()
				return nil
			}
			continue
		}
		// Each turn gets its own interrupt scope: Ctrl-C cancels the in-flight
		// work and returns to the prompt instead of killing styx.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		if err := s.turn(ctx, line); err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "✗ interrupted")
			} else {
				fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			}
		}
		stop()
	}
}

// slash handles REPL slash commands; returns true to quit.
func (s *replSession) slash(line string) bool {
	switch strings.Fields(line)[0] {
	case "/quit", "/exit":
		return true
	case "/status", "/threads":
		lines := s.mgr.StatusLines()
		if len(lines) == 0 {
			s.println("no threads yet (they start lazily on first dispatch)")
		}
		for _, l := range lines {
			s.println(meterize(l))
		}
	case "/budget":
		if err := cmdBudget(nil); err != nil {
			s.println("budget: " + err.Error())
		}
	case "/why":
		s.println(s.lastActionJSON())
	default:
		s.println("unknown command (try /status /budget /threads /why /quit)")
	}
	return false
}

// meterize appends a 5-cell context meter to a "... context NN%" status line.
func meterize(line string) string {
	i := strings.LastIndex(line, "context ")
	if i < 0 {
		return line
	}
	var pct float64
	if _, err := fmt.Sscanf(line[i:], "context %f%%", &pct); err != nil {
		return line
	}
	filled := int(pct / 20)
	if filled > 5 {
		filled = 5
	}
	return line + "  " + strings.Repeat("▮", filled) + strings.Repeat("▯", 5-filled)
}

// endSession writes a session-end summary to project memory (best-effort).
func (s *replSession) endSession() {
	if len(s.recent) == 0 || s.mgr.Summarize == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sum, err := s.mgr.Summarize(ctx, strings.Join(s.recent, "\n"))
	if err != nil || sum == "" {
		return
	}
	vec, err := s.emb.Embed(ctx, sum)
	if err != nil {
		return
	}
	_, _ = s.mem.Add(ctx, memory.Item{
		Kind: memory.KindDistillation, Text: sum, Source: "repl-session", Embedding: vec,
	})
}

// cmdBrainTurn is `styx "..."`: one brain turn, then exit.
func cmdBrainTurn(a *app, utterance string) error {
	s, cleanup, err := newREPLSession(a)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return s.turn(ctx, utterance)
}
```

Add the needed imports to `cmd/styx/repl.go` (final set):

```go
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/brain"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/project"
)
```

- [ ] **Step 2: Wire main.go** — in `cmd/styx/main.go`, replace the bare-invocation block:

```go
	if len(rest) == 0 {
		printHelp()
		return
	}
```

with:

```go
	if len(rest) == 0 {
		// Bare `styx` opens the REPL in the current project.
		globalQuiet = quiet
		globalVerbose = verbose
		if err := ensureFirstRun(); err != nil {
			fmt.Fprintf(os.Stderr, "styx: setup error: %v\n", err)
			os.Exit(1)
		}
		a, err := loadApp()
		if err != nil {
			fmt.Fprintf(os.Stderr, "styx: %v\n", err)
			os.Exit(1)
		}
		defer a.tracker.Close()
		if err := cmdREPL(a); err != nil {
			fmt.Fprintf(os.Stderr, "styx: %v\n", err)
			os.Exit(1)
		}
		return
	}
```

- [ ] **Step 3: Wire unknown-verb fallthrough** — in `cmd/styx/dispatch.go`, replace the final line of `dispatch`:

```go
	return fmt.Errorf("unknown verb %q (run `styx help`)", verb)
```

with:

```go
	// Anything that isn't a verb is an utterance: `styx "fix the flaky test"`
	// runs one brain turn and exits.
	utterance := strings.TrimSpace(strings.Join(append([]string{verb}, args...), " "))
	return cmdBrainTurn(a, utterance)
```

(Add `"strings"` to dispatch.go's imports. Note this sits in the second switch, after `loadApp()` succeeded, so `a` is in scope.)

- [ ] **Step 4: Update help** — in `cmd/styx/help.go`, add these lines to the printed help text, alongside the existing verbs (match the file's existing formatting style):

```
  styx                      open the conversational REPL in this project
  styx "<anything>"         one brain-routed turn, then exit
  styx doctor [--fix]       preflight CLIs, capability cards, ollama models
```

and a note in the appropriate section:

```
  REPL slash commands: /status /budget /threads /why /quit
```

- [ ] **Step 5: Build, test everything, smoke it**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS

Manual smoke (requires ollama running with models pulled — `styx doctor --fix` first):

```bash
go run ./cmd/styx doctor --fix
go run ./cmd/styx "write a commit message for the staged changes"   # should route to ollama
go run ./cmd/styx        # REPL: try "hello", /status, /why, /quit
```

- [ ] **Step 6: Commit**

```bash
git add cmd/styx/
git commit -m "feat(repl): conversational frontend — bare styx, one-shot turns, slash commands"
```

### Checkpoint B — Dogfood the REPL on real work (gate before hardening)

> You now have a runnable `styx`. Use it for real *before* building the safety
> layer (Phase 8) — what you observe should shape the risk gate, the audit
> trail, and what memory needs to forget.

- [ ] **Use it.** Run `styx` in the styx repo and drive one small real change
  end to end (a tiny refactor or a doc fix) through dispatch + a pipeline.
- [ ] **Inspect by hand afterward:**
  - every memory row written: `sqlite3 ~/.config/styx/state/memory/styx.db 'select kind,text,source from memory'`
  - thread state JSON under the threads dir; `/why` output; `styx budget` rows
- [ ] **Write down the friction:** wrong routes, dispatches you did not want,
  memories you would never want recalled again, and — critically — anything that
  committed/pushed/opened a PR without you expecting it.
- [ ] **Decision gate.** Feed these observations into Phase 8 scope (which
  actions are ship-risk, what is worth auditing, what memory must decay). Record
  the findings in the decisions-log addendum. No commit required.

---

## Phase 8 — Safety, provenance & trust

These three tasks close the gaps a design review flagged: the REPL makes
brain-dispatch the *default* path, which silently widens the autonomy surface
versus today's explicit `styx auto`/`execute`. They are deliberately small and
spec-consistent (the brain proposes; the REPL — not the model — enforces).
Run them after Checkpoint B so the design is informed by real use.

### Task 19.1: risk tiers on the Action + REPL ship-confirmation gate

A coarse risk class (`read` | `edit` | `ship`) rides on every dispatch and
action. The brain sets it; the REPL enforces it: `ship` actions
(commit/push/PR/deploy and the `auto` pipeline) confirm with the user first,
and `read` dispatches drop the pre-granted write permission.

**Files:**
- Modify: `internal/brain/action.go`
- Modify: `internal/brain/prompt.go`
- Modify: `internal/agent/adapter.go` (add `DispatchSpec.ReadOnly`; claude arg builder)
- Modify: `cmd/styx/repl.go`
- Test: `internal/brain/action_test.go` (append), `cmd/styx/repl_test.go` (append)

- [ ] **Step 1: Write the failing tests** — append to `internal/brain/action_test.go` (add `"bytes"` to imports):

```go
func TestEffectiveRisk(t *testing.T) {
	tests := []struct {
		name string
		a    Action
		want RiskLevel
	}{
		{"default edit when unset", Action{Action: ActionDispatch, Dispatches: []Dispatch{{Thread: "claude", Message: "x"}}}, RiskEdit},
		{"explicit read", Action{Action: ActionDispatch, Dispatches: []Dispatch{{Thread: "claude", Message: "x", Risk: RiskRead}}}, RiskRead},
		{"max across dispatches", Action{Action: ActionParallelDispatch, Dispatches: []Dispatch{{Thread: "claude", Message: "a", Risk: RiskRead}, {Thread: "codex", Message: "b", Risk: RiskShip}}}, RiskShip},
		{"auto pipeline is ship", Action{Action: ActionPipeline, Pipeline: "auto"}, RiskShip},
		{"research pipeline defaults edit", Action{Action: ActionPipeline, Pipeline: "research"}, RiskEdit},
		{"action-level ship", Action{Action: ActionHandoff, Risk: RiskShip}, RiskShip},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.EffectiveRisk(); got != tt.want {
				t.Errorf("EffectiveRisk() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestActionValidRisk(t *testing.T) {
	good := Action{Action: ActionDispatch, Confidence: 0.7, Dispatches: []Dispatch{{Thread: "claude", Message: "x", Risk: RiskShip}}}
	if !good.Valid() {
		t.Error("valid risk rejected")
	}
	bad := Action{Action: ActionDispatch, Confidence: 0.7, Dispatches: []Dispatch{{Thread: "claude", Message: "x", Risk: "nuke"}}}
	if bad.Valid() {
		t.Error("invalid risk accepted")
	}
}

func TestRiskInSchema(t *testing.T) {
	if !bytes.Contains(ActionSchema, []byte(`"risk"`)) {
		t.Error("ActionSchema missing risk property")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/brain/ -run 'TestEffectiveRisk|TestActionValidRisk|TestRiskInSchema' -v`
Expected: FAIL — `undefined: RiskLevel`, `EffectiveRisk`

- [ ] **Step 3: Implement risk in `internal/brain/action.go`**

Add the type and constants near `ActionType`:

```go
// RiskLevel is the coarse risk class of an action. The brain proposes it; the
// REPL enforces it. Default (empty) is treated as edit — never silently ship.
type RiskLevel string

const (
	RiskRead RiskLevel = "read" // no writes: research, explain, review, status
	RiskEdit RiskLevel = "edit" // edits files in an interruptible thread (default)
	RiskShip RiskLevel = "ship" // commit/push/PR/deploy — confirm first
)
```

Add `Risk` to both `Dispatch` and `Action`:

```go
type Dispatch struct {
	Thread     string    `json:"thread"`
	Model      string    `json:"model,omitempty"`
	Message    string    `json:"message"`
	CLIOptions []string  `json:"cli_options,omitempty"`
	Rationale  string    `json:"rationale,omitempty"`
	Risk       RiskLevel `json:"risk,omitempty"`
}

type Action struct {
	Action     ActionType `json:"action"`
	Dispatches []Dispatch `json:"dispatches,omitempty"`
	Pipeline   string     `json:"pipeline,omitempty"`
	Reply      string     `json:"reply,omitempty"`
	Remember   string     `json:"remember,omitempty"`
	Risk       RiskLevel  `json:"risk,omitempty"`
	Confidence float64    `json:"confidence"`
}
```

Add helpers and validation:

```go
func validRisk(r RiskLevel) bool {
	switch r {
	case "", RiskRead, RiskEdit, RiskShip:
		return true
	default:
		return false
	}
}

func riskRank(r RiskLevel) int {
	switch r {
	case RiskRead:
		return 1
	case RiskEdit:
		return 2
	case RiskShip:
		return 3
	default:
		return 0
	}
}

// EffectiveRisk returns the highest risk class implied by the action,
// defaulting to edit. The `auto` pipeline can commit/push/PR, so it is always
// ship-class regardless of what the model claimed.
func (a Action) EffectiveRisk() RiskLevel {
	r := a.Risk
	for _, d := range a.Dispatches {
		if riskRank(d.Risk) > riskRank(r) {
			r = d.Risk
		}
	}
	if a.Action == ActionPipeline && a.Pipeline == "auto" && riskRank(RiskShip) > riskRank(r) {
		r = RiskShip
	}
	if r == "" {
		return RiskEdit
	}
	return r
}
```

In `Valid()`, reject unknown risk values. Add at the top of the method:

```go
	if !validRisk(a.Risk) {
		return false
	}
```

and inside the dispatch loop, extend the guard:

```go
		for _, d := range a.Dispatches {
			if !validThreads[d.Thread] || d.Message == "" || !validRisk(d.Risk) {
				return false
			}
		}
```

In `ActionSchema`, add a `risk` enum to the dispatch item properties and to the
top-level properties (both: `"risk": {"type": "string", "enum": ["read", "edit", "ship", ""]}`).

- [ ] **Step 4: Teach the brain to set risk** — in `internal/brain/prompt.go`, insert into `systemPreamble` just before the `Capability cards:` line:

```
Risk: set "risk" on each dispatch (and on pipeline/handoff actions) to "read" (no writes — research, explain, review, status), "edit" (modifies files; the default), or "ship" (commits, pushes, opens PRs, deploys). When unsure, choose "edit". styx confirms with the user before any "ship" action, so never use "ship" to mean "important".
```

- [ ] **Step 5: Read-class permission drop (adapter)** — add `ReadOnly bool` to `agent.DispatchSpec`, thread it through `Manager.Dispatch` → runner, and in the claude arg builder (Task 11) append the write-permission flag conditionally. Extract the builder if needed so it is unit-testable:

```go
// claudeArgs builds the headless claude invocation. Read-only turns omit the
// pre-granted write permission.
func claudeArgs(sessionID, model, msg string, extra []string, readOnly bool) []string {
	args := []string{ /* --resume sessionID, --model model, extra, ... as in Task 11 */ }
	args = append(args, "-p", msg, "--output-format", "stream-json", "--verbose")
	if !readOnly {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}
```

Append a test to `internal/agent/adapter_test.go`:

```go
func TestClaudeArgsReadOnlyDropsSkipPermissions(t *testing.T) {
	rw := claudeArgs("s", "sonnet", "do it", nil, false)
	if !containsArg(rw, "--dangerously-skip-permissions") {
		t.Error("read-write dispatch should pre-grant permissions")
	}
	ro := claudeArgs("s", "sonnet", "explain", nil, true)
	if containsArg(ro, "--dangerously-skip-permissions") {
		t.Error("read-only dispatch must NOT pre-grant permissions")
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: Enforce in the REPL** — in `cmd/styx/repl.go`, add a field to `replSession`:

```go
	assumeYes bool // --yes / non-interactive: skip ship-risk confirmations
```

Gate at the very top of `execute`:

```go
	if act.EffectiveRisk() == brain.RiskShip && !s.confirmRisk(act) {
		s.println("◆ cancelled — ship-risk action declined")
		s.pushRecent(utterance, "(cancelled: ship-risk)")
		return nil
	}
```

Set `ReadOnly` when dispatching (in `runOneDispatch`, the `mgr.Dispatch` call):

```go
		Extra: d.CLIOptions, ReadOnly: d.Risk == brain.RiskRead,
```

Add the helpers:

```go
func (s *replSession) confirmRisk(act brain.Action) bool {
	if s.assumeYes {
		return true
	}
	s.print(fmt.Sprintf("⚠ this will %s — proceed? [y/N]: ", riskSummary(act)))
	line, err := s.in.ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func riskSummary(act brain.Action) string {
	if act.Action == brain.ActionPipeline {
		return "run the " + act.Pipeline + " pipeline (may commit/push/open a PR)"
	}
	return "perform a ship-risk action (commit/push/deploy)"
}
```

- [ ] **Step 7: Write the REPL gate tests** — append to `cmd/styx/repl_test.go`:

```go
func TestShipRiskConfirmationDeclined(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionPipeline, Pipeline: "auto", Confidence: 0.9}}}
	s, out := newTestSession(t, b, "n\n")
	ranAuto := false
	s.pipelines = map[string]func(ctx context.Context, arg string) error{
		"auto": func(ctx context.Context, _ string) error { ranAuto = true; return nil },
	}
	if err := s.turn(context.Background(), "ship the rate limiting feature"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if ranAuto {
		t.Error("auto pipeline ran despite declined ship-risk confirmation")
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("expected cancellation notice:\n%s", out.String())
	}
}

func TestShipRiskAutoApproved(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionPipeline, Pipeline: "auto", Confidence: 0.9}}}
	s, _ := newTestSession(t, b, "")
	s.assumeYes = true
	ranAuto := false
	s.pipelines = map[string]func(ctx context.Context, arg string) error{
		"auto": func(ctx context.Context, _ string) error { ranAuto = true; return nil },
	}
	if err := s.turn(context.Background(), "ship it"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !ranAuto {
		t.Error("auto pipeline did not run under assumeYes")
	}
}
```

- [ ] **Step 8: Run and commit**

Run: `go build ./... && go vet ./... && go test ./internal/brain/ ./internal/agent/ ./cmd/styx/`
Expected: PASS

```bash
git add internal/brain/ internal/agent/ cmd/styx/repl.go cmd/styx/repl_test.go
git commit -m "feat(safety): risk tiers on actions; REPL confirms ship-risk, read drops write perms"
```

### Task 19.2: memory provenance & decay

Add `project`, `scope`, and `confidence` to memory items, and weight recall by
confidence × recency so a one-off routing correction fades instead of
overfitting routing forever.

**Files:**
- Modify: `internal/memory/store.go`
- Modify: `internal/memory/recall.go`
- Modify: `cmd/styx/repl.go` (`saveMemoryText`, `renderHits`)
- Test: `internal/memory/store_test.go` (append), `internal/memory/recall_test.go` (append), `cmd/styx/repl_test.go` (append)

- [ ] **Step 1: Write the failing tests** — append to `internal/memory/store_test.go`:

```go
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
```

and to `internal/memory/recall_test.go` (add `"math"` and `"time"` to imports):

```go
func TestDecayedScore(t *testing.T) {
	base := decayedScore(1.0, 1.0, 0)
	older := decayedScore(1.0, 1.0, recallHalfLife)
	lowConf := decayedScore(1.0, 0.5, 0)
	if base <= older {
		t.Errorf("older memory not decayed: base=%v older=%v", base, older)
	}
	if math.Abs(older-0.5) > 1e-9 {
		t.Errorf("half-life decay = %v, want ~0.5", older)
	}
	if math.Abs(lowConf-0.5) > 1e-9 {
		t.Errorf("confidence weighting = %v, want 0.5", lowConf)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/memory/ -run 'TestStoreProvenance|TestDecayedScore' -v`
Expected: FAIL — unknown `Item` fields / `undefined: decayedScore`

- [ ] **Step 3: Implement provenance in `internal/memory/store.go`**

Extend `Item`:

```go
type Item struct {
	ID         int64
	Kind       Kind
	Text       string
	Source     string
	Project    string    // owning project ("" = global)
	Scope      string    // optional applicability hint, e.g. "reviews" or "general"
	Confidence float64   // 0..1; explicit facts high, one-off corrections low
	Embedding  []float32
	CreatedAt  time.Time
	LastUsedAt time.Time // bumped on recall; reserved for future eviction
}
```

Update `schema` to the new columns (fresh DBs):

```go
const schema = `
CREATE TABLE IF NOT EXISTS memory (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,
    text         TEXT    NOT NULL,
    source       TEXT    NOT NULL DEFAULT '',
    project      TEXT    NOT NULL DEFAULT '',
    scope        TEXT    NOT NULL DEFAULT '',
    confidence   REAL    NOT NULL DEFAULT 1,
    embedding    BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
);
`
```

Add a migration for DBs created by Task 3's earlier schema (mirrors the budget
DB's model-column migration) and call it from `Open` after the schema exec:

```go
// migrate adds provenance columns to memory DBs created before they existed.
func migrate(db *sql.DB) error {
	want := map[string]string{
		"project":      "TEXT NOT NULL DEFAULT ''",
		"scope":        "TEXT NOT NULL DEFAULT ''",
		"confidence":   "REAL NOT NULL DEFAULT 1",
		"last_used_at": "INTEGER NOT NULL DEFAULT 0",
	}
	rows, err := db.Query(`PRAGMA table_info(memory)`)
	if err != nil {
		return fmt.Errorf("inspect memory schema: %w", err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan memory schema: %w", err)
		}
		have[name] = true
	}
	rows.Close()
	for name, def := range want {
		if !have[name] {
			if _, err := db.Exec("ALTER TABLE memory ADD COLUMN " + name + " " + def); err != nil {
				return fmt.Errorf("add memory column %s: %w", name, err)
			}
		}
	}
	return nil
}
```

Update `Add` (default confidence to 1, insert the new columns) and `All`
(select and scan them). `Add`:

```go
func (s *Store) Add(ctx context.Context, it Item) (int64, error) {
	if it.Confidence == 0 {
		it.Confidence = 1
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO memory (kind, text, source, project, scope, confidence, embedding, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(it.Kind), it.Text, it.Source, it.Project, it.Scope, it.Confidence,
		encodeVec(it.Embedding), time.Now().Unix(), 0)
	if err != nil {
		return 0, fmt.Errorf("insert memory item: %w", err)
	}
	return res.LastInsertId()
}
```

`All` selects `id, kind, text, source, project, scope, confidence, embedding, created_at, last_used_at`
and scans the new fields (decode `last_used_at` via `time.Unix`, treating 0 as zero time).

- [ ] **Step 4: Implement decay in `internal/memory/recall.go`** (add `"time"` to imports):

```go
// recallHalfLife is how fast an item's recall weight halves with age — old
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
```

In `Recall`, build each hit with the decayed score:

```go
		for _, it := range items {
			cos := cosine(qv, it.Embedding)
			score := decayedScore(cos, it.Confidence, time.Since(it.CreatedAt))
			hits = append(hits, Hit{Item: it, Score: score})
		}
```

(Task 5's `TestRecallTopKAcrossStores` still passes: items are added with
default confidence 1 and ~0 age, so decay ≈ 1 and the ranking is unchanged.)

- [ ] **Step 5: Populate provenance at write sites** — in `cmd/styx/repl.go`, rewrite `saveMemoryText` and `renderHits`:

```go
func (s *replSession) saveMemoryText(ctx context.Context, text string) error {
	kind := memory.KindFact
	scope := "general"
	confidence := 1.0
	if rest, ok := strings.CutPrefix(text, "routing-preference:"); ok {
		kind = memory.KindRoutingPreference
		// One-off corrections start low and decay; the brain only leans on them
		// when they recur. An optional "scope: <x>" hint narrows them.
		confidence = 0.6
		scope = parseScope(rest)
	}
	vec, err := s.emb.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed memory: %w", err)
	}
	if _, err := s.mem.Add(ctx, memory.Item{
		Kind: kind, Text: text, Source: "repl",
		Project: s.proj.Name, Scope: scope, Confidence: confidence, Embedding: vec,
	}); err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	s.println("◆ remembered")
	return nil
}

// parseScope pulls an optional "scope: <tag>" hint out of a routing preference
// ("...scope: reviews"); defaults to "general".
func parseScope(s string) string {
	i := strings.Index(s, "scope:")
	if i < 0 {
		return "general"
	}
	tag := strings.TrimSpace(s[i+len("scope:"):])
	if j := strings.IndexAny(tag, ".\n;"); j >= 0 {
		tag = tag[:j]
	}
	if tag = strings.TrimSpace(tag); tag == "" {
		return "general"
	}
	return tag
}

func renderHits(hits []memory.Hit) []string {
	var out []string
	for _, h := range hits {
		meta := string(h.Item.Kind)
		if h.Item.Scope != "" && h.Item.Scope != "general" {
			meta += "; scope " + h.Item.Scope
		}
		if h.Item.Confidence > 0 && h.Item.Confidence < 1 {
			meta += fmt.Sprintf("; conf %.1f", h.Item.Confidence)
		}
		out = append(out, fmt.Sprintf("[%s] %s", meta, h.Item.Text))
	}
	return out
}
```

Also set provenance on the **distillation** write (Task 13): `Project: proj.Name,
Scope: "thread", Confidence: 0.8`, and on session-end summaries (Task 19):
`Confidence: 0.9`. (Three-field additions; no structural change.)

- [ ] **Step 6: Test the write path** — append to `cmd/styx/repl_test.go`:

```go
func TestRoutingPreferenceIsLowConfidenceAndScoped(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionRemember,
		Remember: "routing-preference: codex handles algorithm reviews; scope: reviews", Confidence: 1}}}
	s, _ := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "no, codex should do reviews"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	items, err := s.mem.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	it := items[0]
	if it.Kind != memory.KindRoutingPreference {
		t.Errorf("kind = %s", it.Kind)
	}
	if it.Confidence >= 1 {
		t.Errorf("routing-pref confidence = %v, want < 1", it.Confidence)
	}
	if it.Scope != "reviews" {
		t.Errorf("scope = %q, want reviews", it.Scope)
	}
}
```

- [ ] **Step 7: Run and commit**

Run: `go build ./... && go vet ./... && go test ./internal/memory/ ./cmd/styx/`
Expected: PASS (including Task 3/5 tests — provenance is additive)

```bash
git add internal/memory/ cmd/styx/repl.go cmd/styx/repl_test.go
git commit -m "feat(memory): provenance (project/scope/confidence) + confidence·recency decay"
```

### Task 19.3: per-session audit trail + /audit

An append-only JSONL log per session records every brain decision, dispatch,
pipeline run, memory write, and risk prompt, so the user can always answer
"what did you do?" — important now that the REPL can commit/push/PR.

**Files:**
- Create: `internal/audit/log.go`, `internal/audit/log_test.go`
- Modify: `internal/paths/paths.go` (`AuditDir`), `internal/paths/paths_test.go`
- Modify: `cmd/styx/repl.go` (wire logger, `/audit`), `cmd/styx/help.go`
- Test: `cmd/styx/repl_test.go` (append)

- [ ] **Step 1: Write the failing tests** — create `internal/audit/log_test.go`:

```go
package audit

import (
	"path/filepath"
	"testing"
)

func TestAppendAndTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 3; i++ {
		if err := l.Append(Record{Kind: KindTurn, Detail: "u"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Append(Record{Kind: KindDecision, Detail: "dispatch"}); err != nil {
		t.Fatal(err)
	}
	recs, err := l.Tail(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("tail = %d, want 2", len(recs))
	}
	if recs[1].Kind != KindDecision || recs[1].Detail != "dispatch" {
		t.Errorf("last record = %+v", recs[1])
	}
	if recs[0].At.IsZero() {
		t.Error("At not stamped")
	}
}
```

and append to `internal/paths/paths_test.go`:

```go
func TestAuditDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	ad, err := AuditDir()
	if err != nil {
		t.Fatalf("AuditDir: %v", err)
	}
	if ad != "/tmp/xdg-test/styx/state/audit" {
		t.Errorf("AuditDir = %q, want /tmp/xdg-test/styx/state/audit", ad)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/audit/ ./internal/paths/ -run 'TestAppendAndTail|TestAuditDir' -v`
Expected: FAIL — package/symbol missing

- [ ] **Step 3: Implement `internal/audit/log.go`**

```go
// Package audit records an append-only, per-session trail of everything styx
// did — brain decisions, dispatches, pipeline runs, memory writes, and risk
// prompts — so the user can always answer "what did you do, and why?".
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Kind labels an audit record.
type Kind string

const (
	KindTurn        Kind = "turn"
	KindDecision    Kind = "decision"
	KindDispatch    Kind = "dispatch"
	KindPipeline    Kind = "pipeline"
	KindMemoryWrite Kind = "memory_write"
	KindRiskPrompt  Kind = "risk_prompt"
	KindError       Kind = "error"
)

// Record is one audited event.
type Record struct {
	At     time.Time         `json:"at"`
	Kind   Kind              `json:"kind"`
	Detail string            `json:"detail"`
	Meta   map[string]string `json:"meta,omitempty"`
}

// Logger appends records to one session's JSONL file.
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// Open opens (creating if needed) the session log at path in append mode.
func Open(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	return &Logger{f: f, path: path}, nil
}

// Append writes one record (stamping the time if unset).
func (l *Logger) Append(r Record) error {
	if r.At.IsZero() {
		r.At = time.Now()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write audit record: %w", err)
	}
	return nil
}

// Tail returns the last n records in chronological order.
func (l *Logger) Tail(n int) ([]Record, error) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var recs []Record
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue // tolerate a torn final line
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan audit log: %w", err)
	}
	if len(recs) > n {
		recs = recs[len(recs)-n:]
	}
	return recs, nil
}

// Close releases the file handle.
func (l *Logger) Close() error { return l.f.Close() }
```

Add `AuditDir` to `internal/paths/paths.go` (next to `MemoryDir`):

```go
// AuditDir returns the directory holding per-project session audit logs.
func AuditDir() (string, error) {
	s, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(s, "audit"), nil
}
```

- [ ] **Step 4: Wire it into the REPL** — in `cmd/styx/repl.go`:

Add a field to `replSession`:

```go
	audit *audit.Logger
```

In `newREPLSession`, after the threads load, open a per-session log and fold it
into `cleanup`:

```go
	auditDir, err := paths.AuditDir()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	projAudit := filepath.Join(auditDir, proj.Name)
	if err := paths.EnsureDir(projAudit); err != nil {
		cleanup()
		return nil, nil, err
	}
	al, err := audit.Open(filepath.Join(projAudit, time.Now().Format("20060102-150405")+".jsonl"))
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	// extend cleanup to also close the audit log
```

Add a helper and emit records along the turn path:

```go
func (s *replSession) auditf(kind audit.Kind, detail string, meta map[string]string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Append(audit.Record{Kind: kind, Detail: detail, Meta: meta})
}
```

- in `turn`: `s.auditf(audit.KindTurn, utterance, nil)` on entry, and after a
  successful decision `s.auditf(audit.KindDecision, string(act.Action), map[string]string{"risk": string(act.EffectiveRisk()), "confidence": fmt.Sprintf("%.2f", act.Confidence)})`.
- in `runOneDispatch`: `s.auditf(audit.KindDispatch, d.Thread+"·"+model, map[string]string{"msg": d.Message})`.
- in the pipeline branch of `execute`: `s.auditf(audit.KindPipeline, act.Pipeline, nil)`.
- in `saveMemoryText`: `s.auditf(audit.KindMemoryWrite, text, nil)` after a successful add.
- in the ship-risk gate (Task 19.1): `s.auditf(audit.KindRiskPrompt, riskSummary(act), map[string]string{"result": "declined"})`.

Add the `/audit` slash command (in `slash`):

```go
	case "/audit":
		recs, err := s.audit.Tail(20)
		if err != nil {
			s.println("(audit unavailable: " + err.Error() + ")")
			return false
		}
		for _, r := range recs {
			s.println(fmt.Sprintf("%s  %-12s %s", r.At.Format("15:04:05"), r.Kind, r.Detail))
		}
		return false
```

Add `/audit` to the banner, the unknown-command hint, and `cmd/styx/help.go`.

- [ ] **Step 5: Wire the test helper** — in `newTestSession` (the Task 18 helper), give the session an audit logger so REPL tests exercise the trail:

```go
	al, _ := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	s.audit = al
	t.Cleanup(func() { al.Close() })
```

Append the test to `cmd/styx/repl_test.go`:

```go
func TestAuditTrail(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionReply, Reply: "hi", Confidence: 0.9}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if quit := s.slash("/audit"); quit {
		t.Fatal("/audit should not quit")
	}
	got := out.String()
	for _, want := range []string{"turn", "decision"} {
		if !strings.Contains(got, want) {
			t.Errorf("/audit output missing %q:\n%s", want, got)
		}
	}
}
```

- [ ] **Step 6: Run and commit**

Run: `go build ./... && go vet ./... && go test ./internal/audit/ ./internal/paths/ ./cmd/styx/`
Expected: PASS

```bash
git add internal/audit/ internal/paths/ cmd/styx/
git commit -m "feat(audit): append-only per-session trail and /audit command"
```

---

### Task 20: E2E scripted REPL session + final verification

**Files:**
- Modify: `cmd/styx/repl_test.go` (append)

- [ ] **Step 1: Write the scripted E2E test** — append to `cmd/styx/repl_test.go`:

```go
// TestScriptedSession drives several turns through one session end-to-end:
// reply -> dispatch (fake CLI) -> remember -> recall influences the next turn.
func TestScriptedSession(t *testing.T) {
	t.Setenv("FAKEAGENT_TEXT", "implemented and tested")
	t.Setenv("FAKEAGENT_SESSION", "sess-e2e")
	b := &scriptedBrain{actions: []brain.Action{
		{Action: brain.ActionReply, Reply: "hello! ready to work", Confidence: 0.95},
		{Action: brain.ActionDispatch, Confidence: 0.9,
			Dispatches: []brain.Dispatch{{Thread: "claude", Model: "sonnet", Message: "add retry logic", Rationale: "implementation"}}},
		{Action: brain.ActionRemember, Remember: "we retry 3 times with backoff", Confidence: 1},
	}}
	s, out := newTestSession(t, b, "")
	ctx := context.Background()

	for _, utterance := range []string{
		"hey styx",
		"add retry logic to the loader",
		"remember: we retry 3 times with backoff",
	} {
		if err := s.turn(ctx, utterance); err != nil {
			t.Fatalf("turn %q: %v", utterance, err)
		}
	}

	got := out.String()
	for _, want := range []string{
		"hello! ready to work",
		"◆ claude·sonnet › implementation",
		"implemented and tested",
		"◆ remembered",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("session output missing %q:\n%s", want, got)
		}
	}

	// The dispatch persisted a durable thread with the CLI's session id.
	th := s.mgr.Threads.Get("claude", "claude")
	if th.SessionID != "sess-e2e" || th.Turns != 1 {
		t.Errorf("thread after session = %+v", th)
	}
	// The remember landed in project memory and is recallable.
	hits, err := memory.Recall(ctx, s.emb, "what's our retry policy?", 1, s.mem)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Item.Text, "retry 3 times") {
		t.Errorf("recall = %+v", hits)
	}
	// /why explains the last decision.
	if !strings.Contains(s.lastActionJSON(), "remember") {
		t.Errorf("lastActionJSON = %s", s.lastActionJSON())
	}
	// /status shows the live thread with a context meter.
	if quit := s.slash("/status"); quit {
		t.Fatal("/status should not quit")
	}
	if !strings.Contains(out.String(), "claude (claude): 1 turns") {
		t.Errorf("status output missing thread line:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run the full suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all PASS

- [ ] **Step 3: Full manual verification checklist** (requires ollama + claude CLI):

```bash
go install ./cmd/styx   # or make install if the Makefile defines it
styx doctor --fix       # everything ✓
styx "hello"            # brain replies inline, no dispatch
styx                    # REPL:
#   styx› what can you do?                 -> reply
#   styx› have claude add a --json flag    -> ◆ claude·sonnet › ... streams
#   styx› /status                          -> claude thread with meter
#   styx› /why                             -> last Action JSON
#   styx› remember that I prefer rebase    -> ◆ remembered
#   styx› /quit
styx budget             # thread dispatches recorded with real token counts
```

- [ ] **Step 4: Commit**

```bash
git add cmd/styx/
git commit -m "test(repl): scripted end-to-end session against fake CLI"
```

---

## Self-review notes (spec coverage)

Checked against `2026-06-12-styx-repl-orchestrator-design.md`:

- REPL + one-shot + verbs kept → Tasks 18–19 (dispatch falls through to brain turn; all existing verbs untouched).
- Persistent agent threads, per-turn `--resume`, per-turn tier switching → Tasks 10–13.
- Lazy start + seed → `Manager.seedMessage` role line (intel context auto-loads via the existing `.claude/context.md` mechanism from `intel.WriteContextMD`).
- Context meter / distill at ~70% (configurable) / capability degrade / crash recovery / permissions pre-granted → Tasks 11–13, config in Task 2.
- Brain: structured output, schema, small context, capability cards, tier policy, budget degradation (fable→opus), escalation to haiku, never-brick ask-the-user, routing corrections as memories → Tasks 6–9, 14, 18.
- Memory: per-project + global SQLite, nomic embeddings, top-k cosine, write paths (distillations Task 13, session-end summaries Task 19, briefs Task 19, corrections Task 18, explicit remember Task 18) → Tasks 3–5.
- Verb mapping: doctor new (Task 17); slash commands (Task 19); build/execute unchanged (absorbed = handoff covers the interactive case; verbs still work).
- Reliability: circuit breaker wired (Task 15), subprocess timeouts (Task 15), silent swallows surfaced (Task 16), real token accounting from stream-json (Tasks 12–14; ollama keeps len/4 estimates per spec).
- Testing strategy: fake agent CLI (Task 12), brain behind interface + scripted fake (Tasks 8, 18), env-gated ollama integration with labeled fixtures (Task 9), deterministic memory tests (Tasks 3–5), scripted E2E (Task 20).
- Safety (Phase 8): coarse risk class on every action (read/edit/ship); ship-class confirmed before running, read-class drops pre-granted write permissions → Task 19.1.
- Trust (Phase 8): memory provenance (project/scope/confidence) + confidence·recency decay so one-off routing corrections fade instead of overfitting → Task 19.2.
- Auditability (Phase 8): append-only per-session JSONL trail + `/audit` ("what did you do?") → Task 19.3.
- Reality gates: dogfood the brain before Phase 4 (Checkpoint A) and the REPL before hardening (Checkpoint B) — the plan is no longer 20 linear tasks with no real-use checkpoint.
- Out of scope respected: no long-lived agent processes, no styx tool loop, no token-level cost optimization, ollama never routes implementation work (encoded in its capability card).

Known deviations (intentional, called out inline):
- codex runs as a plain adapter in v1 (no stream-json/resume); doctor reports its mode. Upgrading it to native `codex exec resume` is a follow-up once probed flags are verified against the installed CLI.
- Interactive handoff ingest is best-effort because claude's interactive `--resume` forks the session (noted in `Manager.Handoff`).
- Fixture set starts at 22 labeled utterances, not 50 — the accuracy test is ratio-based; extend the JSON freely (Checkpoint A makes this mandatory before Phase 4).
- Read-class permission drop (Task 19.1) depends on the claude adapter honoring `DispatchSpec.ReadOnly`; codex/agy keep their existing permission posture until probed (doctor reports mode). The ship-confirmation gate is enforced regardless of adapter.

---

## Decisions-log addendum (2026-06-14)

Added after design-review feedback on this plan. The review pushed to recenter
styx as a general task/policy/tool operating system; we kept the spec's thinner
thesis (**the channels are the agents; styx aims them**) and folded in only the
in-scope safety/trust gaps it correctly surfaced.

**Accepted (now Phase 8 + Checkpoints A/B):**

1. **Dogfood checkpoints.** The plan was 20 linear TDD tasks with no "use it for
   real" gate, yet the brain's real-world routing quality is the project's #1
   risk. Checkpoint A gates Phase 4 on expanded-fixture routing accuracy;
   Checkpoint B dogfoods the REPL before the safety layer is designed.
2. **Coarse risk tiers (read/edit/ship), brain-proposed and REPL-enforced.** The
   REPL makes brain-dispatch the *default* path, widening the autonomy surface
   versus today's explicit `styx auto`/`execute`. Ship-class actions now confirm
   first; read-class drops the pre-granted write permission.
3. **Memory provenance + decay.** Distillations and routing corrections are
   auto-recalled into every turn; without confidence/scope/decay a single wrong
   correction overfits routing permanently.
4. **Per-session audit trail (`/audit`).** styx can commit/push/PR via handoff
   and the auto pipeline; the user must be able to ask "what did you do?".

**Explicitly declined (out of scope for v1 — recorded so they are not
re-litigated):**

- **A first-class `internal/task` state machine** (plans/steps/artifacts/
  verification). The channel CLIs already own planning, execution, and
  verification; styx re-implementing that loop is precisely the trap the spec
  avoids ("styx never grows its own agentic tool loop").
- **A generalized external-tool registry** (calendar/email/browser/etc.). styx
  is a conversational dev-work orchestrator, not a general assistant; "no styx
  tool loop" stands. If the scope ever widens, that is a new spec, not a patch
  to this plan.
- **A standalone task-completion eval harness** beyond routing accuracy. The
  dogfood gates (A/B) are the cheap substitute; per-feature unit tests (e.g. the
  risk gate's) cover the rest at v1 scale.

**Model availability — the "fable" tier (2026-06-12):**

Anthropic suspended **Claude Fable 5 and Mythos 5 worldwide** on 2026-06-12 to
comply with a US government export-control directive (no public access for any
user, including paying enterprises). The original spec/plan named `fable` as the
premium judgment tier; that tier is currently uncallable.

Resolution (applied across this plan): the tier→alias indirection in
`routing.toml` absorbs this without a structural change. `[tiers] fable` now
maps to `opus` (Opus 4.8 is the most capable model currently callable);
`applyBrainDefaults`, the seeded `default_routing.toml`, the brain preamble, and
the claude capability card all prefer `opus` and describe `fable` as suspended.
The `fable_weekly_cap` / `fableHot` degradation is left in place but vestigial
(you cannot run hot on a model you can't call) — kept so the tier is a one-line
restore if Anthropic brings Fable back. Task 17's `doctor` now probes each
distinct tier alias for callability (Step 3b), so a future suspension — or a
restored `fable = "fable"` during an ongoing one — is surfaced automatically
rather than failing mid-dispatch.

**Brain model — qwen3:4b → llama3.2:3b (2026-06-15, found at Checkpoint A):**

The spec/plan named `qwen3:4b` as the brain. At Checkpoint A it proved unusable:
qwen3 is a *reasoning* model and ran ~25s per call on an M-series/16GB machine,
blowing both the request timeout and the spec's sub-second target, and its
reasoning bled into the structured output (mis-slotted dispatch fields). `think:
false` did not suppress it on the installed ollama. First accuracy run: 52%.

Fixes (committed in `fix(brain): use fast non-reasoning model …`): default brain
model is now **`llama3.2:3b`** (a fast non-reasoning instruct model, ~1s/call);
`Ollama.chat` always sends `think: false`; and `systemPreamble` gained few-shot
examples that (a) force `remember` actions to populate the `remember` field —
the single biggest miss bucket — and (b) sharpen thread selection
(ollama/codex/agy vs claude). Re-run: **84/99 = 85%**, clearing the 80% gate.

Lesson for re-execution: the brain must be a *non-reasoning* instruct model.
Some embedded code/test snippets above still show the original `qwen3:4b`; the
as-built default and the `[brain] model`/`applyBrainDefaults`/test defaults are
`llama3.2:3b`. The remaining misses were first assumed to be mostly debatable
labels handled by the haiku-escalation valve — that assumption was **refined and
partly corrected** by the preamble rework below.

**Routing preamble rework — 85% → 96% (2026-06-15, Checkpoint A follow-on):**

Building on the `llama3.2:3b` swap above, `systemPreamble` was reworked (prompt
only — no model, dataset, or code-logic change) and re-validated on the canonical
gate: **95/99 = 96%**, stable across two runs with identical misses (up from
84/99). The baseline's dominant failure was *pipeline-verb leakage* (the 3B
keyword-matched `intel`/`review`/`research` on any "codebase"/"find"/"context"/
"diff" mention). The fix reserves pipeline verbs for the four exact styx ops and
sends repo code-work to claude, plus crisp boundaries for `research` (answers
that live *outside* the repo), `review` (the current diff/changes vs a PR/
design), `handoff`, `remember` (store the fact, don't acknowledge via `reply`),
and `agy` (size routes large-file explains), with a `parallel_dispatch` anchor
example.

Two corrections to the assumption in the entry above:

- The residual misses are **not** mostly debatable labels recovered by escalation.
  The 4 remaining are: 1 structured-output JSON-serialization limit (routing is
  *correct* — `dispatch:codex` — but the 3B emits truncated JSON when the
  utterance contains `()`), 2 genuinely debatable labels (`is this approach
  sound?` / `what's the blast radius?` — reply vs dispatch:claude), and 1 real
  `auto`-vs-dispatch miss. **All four are emitted at confidence 0.8–0.9**, so
  raising `confidence_threshold` would *not* catch them — the haiku valve is for
  *low-confidence* turns, of which the fixture set has none. The real levers for
  the residual are JSON-repair reliability and dataset expansion, not the
  threshold.
- Prompt tuning has hit diminishing returns on a 3B: intermediate variants that
  added more rules/examples regressed (destabilized `parallel_dispatch`/`auto`),
  and one rule that forced analysis-questions to claude cost ~7 points of
  collateral. Further accuracy should come from a bigger brain or more fixtures,
  not more rules — consistent with Checkpoint A's decision-gate options.

The two debatable labels are surfaced for adjudication (not tuned to, since
forcing them regressed other cases). The prompt was iterated with a byte-faithful
promptfoo harness now in `eval/promptfoo/` (dev tool, `npx`, no `go.mod` deps)
that mirrors `brain.go`'s `/api/chat` request and `TestRoutingAccuracy`'s match
logic exactly and generates its tests from the same `utterances.json`; the Go
test stays canonical. Full writeup + miss buckets: `eval/promptfoo/RESULTS.md`.
