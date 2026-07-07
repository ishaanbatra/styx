# Styx Async Dispatch (B1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** A conductor can fire a dispatch with `background: true`, keep talking, and pick the result up later via `collect` — with piggybacked status on every conductor tool result, a global concurrency cap, per-thread and per-project-write ordering, honest orphan reporting after crashes, and an append-only `outcomes` table that every dispatch completion (sync or background) feeds.

**Architecture:** All conductor-side changes live in `cmd/styx/` (package main), matching the existing conductor layout: a new in-memory `taskRegistry` in `cmd/styx/mcp_tasks.go` with a JSON state mirror under `~/.config/styx/state/tasks/`, plus handler changes in `cmd/styx/mcp_conductor.go`. The `outcomes` table lives in the existing budget sqlite (`internal/budget`). No daemons: background goroutines derive from a cancellable root context created in `cmdMCP` and die when `styx mcp` dies. Spec: `docs/superpowers/specs/2026-07-07-styx-async-dispatch-design.md` (Decisions and Non-goals are settled — do not redesign).

**Tech Stack:** Go stdlib only (no new deps), `modernc.org/sqlite` via the existing budget package, `testdata/fakeagent` bash fixture, `e2e/` JSON-RPC harness.

## Global Constraints

- Pure Go, no cgo, no provider SDKs — channels shell out to CLIs or speak local HTTP.
- Never swallow errors: wrap with context (`fmt.Errorf("record outcome: %w", err)`). The one sanctioned soften: outcome/budget *recording* failures are narrated via `logStatus` and never fail a completed dispatch (existing convention at `mcp_conductor.go`'s ollama branch).
- In `styx mcp`, stdout carries JSON-RPC exclusively. Background tasks write nothing to stdout mid-flight; narration goes to stderr via `logStatus`.
- All file writes atomic (tmp + rename).
- Never break `styx auto --resume` (no pipeline state changes in this plan — keep it that way).
- No daemons: no detached processes, background work dies with the `styx mcp` process. Every subprocess keeps its timeout/interruptible context (`Manager.Timeout` already enforces this inside `Runner`).
- `risk=ship` never runs in background — rejected at spawn.
- **Drift contract:** every task that changes behavior described in `docs/ARCHITECTURE.md` updates that doc **in the same commit** and bumps its `last_verified` date (currently `2026-07-07` in its frontmatter → set to the commit date). The commit steps below name the sections to edit. No verb changes in this plan, so README's verb table is untouched; README is only touched if it states the MCP tool count.
- Before every commit: `go vet ./... && gofmt -w .` and `make test` (full suite), not just the new tests.
- Hermetic tests only. No live AI calls anywhere in this plan except the single optional `STYX_E2E_LIVE=1` run in post-plan verification.

## Pinned plan-time decisions (spec left these open)

1. **Shared completion bookkeeping:** the budget event stays inside `agent.Manager.record` (both paths call `Manager.Dispatch`). What gets extracted is the conductor-level tail — outcome-row append + result-map shaping — into `conductorDeps.finishDispatch`, used by the sync handler and background goroutines alike.
2. **`background: true` with `cli: ollama` is rejected** (loud error). Ollama one-shots are local and fast; queue semantics are thread-scoped. Keeps the registry free of the no-thread branch.
3. **Sync dispatch to a busy thread errors loudly** ("thread busy with t3 — collect it or use another thread"): a sync turn interleaved with a background turn on the same session would corrupt resume state. Same guard for a sync edit-risk dispatch against a running background edit-risk task on the same project.
4. **Orphan semantics:** at server start, any unclaimed state file (queued/running/done/error/orphaned) from a previous server lifetime becomes an `orphaned` registry entry with id `o1`, `o2`, … Results are never persisted, so a finished-but-uncollected task is also a (loud) loss. Collecting an orphan claims its file; claimed files older than 7 days are pruned at startup.
5. **Cap knob:** `[conductor] max_background_tasks = 4` in routing.toml, defaulted by `applyConductorDefaults`, seeded in `default_routing.go`, upgraded into existing configs by `EnsureConductorTaskCap` following `EnsureFableTier`'s exact pattern in `internal/config/upgrade.go` + `UpgradeRoutingFile`.
6. **Routing signals on outcome rows** come from `signals.Extract("dispatch", []string{message}, config.Project{})`, comma-joined. Rating is stored as TEXT `''|'good'|'bad'` (Go-friendlier than NULL).

---

### Task 1: `outcomes` table + record/rate/read API in internal/budget

The shared substrate with the self-improvement spec. Append-only except the single sanctioned mutation: stamping a rating.

**Files:**
- Create: `internal/budget/outcome.go`
- Create: `internal/budget/outcome_test.go`
- Modify: `internal/budget/budget.go` (execute the outcomes schema in `New`)

**Interfaces:**
- Consumes: `Tracker.db` (same file, package-private).
- Produces (later tasks and plan 2 rely on these exact names):
  - `type Outcome struct { ID int64; CreatedAt time.Time; Project, Thread, TaskID, CLI, Model, Signals, Risk string; DurationS float64; TokensIn, TokensOut int; ErrorKind string; Background bool; Rating, Note string }`
  - `func (t *Tracker) RecordOutcome(ctx context.Context, o Outcome) error`
  - `func (t *Tracker) RateOutcome(ctx context.Context, ref string, ok bool, note string) (int64, error)` — most-recent-matching semantics.
  - `func (t *Tracker) OutcomesSince(ctx context.Context, since time.Time) ([]Outcome, error)` — newest first.

- [x] **Step 1: Write the failing tests**

Create `internal/budget/outcome_test.go`:

```go
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
		{Thread: "codex", CLI: "codex"},          // most recent thread=codex
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
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/budget/ -run 'TestRecordAndReadOutcomes|TestRateOutcome' -v`
Expected: FAIL — `undefined: Outcome`, `tr.RecordOutcome undefined`

- [x] **Step 3: Implement**

Create `internal/budget/outcome.go`:

```go
package budget

import (
	"context"
	"fmt"
	"time"
)

// Outcome is one dispatch completion record — the learning substrate shared
// with the self-improvement digest (styx learn). Append-only; the single
// sanctioned mutation is RateOutcome stamping rating+note.
type Outcome struct {
	ID        int64
	CreatedAt time.Time
	Project   string // stable project ID ("" = none)
	Thread    string // agent thread name ("" for one-shots)
	TaskID    string // background task id ("" for sync dispatches)
	CLI       string // claude | codex | agy | ollama
	Model     string
	Signals   string // comma-joined routing signals from signals.Extract
	Risk      string // read | edit | ship
	DurationS float64
	TokensIn  int
	TokensOut int
	ErrorKind string // "" on success, else classified: timeout|429|5xx|other
	Background bool
	Rating    string // "" (unrated) | "good" | "bad"
	Note      string
}

const outcomesSchema = `
CREATE TABLE IF NOT EXISTS outcomes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER NOT NULL,
    project     TEXT    NOT NULL DEFAULT '',
    thread      TEXT    NOT NULL DEFAULT '',
    task_id     TEXT    NOT NULL DEFAULT '',
    cli         TEXT    NOT NULL,
    model       TEXT    NOT NULL DEFAULT '',
    signals     TEXT    NOT NULL DEFAULT '',
    risk        TEXT    NOT NULL DEFAULT '',
    duration_s  REAL    NOT NULL DEFAULT 0,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    error_kind  TEXT    NOT NULL DEFAULT '',
    background  INTEGER NOT NULL DEFAULT 0,
    rating      TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS outcomes_ts ON outcomes (ts DESC);
`

// RecordOutcome appends one dispatch-completion row.
func (t *Tracker) RecordOutcome(ctx context.Context, o Outcome) error {
	bg := 0
	if o.Background {
		bg = 1
	}
	_, err := t.db.ExecContext(ctx,
		`INSERT INTO outcomes (ts, project, thread, task_id, cli, model, signals, risk,
		                       duration_s, tokens_in, tokens_out, error_kind, background, rating, note)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), o.Project, o.Thread, o.TaskID, o.CLI, o.Model, o.Signals, o.Risk,
		o.DurationS, o.TokensIn, o.TokensOut, o.ErrorKind, bg, o.Rating, o.Note)
	if err != nil {
		return fmt.Errorf("record outcome: %w", err)
	}
	return nil
}

// RateOutcome stamps a rating onto the MOST RECENT outcome row whose task_id
// or thread matches ref — the one sanctioned mutation of the outcomes table.
// Returns the rated row's id; no match is a loud error.
func (t *Tracker) RateOutcome(ctx context.Context, ref string, ok bool, note string) (int64, error) {
	rating := "bad"
	if ok {
		rating = "good"
	}
	var id int64
	err := t.db.QueryRowContext(ctx,
		`SELECT id FROM outcomes WHERE task_id = ?1 OR thread = ?1 ORDER BY id DESC LIMIT 1`,
		ref).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("rate outcome: no outcome matches thread or task %q", ref)
	}
	if _, err := t.db.ExecContext(ctx,
		`UPDATE outcomes SET rating = ?, note = ? WHERE id = ?`, rating, note, id); err != nil {
		return 0, fmt.Errorf("rate outcome %d: %w", id, err)
	}
	return id, nil
}

// OutcomesSince returns outcome rows recorded at or after since, newest first.
func (t *Tracker) OutcomesSince(ctx context.Context, since time.Time) ([]Outcome, error) {
	rows, err := t.db.QueryContext(ctx,
		`SELECT id, ts, project, thread, task_id, cli, model, signals, risk,
		        duration_s, tokens_in, tokens_out, error_kind, background, rating, note
		 FROM outcomes WHERE ts >= ? ORDER BY id DESC`, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("read outcomes: %w", err)
	}
	defer rows.Close()
	var out []Outcome
	for rows.Next() {
		var o Outcome
		var ts int64
		var bg int
		if err := rows.Scan(&o.ID, &ts, &o.Project, &o.Thread, &o.TaskID, &o.CLI, &o.Model,
			&o.Signals, &o.Risk, &o.DurationS, &o.TokensIn, &o.TokensOut, &o.ErrorKind,
			&bg, &o.Rating, &o.Note); err != nil {
			return nil, fmt.Errorf("scan outcome: %w", err)
		}
		o.CreatedAt = time.Unix(ts, 0)
		o.Background = bg == 1
		out = append(out, o)
	}
	return out, rows.Err()
}
```

In `internal/budget/budget.go`, in `New`, right after the existing `schema` exec (before the v0.3 ALTER migration):

```go
	if _, err := db.ExecContext(context.Background(), outcomesSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply outcomes schema: %w", err)
	}
```

Note: `time.Time{}.Unix()` is a large negative number, so `OutcomesSince(ctx, time.Time{})` returns everything — the tests rely on that.

- [x] **Step 4: Run tests**

Run: `go test ./internal/budget/ -v` → PASS (all, including existing tracker tests — the new schema is additive).

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Budget (internal/budget)" section: add a paragraph — append-only `outcomes` table (schema fields), `RecordOutcome`/`RateOutcome` (most-recent-matching, the sanctioned mutation)/`OutcomesSince`, created idempotently in `New`. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(budget): append-only outcomes table with record/rate/read API (B1 substrate)"
```

---

### Task 2: sync dispatch completions append outcome rows (shared `finishDispatch`)

Every dispatch completion — ollama one-shot or thread — appends one outcome row. The thread branch's post-dispatch tail is extracted into `finishDispatch`, the function background completions will share in Task 7.

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (dispatch handler; new helpers `finishDispatch`, `dispatchSignals`, `outcomeErrKind`)
- Test: `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Consumes: `budget.Outcome`/`RecordOutcome`/`OutcomesSince` (Task 1), `signals.Extract(verb string, args []string, proj config.Project) []string`, `channel.ClassifiedError{Kind channel.ErrorKindLabel}`.
- Produces (Task 7 reuses these exact signatures):
  - `type dispatchMeta struct { ProjectID, Thread, CLI, Model, Risk, Signals, TaskID string; Background bool; Start time.Time }`
  - `func (d *conductorDeps) finishDispatch(ctx context.Context, meta dispatchMeta, res agent.TurnResult, dispatchErr error) (map[string]any, error)`
  - `func dispatchSignals(message string) string`
  - `func outcomeErrKind(err error) string`

- [x] **Step 1: Write the failing tests**

Add to `cmd/styx/mcp_conductor_test.go` (the file already imports `budget`, `channel`, `config`, `shipgate`, `os`, `filepath`, `strings`; add `"time"` if absent):

```go
func TestDispatchThreadAppendsOutcomeRow(t *testing.T) {
	// Same scaffolding as TestDispatchHappyPath: fakeagent as `claude` on PATH.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "done")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	d := &conductorDeps{
		a: &app{
			routing: config.Routing{
				Brain: config.BrainConfig{Model: "haiku", ContextThresholdPct: 70},
				Tiers: map[string]string{"haiku": "haiku"},
			},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate:     shipgate.New(shipgate.ModeOff),
		emb:      replEmbedder{},
		managers: map[string]*managed{},
	}

	if _, err := callTool(t, d, "dispatch", map[string]any{
		"project": "proj1", "cli": "claude",
		"message": "refactor the loader architecture", "risk": "edit",
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	rows, err := bud.OutcomesSince(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("read outcomes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 outcome row, got %d", len(rows))
	}
	o := rows[0]
	if o.CLI != "claude" || o.Thread != "claude" || o.Risk != "edit" || o.Background {
		t.Fatalf("outcome row mismatch: %+v", o)
	}
	if o.ErrorKind != "" {
		t.Fatalf("success must record empty error kind, got %q", o.ErrorKind)
	}
	if !strings.Contains(o.Signals, "complex") {
		t.Fatalf("signals must be extracted from the message (refactor => complex), got %q", o.Signals)
	}
	if o.TokensIn == 0 || o.DurationS < 0 {
		t.Fatalf("tokens/duration must be recorded: %+v", o)
	}
}

func TestDispatchOllamaAppendsOutcomeRow(t *testing.T) {
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	cap := &captureChannel{}
	d := &conductorDeps{
		gate: shipgate.New(shipgate.ModeOff),
		a: &app{
			channels: map[string]channel.Channel{"ollama": cap},
			tracker:  bud,
			routing:  config.Routing{Brain: config.BrainConfig{Model: "qwen2.5-coder:7b"}},
		},
	}
	if _, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "ollama", "message": "say pong", "risk": "read",
	}); err != nil {
		t.Fatalf("ollama dispatch: %v", err)
	}
	rows, err := bud.OutcomesSince(context.Background(), time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("want 1 outcome row, got %d (%v)", len(rows), err)
	}
	if rows[0].CLI != "ollama" || rows[0].Thread != "" || rows[0].Model != "qwen2.5-coder:7b" {
		t.Fatalf("ollama outcome mismatch: %+v", rows[0])
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run 'TestDispatchThreadAppendsOutcomeRow|TestDispatchOllamaAppendsOutcomeRow' -v`
Expected: FAIL — `want 1 outcome row, got 0` (both).

- [x] **Step 3: Implement**

In `cmd/styx/mcp_conductor.go`, add three helpers (near `registeredProjectNames`); add `"errors"` and the `signals` import (`github.com/ishaanbatra/styx/internal/signals`) to the import block:

```go
// dispatchSignals tags the dispatch message with routing signals for the
// outcome row (comma-joined). The conductor picks the cli explicitly, so the
// signals are recorded for learning, not for routing.
func dispatchSignals(message string) string {
	return strings.Join(signals.Extract("dispatch", []string{message}, config.Project{}), ",")
}

// outcomeErrKind classifies a dispatch error for the outcome row: the
// channel's classified kind when available, else "other". "" on success.
func outcomeErrKind(err error) string {
	if err == nil {
		return ""
	}
	var ce *channel.ClassifiedError
	if errors.As(err, &ce) {
		return string(ce.Kind)
	}
	return "other"
}

// dispatchMeta carries what finishDispatch needs to append an outcome row
// and shape the result map — the post-dispatch bookkeeping shared by the
// synchronous handler and background task completions.
type dispatchMeta struct {
	ProjectID  string
	Thread     string
	CLI        string
	Model      string
	Risk       string
	Signals    string
	TaskID     string // "" for sync dispatches
	Background bool
	Start      time.Time
}

// finishDispatch appends the outcome row (success and failure alike; record
// errors are narrated, never fail the dispatch — budget events are already
// recorded inside Manager.Dispatch) and shapes the dispatch result map.
func (d *conductorDeps) finishDispatch(ctx context.Context, meta dispatchMeta, res agent.TurnResult, dispatchErr error) (map[string]any, error) {
	durS := math.Round(time.Since(meta.Start).Seconds()*10) / 10
	if rerr := d.a.tracker.RecordOutcome(ctx, budget.Outcome{
		Project: meta.ProjectID, Thread: meta.Thread, TaskID: meta.TaskID,
		CLI: meta.CLI, Model: meta.Model, Signals: meta.Signals, Risk: meta.Risk,
		DurationS: durS, TokensIn: res.InputTokens, TokensOut: res.OutputTokens,
		ErrorKind: outcomeErrKind(dispatchErr), Background: meta.Background,
	}); rerr != nil {
		logStatus("outcome record (%s) failed: %v", meta.CLI, rerr)
	}
	if dispatchErr != nil {
		return nil, fmt.Errorf("dispatch %s: %w", meta.CLI, dispatchErr)
	}
	return map[string]any{
		"thread": meta.Thread, "cli": meta.CLI, "text": res.Text,
		"tokens_in": res.InputTokens, "tokens_out": res.OutputTokens,
		"model": meta.Model, "duration_s": durS,
	}, nil
}
```

Rewire the dispatch handler's **thread branch**. Replace everything from `m, err := d.managerFor(in.Project)` through the final `return map[string]any{...}, nil` with:

```go
				m, err := d.managerFor(in.Project)
				if err != nil {
					return nil, err
				}
				model := in.Model
				if resolved, ok := d.a.routing.Tiers[model]; ok {
					model = resolved
				}
				thread := in.Thread
				if thread == "" {
					thread = in.CLI
				}
				meta := dispatchMeta{
					ProjectID: m.mgr.ProjectID, Thread: thread, CLI: in.CLI,
					Model: model, Risk: in.Risk, Signals: dispatchSignals(in.Message),
					Start: start,
				}
				notify, hasNotify := mcpserver.ProgressFn(ctx)
				var events int
				onEvent := func(ev agent.Event) {
					events++
					var msg string
					switch ev.Type {
					case agent.EventInit:
						msg = in.CLI + ": session started"
					case agent.EventText:
						if events%5 != 0 { // throttle streaming chatter
							return
						}
						msg = fmt.Sprintf("%s: streaming (%d events)", in.CLI, events)
					case agent.EventResult:
						msg = in.CLI + ": finishing"
					}
					if msg == "" {
						return
					}
					logStatus("dispatch %s", msg)
					if hasNotify {
						notify(float64(events), msg)
					}
				}
				res, err := m.mgr.Dispatch(ctx, agent.DispatchSpec{
					Thread: in.Thread, CLI: in.CLI, Model: model,
					Message: in.Message, ExtraRoots: in.ExtraRoots,
					ReadOnly: in.Risk == "read",
				}, onEvent)
				return d.finishDispatch(ctx, meta, res, err)
```

(Behavior is unchanged for consumers: the result map keys and the `dispatch %s: %w` error wrap are byte-identical to today's inline versions.)

In the **ollama branch**, right before `if err != nil { return nil, fmt.Errorf("ollama dispatch: %w", err) }`, add the outcome append (the ollama branch keeps its own result shape — no thread key):

```go
					if rerr := d.a.tracker.RecordOutcome(ctx, budget.Outcome{
						CLI: "ollama", Model: model, Signals: dispatchSignals(in.Message),
						Risk: in.Risk, DurationS: math.Round(time.Since(start).Seconds()*10) / 10,
						TokensIn: resp.EstTokensIn, TokensOut: resp.EstTokensOut,
						ErrorKind: outcomeErrKind(err),
					}); rerr != nil {
						logStatus("outcome record (ollama one-shot) failed: %v", rerr)
					}
```

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestDispatch -v` → PASS (new tests plus all existing dispatch tests — the happy-path/gate/validation tests must be untouched by the refactor).

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Conductor MCP tools" `dispatch` bullet: every dispatch completion appends an outcome row (both branches; signals extracted from the message; record failures narrated, never fatal); name `finishDispatch` as the shared sync/background completion function. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(mcp): every dispatch completion appends an outcomes row via shared finishDispatch"
```

---

### Task 3: `rate_dispatch` tool

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (append the tool to `conductorTools`)
- Modify: `cmd/styx/mcp.go` (`cmdMCP` readiness logStatus names the new tool)
- Modify: `e2e/e2e_test.go` (tools/list count 11 → 12 in `TestFirstContact`)
- Test: `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Consumes: `Tracker.RateOutcome` (Task 1).
- Produces: MCP tool `rate_dispatch({thread_or_task, ok, note?}) → {rated, outcome_id, target}`.

- [x] **Step 1: Write the failing test**

Add to `cmd/styx/mcp_conductor_test.go`:

```go
func TestRateDispatchStampsMostRecentOutcome(t *testing.T) {
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	ctx := context.Background()
	for _, o := range []budget.Outcome{
		{Thread: "codex", CLI: "codex"},
		{Thread: "codex", CLI: "codex"},
	} {
		if err := bud.RecordOutcome(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff), a: &app{tracker: bud}}

	res, err := callTool(t, d, "rate_dispatch", map[string]any{
		"thread_or_task": "codex", "ok": true, "note": "clean implement",
	})
	if err != nil {
		t.Fatalf("rate_dispatch: %v", err)
	}
	if res["rated"] != true {
		t.Fatalf("want rated=true, got %v", res)
	}
	rows, _ := bud.OutcomesSince(ctx, time.Time{})
	if rows[0].Rating != "good" || rows[0].Note != "clean implement" {
		t.Fatalf("most recent codex outcome must carry the rating: %+v", rows[0])
	}
	if rows[1].Rating != "" {
		t.Fatalf("older outcome must stay unrated: %+v", rows[1])
	}

	// Unknown ref is a loud error; missing arg too.
	if _, err := callTool(t, d, "rate_dispatch", map[string]any{"thread_or_task": "ghost", "ok": false}); err == nil {
		t.Fatal("unknown thread/task must error")
	}
	if _, err := callTool(t, d, "rate_dispatch", map[string]any{"ok": true}); err == nil {
		t.Fatal("missing thread_or_task must error")
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestRateDispatch -v`
Expected: FAIL — `tool "rate_dispatch" not registered`

- [x] **Step 3: Implement**

Append to the `[]mcpserver.Tool` slice returned by `conductorTools` (after `pipeline_run`):

```go
		{
			Name: "rate_dispatch",
			Description: "Rate a recent dispatch outcome as notably good or bad (feeds styx learn). " +
				"Rate only notable outcomes — not every dispatch.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"thread_or_task": map[string]any{"type": "string", "description": "thread name or background task id; the most recent matching outcome is rated"},
				"ok":             map[string]any{"type": "boolean", "description": "true = notably good, false = notably bad"},
				"note":           map[string]any{"type": "string", "description": "one line on why (optional)"},
			}, "required": []string{"thread_or_task", "ok"}},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in struct {
					ThreadOrTask string `json:"thread_or_task"`
					OK           bool   `json:"ok"`
					Note         string `json:"note"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("rate_dispatch args: %w", err)
				}
				if in.ThreadOrTask == "" {
					return nil, fmt.Errorf("thread_or_task is required")
				}
				id, err := d.a.tracker.RateOutcome(ctx, in.ThreadOrTask, in.OK, in.Note)
				if err != nil {
					return nil, err
				}
				return map[string]any{"rated": true, "outcome_id": id, "target": in.ThreadOrTask}, nil
			},
		},
```

In `cmd/styx/mcp.go`, update `cmdMCP`'s readiness logStatus to name it:

```go
	logStatus("mcp server ready on stdio (route, budget_status, record_usage, channel_health, get_intel, refresh_intel, recall, dispatch, thread_status, memory_save, pipeline_run, rate_dispatch)")
```

In `e2e/e2e_test.go` `TestFirstContact`, update the tool count:

```go
	if len(tools) != 12 {
		t.Fatalf("want 12 tools, got %d", len(tools))
	}
```

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestRateDispatch -v` → PASS
Run: `make e2e` → PASS (tool count now 12)

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md`: "MCP server" tool enumeration ("eleven tools" → twelve, add `rate_dispatch` to the list) and a `rate_dispatch` bullet under "Conductor MCP tools" (most-recent-matching semantics, rate-only-notable guidance). Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test && make e2e
git add -A
git commit -m "feat(mcp): rate_dispatch tool stamps ratings onto the most recent matching outcome"
```

---

### Task 4: `[conductor] max_background_tasks` knob + seeded default + upgrade path

**Files:**
- Modify: `internal/config/routing.go` (`Conductor` struct + `applyConductorDefaults`)
- Modify: `cmd/styx/default_routing.go` (`[conductor]` block)
- Modify: `internal/config/upgrade.go` (`EnsureConductorTaskCap`, wire into `UpgradeRoutingFile`)
- Modify: `cmd/styx/dispatch.go` (`ensureFirstRun` logs the new migration result)
- Test: `internal/config/upgrade_test.go`, `internal/config/routing_test.go`

**Interfaces:**
- Produces: `config.Conductor.MaxBackgroundTasks int` (default 4); `EnsureConductorTaskCap(content string) (string, bool)`; `UpgradeRoutingFile` returns `(geminiN int, implementInjected, fableRestored, taskCapInjected bool, err error)`.

- [x] **Step 1: Write the failing tests**

Add to `internal/config/upgrade_test.go`:

```go
func TestEnsureConductorTaskCap(t *testing.T) {
	// Seeded-shape config: knob injected inside the existing [conductor] section.
	seeded := "[tiers]\nfable  = \"fable\"\n\n[conductor]\nship_gate = \"handshake\"\n"
	got, changed := EnsureConductorTaskCap(seeded)
	if !changed || !strings.Contains(got, "max_background_tasks = 4") {
		t.Fatalf("must inject the cap knob, got changed=%v:\n%s", changed, got)
	}
	if strings.Index(got, "[conductor]") > strings.Index(got, "max_background_tasks") {
		t.Fatalf("knob must land inside the [conductor] section:\n%s", got)
	}
	// Idempotent.
	again, changed2 := EnsureConductorTaskCap(got)
	if changed2 || again != got {
		t.Fatal("second run must be a no-op")
	}
	// User customization is respected.
	custom := "[conductor]\nship_gate = \"off\"\nmax_background_tasks = 2\n"
	if _, changed3 := EnsureConductorTaskCap(custom); changed3 {
		t.Fatal("a config already carrying the knob must be left alone")
	}
	// Config with no [conductor] section at all: whole section appended.
	bare := "[tiers]\nfable  = \"fable\"\n"
	got4, changed4 := EnsureConductorTaskCap(bare)
	if !changed4 || !strings.Contains(got4, "[conductor]") || !strings.Contains(got4, "max_background_tasks = 4") {
		t.Fatalf("missing section must be appended whole:\n%s", got4)
	}
}
```

Add to `internal/config/routing_test.go` (mirror the file's existing default-assertion style):

```go
func TestConductorTaskCapDefault(t *testing.T) {
	var r Routing
	applyConductorDefaults(&r)
	if r.Conductor.MaxBackgroundTasks != 4 {
		t.Errorf("default max_background_tasks = %d, want 4", r.Conductor.MaxBackgroundTasks)
	}
	r2 := Routing{Conductor: Conductor{MaxBackgroundTasks: 2}}
	applyConductorDefaults(&r2)
	if r2.Conductor.MaxBackgroundTasks != 2 {
		t.Errorf("explicit knob must be preserved, got %d", r2.Conductor.MaxBackgroundTasks)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestEnsureConductorTaskCap|TestConductorTaskCapDefault' -v`
Expected: FAIL — `undefined: EnsureConductorTaskCap`; `r.Conductor.MaxBackgroundTasks undefined`

- [x] **Step 3: Implement**

`internal/config/routing.go` — extend the struct and defaults:

```go
// Conductor configures the frontier-brain launcher + MCP toolbelt.
type Conductor struct {
	ShipGate           string `toml:"ship_gate"`            // handshake | tty | off
	MaxBackgroundTasks int    `toml:"max_background_tasks"` // concurrent background dispatch cap (task registry)
}
```

```go
// applyConductorDefaults fills zero-valued conductor settings so configs written
// before this section existed keep working.
func applyConductorDefaults(r *Routing) {
	if r.Conductor.ShipGate == "" {
		r.Conductor.ShipGate = "handshake"
	}
	if r.Conductor.MaxBackgroundTasks == 0 {
		r.Conductor.MaxBackgroundTasks = 4
	}
}
```

`cmd/styx/default_routing.go` — extend the `[conductor]` block:

```toml
# ── Conductor (frontier-brain launcher + MCP toolbelt) ──
[conductor]
# ship confirmation for dispatch(risk=ship) / pipeline_run auto:
# handshake (token relay, default) | tty (prompt on /dev/tty) | off
ship_gate = "handshake"
# max concurrent background dispatches; over-cap tasks queue (collect shows position)
max_background_tasks = 4
```

`internal/config/upgrade.go` — new migration, modeled on `EnsureFableTier`:

```go
// conductorTaskCapKnob is the seeded max_background_tasks knob, injected into
// pre-B1 configs by EnsureConductorTaskCap. Kept identical in spirit to the
// block in cmd/styx/default_routing.go so seeded and upgraded configs agree.
const conductorTaskCapKnob = `# max concurrent background dispatches; over-cap tasks queue (collect shows position)
max_background_tasks = 4`

// EnsureConductorTaskCap injects the [conductor] max_background_tasks knob
// (B1) when absent. A config already carrying the key — any value — is left
// alone. If the [conductor] section exists the knob lands at its end; with no
// section at all, a whole seeded section is appended. Returns the new content
// and whether a rewrite happened.
func EnsureConductorTaskCap(content string) (string, bool) {
	if strings.Contains(content, "max_background_tasks") {
		return content, false
	}
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "[conductor]" {
			continue
		}
		// Find the section end: next section header or EOF.
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if len(t) > 0 && t[0] == '[' {
				end = j
				break
			}
		}
		// Trim trailing blank lines inside the section so the knob sits with it.
		insert := end
		for insert > i+1 && strings.TrimSpace(lines[insert-1]) == "" {
			insert--
		}
		out := append(append(append([]string{}, lines[:insert]...), conductorTaskCapKnob), lines[insert:]...)
		return strings.Join(out, "\n"), true
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n\n# ── Conductor (frontier-brain launcher + MCP toolbelt) ──\n[conductor]\nship_gate = \"handshake\"\n" + conductorTaskCapKnob + "\n", true
}
```

Wire into `UpgradeRoutingFile` alongside `EnsureFableTier` (extend signature and doc comment):

```go
func UpgradeRoutingFile(routingPath string) (geminiN int, implementInjected, fableRestored, taskCapInjected bool, err error) {
	b, err := os.ReadFile(routingPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, false, false, nil
		}
		return 0, false, false, false, fmt.Errorf("read routing: %w", err)
	}
	newContent, n := RewriteRoutingGeminiToAgy(string(b))
	newContent, injected := EnsureImplementRules(newContent)
	newContent, fable := EnsureFableTier(newContent)
	newContent, taskCap := EnsureConductorTaskCap(newContent)
	if newContent == string(b) {
		return 0, false, false, false, nil
	}
	backup := filepath.Join(filepath.Dir(routingPath), "routing.v0.1.toml.bak")
	if err := os.WriteFile(backup, b, 0o644); err != nil {
		return 0, false, false, false, fmt.Errorf("write backup %s: %w", backup, err)
	}
	tmp := routingPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(newContent), 0o644); err != nil {
		return 0, false, false, false, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, routingPath); err != nil {
		return 0, false, false, false, fmt.Errorf("atomic rename: %w", err)
	}
	return n, injected, fable, taskCap, nil
}
```

`cmd/styx/dispatch.go` `ensureFirstRun` — update the call site:

```go
	if n, injected, fableRestored, taskCapInjected, err := config.UpgradeRoutingFile(routingPath); err != nil {
		logStatus("upgrade check failed: %v", err)
	} else {
		if n > 0 {
			logStatus("auto-upgraded %d gemini reference(s) to agy (backup at routing.v0.1.toml.bak)", n)
		}
		if injected {
			logStatus("auto-upgraded routing.toml with the implement verb (codex implements, claude fallback)")
		}
		if fableRestored {
			logStatus("auto-upgraded routing.toml: restored the fable tier (suspension lifted)")
		}
		if taskCapInjected {
			logStatus("auto-upgraded routing.toml: seeded [conductor] max_background_tasks = 4")
		}
	}
```

Fix any other `UpgradeRoutingFile` call sites the compiler reports (e.g. tests): `grep -rn "UpgradeRoutingFile(" --include="*.go"`.

- [x] **Step 4: Run tests**

Run: `go test ./internal/config/ -v` → PASS (all — including the existing `UpgradeRoutingFile` tests, updated for the new return).

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Routing" section: extend the `Conductor` sentence with `max_background_tasks` (default 4, seeded, `EnsureConductorTaskCap` upgrade path). Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(config): [conductor] max_background_tasks knob with seeded default + upgrade path"
```

---

### Task 5: task registry core (cap, ordering rules, collect/claim)

The in-memory heart of B1: mutex-guarded registry, monotonic `t<n>` ids, global cap, per-thread serialization, per-project write queue. Persistence is Task 6 (the `persistLocked` hook is a no-op until then).

**Files:**
- Create: `cmd/styx/mcp_tasks.go`
- Create: `cmd/styx/mcp_tasks_test.go`

**Interfaces:**
- Consumes: `pipeline.NewRunID` (existing).
- Produces (Tasks 6-9 rely on these exact names):
  - Task states: `taskQueued`, `taskRunning`, `taskDone`, `taskError`, `taskOrphaned` (string consts `"queued"` etc.).
  - `type taskSpec struct { Project, ProjectID, Thread, CLI, Model, Risk string }`
  - `type bgTask struct { ID, RunID string; Spec taskSpec; State, QueuedBehind string; Created, Started, Finished time.Time; Result map[string]any; Err string; Claimed bool; run func(context.Context, string) (map[string]any, error) }`
  - `func newTaskRegistry(rootCtx context.Context, limit int) *taskRegistry`
  - `func (r *taskRegistry) Spawn(spec taskSpec, run func(ctx context.Context, id string) (map[string]any, error)) (id, state string)` — the run func receives the assigned task id (Task 7's completion bookkeeping stamps it onto the outcome row).
  - `func (r *taskRegistry) Get(id string) (bgTask, bool)` — returns a copy.
  - `func (r *taskRegistry) Claim(id string)`
  - `func (r *taskRegistry) Busy(projectID, thread, risk string) (string, bool)` — nil-safe.
  - `func (r *taskRegistry) Snapshot() []bgTask` — copies, creation order; nil-safe (returns nil).
  - `func (r *taskRegistry) StatusLine() string` — the piggyback line; nil-safe (returns "").

- [x] **Step 1: Write the failing tests**

Create `cmd/styx/mcp_tasks_test.go`:

```go
package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// blockingRun returns a run func that reports it started and blocks until
// released — the test controls task lifetimes without sleeps.
func blockingRun(result map[string]any) (run func(context.Context, string) (map[string]any, error), started chan struct{}, release chan struct{}) {
	started = make(chan struct{})
	release = make(chan struct{})
	run = func(context.Context, string) (map[string]any, error) {
		close(started)
		<-release
		return result, nil
	}
	return run, started, release
}

// waitFor polls cond up to 2s — registry completions happen on goroutines.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func state(r *taskRegistry, id string) string {
	tk, _ := r.Get(id)
	return tk.State
}

func TestRegistryCapQueuesExcessTasks(t *testing.T) {
	r := newTaskRegistry(context.Background(), 1)
	run1, started1, release1 := blockingRun(map[string]any{"text": "one"})
	run2, started2, release2 := blockingRun(map[string]any{"text": "two"})

	id1, st1 := r.Spawn(taskSpec{ProjectID: "p1", Thread: "a", CLI: "codex", Risk: "read"}, run1)
	if st1 != taskRunning {
		t.Fatalf("first task must run immediately, got %q", st1)
	}
	<-started1
	id2, st2 := r.Spawn(taskSpec{ProjectID: "p2", Thread: "b", CLI: "codex", Risk: "read"}, run2)
	if st2 != taskQueued {
		t.Fatalf("over-cap task must queue, got %q", st2)
	}
	close(release1)
	waitFor(t, "t1 done", func() bool { return state(r, id1) == taskDone })
	waitFor(t, "t2 promoted", func() bool { return state(r, id2) == taskRunning })
	<-started2
	close(release2)
	waitFor(t, "t2 done", func() bool { return state(r, id2) == taskDone })
	tk, _ := r.Get(id2)
	if tk.Result["text"] != "two" {
		t.Fatalf("result must be captured, got %v", tk.Result)
	}
}

func TestRegistryThreadSerialization(t *testing.T) {
	r := newTaskRegistry(context.Background(), 4)
	run1, started1, release1 := blockingRun(nil)
	run2, _, release2 := blockingRun(nil)
	id1, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"}, run1)
	<-started1
	id2, st2 := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"}, run2)
	if st2 != taskQueued {
		t.Fatalf("same project+thread must serialize, got %q", st2)
	}
	if tk, _ := r.Get(id2); tk.QueuedBehind != id1 {
		t.Fatalf("queued task must name its blocker, got %q", tk.QueuedBehind)
	}
	// A different thread on the same project (read risk) runs freely.
	run3, started3, release3 := blockingRun(nil)
	_, st3 := r.Spawn(taskSpec{ProjectID: "p1", Thread: "other", CLI: "claude", Risk: "read"}, run3)
	if st3 != taskRunning {
		t.Fatalf("read-risk tasks on distinct threads must run in parallel, got %q", st3)
	}
	<-started3
	close(release1)
	waitFor(t, "t2 promoted", func() bool { return state(r, id2) == taskRunning })
	close(release2)
	close(release3)
}

func TestRegistryProjectWriteQueue(t *testing.T) {
	r := newTaskRegistry(context.Background(), 4)
	run1, started1, release1 := blockingRun(nil)
	id1, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "edit"}, run1)
	<-started1
	// Second edit-risk task on the same project queues behind the first,
	// even on a different thread.
	run2, _, release2 := blockingRun(nil)
	id2, st2 := r.Spawn(taskSpec{ProjectID: "p1", Thread: "claude", CLI: "claude", Risk: "edit"}, run2)
	if st2 != taskQueued {
		t.Fatalf("second edit-risk task on one project must queue, got %q", st2)
	}
	if tk, _ := r.Get(id2); tk.QueuedBehind != id1 {
		t.Fatalf("write-queued task must show queued behind %s, got %q", id1, tk.QueuedBehind)
	}
	// Edit-risk on a DIFFERENT project runs.
	run3, started3, release3 := blockingRun(nil)
	_, st3 := r.Spawn(taskSpec{ProjectID: "p2", Thread: "codex", CLI: "codex", Risk: "edit"}, run3)
	if st3 != taskRunning {
		t.Fatalf("edit-risk on another project must run, got %q", st3)
	}
	<-started3
	close(release1)
	waitFor(t, "write queue drains", func() bool { return state(r, id2) == taskRunning })
	close(release2)
	close(release3)
}

func TestRegistryErrorsCollectAndClaim(t *testing.T) {
	r := newTaskRegistry(context.Background(), 4)
	id, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"},
		func(context.Context, string) (map[string]any, error) {
			return nil, context.DeadlineExceeded
		})
	waitFor(t, "error state", func() bool { return state(r, id) == taskError })
	tk, ok := r.Get(id)
	if !ok || tk.Err == "" {
		t.Fatalf("failed task must carry its error text, got %+v", tk)
	}
	if line := r.StatusLine(); !strings.Contains(line, id) || !strings.Contains(line, "unclaimed") {
		t.Fatalf("unclaimed error must appear in the status line, got %q", line)
	}
	r.Claim(id)
	if line := r.StatusLine(); line != "" {
		t.Fatalf("claimed task must leave the status line, got %q", line)
	}
}

func TestRegistryBusyAndNilSafety(t *testing.T) {
	var nilReg *taskRegistry
	if _, busy := nilReg.Busy("p1", "codex", "edit"); busy {
		t.Fatal("nil registry must report not busy")
	}
	if nilReg.StatusLine() != "" || nilReg.Snapshot() != nil {
		t.Fatal("nil registry reads must be safe no-ops")
	}

	r := newTaskRegistry(context.Background(), 4)
	run1, started1, release1 := blockingRun(nil)
	id1, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "edit"}, run1)
	<-started1
	if blocker, busy := r.Busy("p1", "codex", "read"); !busy || blocker != id1 {
		t.Fatalf("same thread must be busy, got %q %v", blocker, busy)
	}
	if blocker, busy := r.Busy("p1", "other", "edit"); !busy || blocker != id1 {
		t.Fatalf("edit against a running edit on the project must be busy, got %q %v", blocker, busy)
	}
	if _, busy := r.Busy("p1", "other", "read"); busy {
		t.Fatal("read on another thread must not be busy")
	}
	if _, busy := r.Busy("p2", "codex", "edit"); busy {
		t.Fatal("another project must not be busy")
	}
	close(release1)
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run TestRegistry -v`
Expected: FAIL — `undefined: newTaskRegistry` (compile error)

- [x] **Step 3: Implement**

Create `cmd/styx/mcp_tasks.go`:

```go
package main

// Background task registry for conductor dispatches (B1, spec
// docs/superpowers/specs/2026-07-07-styx-async-dispatch-design.md).
// In-memory, mutex-guarded; goroutines derive from the MCP server's root
// context so background work lives and dies with the `styx mcp` process —
// no daemons. State files under ~/.config/styx/state/tasks/ mirror each
// task for crash honesty (orphan reporting), never for resumption.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/pipeline"
)

// Task states. queued|running are "live"; done|error|orphaned are terminal
// and stay visible until claimed via collect.
const (
	taskQueued   = "queued"
	taskRunning  = "running"
	taskDone     = "done"
	taskError    = "error"
	taskOrphaned = "orphaned"
)

// taskSpec is what the registry needs about a dispatch for ordering rules
// and status rendering.
type taskSpec struct {
	Project   string // registry alias as passed ("" = launch-dir project)
	ProjectID string // resolved stable project id (ordering key)
	Thread    string // resolved thread name (cli-defaulted)
	CLI       string
	Model     string
	Risk      string // read | edit (ship is rejected at spawn)
}

// bgTask is one background dispatch. Fields are guarded by the registry
// mutex; Get/Snapshot hand out copies.
type bgTask struct {
	ID           string // t1, t2, … monotonic within a server lifetime (o<n> for adopted orphans)
	RunID        string // collision-free id used for the state file name
	Spec         taskSpec
	State        string
	QueuedBehind string // blocking task id ("" = waiting on capacity, or not queued)
	Created      time.Time
	Started      time.Time
	Finished     time.Time
	Result       map[string]any
	Err          string
	Claimed      bool

	run func(context.Context, string) (map[string]any, error)
}

// taskRegistry owns every background task of one `styx mcp` process.
type taskRegistry struct {
	mu      sync.Mutex
	limit   int
	seq     int
	rootCtx context.Context
	tasks   map[string]*bgTask
	order   []string // creation order, stable listing
	dir     string   // state-file mirror dir; "" disables persistence (unit tests)
}

// newTaskRegistry builds a registry. Goroutines derive from rootCtx — the
// server's root context, NOT any tool call's — so tasks survive the spawning
// call returning and die when the server dies.
func newTaskRegistry(rootCtx context.Context, limit int) *taskRegistry {
	if limit <= 0 {
		limit = 4
	}
	return &taskRegistry{limit: limit, rootCtx: rootCtx, tasks: map[string]*bgTask{}}
}

// Spawn registers a task and starts it immediately when the cap and ordering
// rules allow, else leaves it queued. Returns the task id and initial state.
// The run func receives the assigned id so completion bookkeeping (outcome
// rows) can reference the task.
func (r *taskRegistry) Spawn(spec taskSpec, run func(context.Context, string) (map[string]any, error)) (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("t%d", r.seq)
	t := &bgTask{
		ID:      id,
		RunID:   pipeline.NewRunID("task-" + id + "-" + spec.CLI),
		Spec:    spec,
		State:   taskQueued,
		Created: time.Now(),
		run:     run,
	}
	r.tasks[id] = t
	r.order = append(r.order, id)
	r.startEligibleLocked()
	r.persistLocked(t)
	return id, t.State
}

// startEligibleLocked promotes queued tasks in creation order while capacity
// and the ordering rules allow. Callers hold r.mu.
func (r *taskRegistry) startEligibleLocked() {
	running := 0
	for _, id := range r.order {
		if r.tasks[id].State == taskRunning {
			running++
		}
	}
	for _, id := range r.order {
		t := r.tasks[id]
		if t.State != taskQueued {
			continue
		}
		if blocker := r.conflictLocked(t); blocker != "" {
			t.QueuedBehind = blocker
			continue
		}
		if running >= r.limit {
			t.QueuedBehind = "" // waiting on capacity, not on a specific task
			continue
		}
		t.State = taskRunning
		t.QueuedBehind = ""
		t.Started = time.Now()
		running++
		logStatus("task %s started (%s, thread %s)", t.ID, t.Spec.CLI, t.Spec.Thread)
		go r.execute(t)
	}
}

// conflictLocked returns the id of the running task that blocks t, or "".
// Rule 1 — per-thread serialization: same project+thread never runs
// concurrently (session resume is stateful). Rule 2 — per-project write
// queue: an edit-risk task waits for any running edit-risk task on the same
// project; read-risk tasks run freely in parallel.
func (r *taskRegistry) conflictLocked(t *bgTask) string {
	for _, id := range r.order {
		o := r.tasks[id]
		if o.State != taskRunning {
			continue
		}
		if o.Spec.ProjectID == t.Spec.ProjectID && o.Spec.Thread == t.Spec.Thread {
			return o.ID
		}
		if t.Spec.Risk != "read" && o.Spec.Risk != "read" && o.Spec.ProjectID == t.Spec.ProjectID {
			return o.ID
		}
	}
	return ""
}

// execute runs one task to completion on its own goroutine, then promotes
// whatever its completion unblocked.
func (r *taskRegistry) execute(t *bgTask) {
	res, err := t.run(r.rootCtx, t.ID)
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Finished = time.Now()
	if err != nil {
		t.State = taskError
		t.Err = err.Error()
		logStatus("task %s failed: %v", t.ID, err)
	} else {
		t.State = taskDone
		t.Result = res
		logStatus("task %s done (%s) — collect to read it", t.ID, t.Spec.CLI)
	}
	r.persistLocked(t)
	r.startEligibleLocked()
}

// Busy reports the live (queued or running) task that would collide with a
// SYNCHRONOUS dispatch on (projectID, thread, risk) — the same two ordering
// rules, checked so a sync turn never interleaves with background work on a
// stateful session. Nil-safe.
func (r *taskRegistry) Busy(projectID, thread, risk string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.order {
		o := r.tasks[id]
		if o.State != taskRunning && o.State != taskQueued {
			continue
		}
		if o.Spec.ProjectID == projectID && o.Spec.Thread == thread {
			return o.ID, true
		}
		if risk != "read" && o.Spec.Risk != "read" && o.Spec.ProjectID == projectID {
			return o.ID, true
		}
	}
	return "", false
}

// Get returns a copy of the task (safe to read without the lock).
func (r *taskRegistry) Get(id string) (bgTask, bool) {
	if r == nil {
		return bgTask{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok {
		return bgTask{}, false
	}
	return *t, true
}

// Claim marks a finished task collected so it stops resurfacing.
func (r *taskRegistry) Claim(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.Claimed = true
		r.persistLocked(t)
	}
}

// Snapshot returns copies of all tasks in creation order. Nil-safe.
func (r *taskRegistry) Snapshot() []bgTask {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]bgTask, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, *r.tasks[id])
	}
	return out
}

// StatusLine renders the compact piggyback line: every live task and every
// unclaimed finished one. "" when there is nothing to report. Nil-safe.
func (r *taskRegistry) StatusLine() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var parts []string
	for _, id := range r.order {
		t := r.tasks[id]
		switch t.State {
		case taskRunning:
			parts = append(parts, fmt.Sprintf("%s running (%s, %s)", t.ID, t.Spec.CLI, elapsedShort(time.Since(t.Started))))
		case taskQueued:
			if t.QueuedBehind != "" {
				parts = append(parts, fmt.Sprintf("%s queued behind %s (%s)", t.ID, t.QueuedBehind, elapsedShort(time.Since(t.Created))))
			} else {
				parts = append(parts, fmt.Sprintf("%s queued at cap (%s)", t.ID, elapsedShort(time.Since(t.Created))))
			}
		case taskDone, taskError, taskOrphaned:
			if !t.Claimed {
				parts = append(parts, fmt.Sprintf("%s %s unclaimed — call collect", t.ID, t.State))
			}
		}
	}
	return strings.Join(parts, "; ")
}

// elapsedShort renders a duration as 12s / 4m03s / 1h12m for status lines.
func elapsedShort(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// persistLocked mirrors task state to disk. No-op until Task 6 wires the
// mirror dir; callers hold r.mu.
func (r *taskRegistry) persistLocked(t *bgTask) {
	if r.dir == "" {
		return
	}
	// Implemented in Task 6 (writeTaskFile).
	writeTaskFile(r.dir, t)
}
```

For this task only, add a temporary stub so the file compiles (Task 6 replaces it):

```go
// writeTaskFile is implemented in Task 6 (state-file mirror).
func writeTaskFile(dir string, t *bgTask) {}
```

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestRegistry -v -race` → PASS (run with `-race`; the registry is the one concurrent structure in this plan)
Run: `make test` → green

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Conductor MCP tools" section: add a "Background task registry" subsection — states, monotonic ids, cap from `[conductor] max_background_tasks`, both ordering rules verbatim, root-context lifetime rule, claim semantics, `Busy` guard for sync dispatches. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(mcp): background task registry with cap, thread serialization, and project write queue"
```

---

### Task 6: task state files + orphan scan + prune

**Files:**
- Modify: `internal/paths/paths.go` (`TasksDir`)
- Modify: `cmd/styx/mcp_tasks.go` (replace the `writeTaskFile` stub; add `adoptOrphanedTaskFiles`, `(r *taskRegistry) adoptOrphans`)
- Test: `cmd/styx/mcp_tasks_test.go`, `internal/paths/paths_test.go` (only if that file exists — otherwise skip; TasksDir is exercised through the registry tests)

**Interfaces:**
- Produces:
  - `paths.TasksDir() (string, error)` → `<state>/tasks`.
  - `type taskFile struct` (JSON mirror shape below).
  - `func writeTaskFile(dir string, t *bgTask)` — atomic tmp+rename, best-effort (`logStatus` on failure).
  - `func adoptOrphanedTaskFiles(dir string, maxClaimedAge time.Duration) []taskFile` — startup scan: flips unclaimed files to orphaned on disk, prunes old claimed files, returns the orphans.
  - `func (r *taskRegistry) adoptOrphans(files []taskFile)` — inserts them as `o1`, `o2`, … registry entries.

- [x] **Step 1: Write the failing tests**

Add to `cmd/styx/mcp_tasks_test.go` (add `"encoding/json"`, `"os"`, `"path/filepath"` imports):

```go
func TestTaskStateMirrorAndOrphanScan(t *testing.T) {
	dir := t.TempDir()
	r := newTaskRegistry(context.Background(), 4)
	r.dir = dir
	run1, started1, release1 := blockingRun(map[string]any{"text": "hi"})
	id, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "edit"}, run1)
	<-started1

	// Spawn mirrored the running task to disk.
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("want 1 state file after spawn, got %d", len(files))
	}
	var tf taskFile
	b, _ := os.ReadFile(files[0])
	if err := json.Unmarshal(b, &tf); err != nil {
		t.Fatalf("state file must be JSON: %v", err)
	}
	if tf.State != taskRunning || tf.ID != id || tf.CLI != "codex" {
		t.Fatalf("state file mismatch: %+v", tf)
	}

	close(release1)
	waitFor(t, "done mirrored", func() bool {
		b, _ := os.ReadFile(files[0])
		json.Unmarshal(b, &tf)
		return tf.State == taskDone
	})

	// A NEW server adopting this dir treats the unclaimed done file as an
	// orphan (results are in-memory only — losses are loud, never silent).
	orphans := adoptOrphanedTaskFiles(dir, 7*24*time.Hour)
	if len(orphans) != 1 {
		t.Fatalf("want 1 orphan, got %d", len(orphans))
	}
	r2 := newTaskRegistry(context.Background(), 4)
	r2.dir = dir
	r2.adoptOrphans(orphans)
	snap := r2.Snapshot()
	if len(snap) != 1 || snap[0].State != taskOrphaned || snap[0].ID != "o1" {
		t.Fatalf("adopted orphan mismatch: %+v", snap)
	}
	if !strings.Contains(snap[0].Err, "styx mcp exited") {
		t.Fatalf("orphan must explain the loss, got %q", snap[0].Err)
	}
	if line := r2.StatusLine(); !strings.Contains(line, "o1 orphaned") {
		t.Fatalf("orphans must be reported in the status line, got %q", line)
	}

	// The on-disk file was flipped to orphaned; a third scan adopts it again
	// (still unclaimed), and claiming persists.
	if again := adoptOrphanedTaskFiles(dir, 7*24*time.Hour); len(again) != 1 || again[0].State != taskOrphaned {
		t.Fatalf("unclaimed orphan file must keep resurfacing, got %+v", again)
	}
	r2.Claim("o1")
	if left := adoptOrphanedTaskFiles(dir, 7*24*time.Hour); len(left) != 0 {
		t.Fatalf("claimed orphan must not resurface, got %+v", left)
	}
}

func TestOrphanPruneOldClaimedFiles(t *testing.T) {
	dir := t.TempDir()
	old := taskFile{RunID: "run-old", ID: "t9", State: taskDone, Claimed: true,
		Finished: time.Now().Add(-8 * 24 * time.Hour)}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, "run-old.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if orphans := adoptOrphanedTaskFiles(dir, 7*24*time.Hour); len(orphans) != 0 {
		t.Fatalf("claimed files are never orphans, got %+v", orphans)
	}
	if _, err := os.Stat(filepath.Join(dir, "run-old.json")); !os.IsNotExist(err) {
		t.Fatal("claimed file older than 7 days must be pruned")
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run 'TestTaskStateMirror|TestOrphanPrune' -v`
Expected: FAIL — `undefined: taskFile`, `undefined: adoptOrphanedTaskFiles`

- [x] **Step 3: Implement**

`internal/paths/paths.go`, after `ThreadsDir`:

```go
// TasksDir returns the directory holding background-task state mirrors
// (crash honesty for the conductor's task registry).
func TasksDir() (string, error) {
	s, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(s, "tasks"), nil
}
```

In `cmd/styx/mcp_tasks.go`, delete the `writeTaskFile` stub and add (new imports: `"encoding/json"`, `"os"`, `"path/filepath"`):

```go
// taskFile is the JSON state mirror of one task. It exists for crash honesty
// — orphan reporting after a dead server — never for resumption: results are
// in-memory only, so an uncollected finish is a reported loss.
type taskFile struct {
	RunID     string    `json:"run_id"`
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Project   string    `json:"project"`
	ProjectID string    `json:"project_id"`
	Thread    string    `json:"thread"`
	CLI       string    `json:"cli"`
	Model     string    `json:"model"`
	Risk      string    `json:"risk"`
	Created   time.Time `json:"created"`
	Started   time.Time `json:"started,omitempty"`
	Finished  time.Time `json:"finished,omitempty"`
	Err       string    `json:"error,omitempty"`
	Claimed   bool      `json:"claimed"`
}

// writeTaskFile mirrors one task to <dir>/<run-id>.json (atomic tmp+rename).
// Best-effort: a mirror failure is narrated, never fails the task.
func writeTaskFile(dir string, t *bgTask) {
	tf := taskFile{
		RunID: t.RunID, ID: t.ID, State: t.State,
		Project: t.Spec.Project, ProjectID: t.Spec.ProjectID, Thread: t.Spec.Thread,
		CLI: t.Spec.CLI, Model: t.Spec.Model, Risk: t.Spec.Risk,
		Created: t.Created, Started: t.Started, Finished: t.Finished,
		Err: t.Err, Claimed: t.Claimed,
	}
	b, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		logStatus("task mirror %s: %v", t.ID, err)
		return
	}
	path := filepath.Join(dir, tf.RunID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logStatus("task mirror %s: %v", t.ID, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logStatus("task mirror %s: %v", t.ID, err)
	}
}

// adoptOrphanedTaskFiles scans dir at server start: every UNCLAIMED file from
// a previous lifetime — whatever its state — is a loss (queued/running died
// with the server; done/error results lived only in that server's memory).
// Each is flipped to orphaned on disk and returned for adoption. Claimed
// files finished more than maxClaimedAge ago are pruned. Best-effort
// throughout: unreadable files are narrated and skipped.
func adoptOrphanedTaskFiles(dir string, maxClaimedAge time.Duration) []taskFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logStatus("task scan: %v", err)
		}
		return nil
	}
	var orphans []taskFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			logStatus("task scan %s: %v", e.Name(), err)
			continue
		}
		var tf taskFile
		if err := json.Unmarshal(b, &tf); err != nil {
			logStatus("task scan %s: %v", e.Name(), err)
			continue
		}
		if tf.Claimed {
			if !tf.Finished.IsZero() && time.Since(tf.Finished) > maxClaimedAge {
				if err := os.Remove(path); err != nil {
					logStatus("task prune %s: %v", e.Name(), err)
				}
			}
			continue
		}
		prior := tf.State
		tf.State = taskOrphaned
		if tf.Err == "" || prior == taskQueued || prior == taskRunning || prior == taskDone {
			tf.Err = fmt.Sprintf("lost when styx mcp exited (state was %q); no result — re-dispatch if still needed", prior)
		}
		nb, _ := json.MarshalIndent(tf, "", "  ")
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, nb, 0o644); err == nil {
			_ = os.Rename(tmp, path)
		}
		orphans = append(orphans, tf)
	}
	return orphans
}

// adoptOrphans inserts prior-lifetime orphans as o1, o2, … entries so
// collect and the piggyback line report them. Claiming persists to their
// original run-id file.
func (r *taskRegistry) adoptOrphans(files []taskFile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, tf := range files {
		id := fmt.Sprintf("o%d", i+1)
		t := &bgTask{
			ID: id, RunID: tf.RunID,
			Spec: taskSpec{Project: tf.Project, ProjectID: tf.ProjectID, Thread: tf.Thread,
				CLI: tf.CLI, Model: tf.Model, Risk: tf.Risk},
			State: taskOrphaned, Created: tf.Created, Started: tf.Started,
			Finished: tf.Finished, Err: tf.Err,
		}
		r.tasks[id] = t
		r.order = append(r.order, id)
	}
}
```

Note on the orphan-state edge in the scan: a file already marked `orphaned` (flipped by a previous scan, never collected) keeps its existing `Err` text and keeps resurfacing until claimed — that's the `prior == taskQueued || ...` condition leaving `orphaned` files' Err intact.

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run 'TestTaskStateMirror|TestOrphanPrune|TestRegistry' -v -race` → PASS

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md`: "On-disk layout" gains `~/.config/styx/state/tasks/<run-id>.json  background-task state mirrors (crash honesty)`; the Task 5 registry subsection gains the mirror/orphan/prune paragraph. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(mcp): task state mirrors with startup orphan adoption and claimed-file pruning"
```

---

### Task 7: `dispatch background:true` + spawn-time checks + sync busy guard

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (deps gain the registry; dispatch handler)
- Modify: `cmd/styx/mcp.go` (`cmdMCP`: cancellable root ctx, registry construction, orphan adoption)
- Test: `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Consumes: registry API (Tasks 5-6), `finishDispatch` (Task 2), `paths.TasksDir` (Task 6), `config.Conductor.MaxBackgroundTasks` (Task 4).
- Produces:
  - `conductorDeps.reg *taskRegistry` field; `newConductorDeps(a *app, rootCtx context.Context) *conductorDeps` (signature change — update all call sites).
  - dispatch arg `background bool`; background return `{task_id, thread, cli, status}`.
  - `func (d *conductorDeps) spawnBudgetCheck(ctx context.Context, cli string) error`.

- [x] **Step 1: Write the failing tests**

Add to `cmd/styx/mcp_conductor_test.go`:

```go
func TestDispatchBackgroundRejectsShipAndOllama(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff)}
	_, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "codex", "message": "ship it", "risk": "ship", "background": true,
	})
	if err == nil || !strings.Contains(err.Error(), "background") {
		t.Fatalf("ship-risk background dispatch must be rejected at spawn, got %v", err)
	}
	_, err = callTool(t, d, "dispatch", map[string]any{
		"cli": "ollama", "message": "quick", "risk": "read", "background": true,
	})
	if err == nil || !strings.Contains(err.Error(), "ollama") {
		t.Fatalf("ollama background dispatch must be rejected, got %v", err)
	}
}

func TestDispatchBackgroundSpawnBudgetCheck(t *testing.T) {
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	// Trip the circuit: 3 failures inside the breaker window.
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := bud.Record(ctx, budget.Event{Channel: "codex", Verb: "thread", Success: false, ErrorKind: "5xx"}); err != nil {
			t.Fatal(err)
		}
	}
	d := &conductorDeps{
		gate: shipgate.New(shipgate.ModeOff),
		a:    &app{tracker: bud, routing: config.Routing{}},
		reg:  newTaskRegistry(context.Background(), 4),
	}
	_, err = callTool(t, d, "dispatch", map[string]any{
		"cli": "codex", "message": "long task", "risk": "read", "background": true,
	})
	if err == nil || !strings.Contains(err.Error(), "circuit") {
		t.Fatalf("spawn must fail synchronously on an open circuit, got %v", err)
	}
}

func TestDispatchSyncBusyThreadGuard(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4)
	run1, started1, release1 := blockingRun(nil)
	reg.Spawn(taskSpec{ProjectID: "proj1", Thread: "codex", CLI: "codex", Risk: "edit"}, run1)
	<-started1
	defer close(release1)

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{}, reg: reg,
	}
	_, err = callTool(t, d, "dispatch", map[string]any{
		"project": "proj1", "thread": "codex", "cli": "codex", "message": "hi", "risk": "read",
	})
	if err == nil || !strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), "t1") {
		t.Fatalf("sync dispatch to a thread with a live background task must error naming it, got %v", err)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run 'TestDispatchBackground|TestDispatchSyncBusy' -v`
Expected: FAIL — `unknown field reg` compile error first; after adding the field (Step 3 does), the ship/ollama test fails because `background` is silently ignored and the dispatch proceeds.

- [x] **Step 3: Implement**

`conductorDeps` gains the registry; `newConductorDeps` takes the server root context:

```go
type conductorDeps struct {
	a    *app
	gate *shipgate.Gate
	emb  memory.Embedder
	reg  *taskRegistry // background dispatch registry (nil-safe on read paths)

	mu       sync.Mutex
	managers map[string]*managed
}

// newConductorDeps wires conductorDeps the same way cmdMCP wires the rest of
// the app: real ollama embedder, ship gate + background-task cap from
// routing.toml's [conductor] section, task registry rooted on the server's
// context (background work dies with the server — no daemons).
func newConductorDeps(a *app, rootCtx context.Context) *conductorDeps {
	return &conductorDeps{
		a:        a,
		gate:     shipgate.New(shipgate.Mode(a.routing.Conductor.ShipGate)),
		emb:      memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel),
		reg:      newTaskRegistry(rootCtx, a.routing.Conductor.MaxBackgroundTasks),
		managers: map[string]*managed{},
	}
}
```

Add the spawn-time budget check + cap helper:

```go
// capPctFor returns the routing.toml cap_pct for a channel (0 = no cap).
func capPctFor(r config.Routing, cli string) float64 {
	switch cli {
	case "claude":
		return r.Budget.Claude.CapPct
	case "codex":
		return r.Budget.Codex.CapPct
	case "agy":
		return r.Budget.Agy.CapPct
	}
	return 0
}

// spawnBudgetCheck refuses a background spawn when the target channel is
// circuit-open or over its budget cap — spawn-time failures must be
// synchronous errors, never background losses discovered at collect time.
func (d *conductorDeps) spawnBudgetCheck(ctx context.Context, cli string) error {
	if broken, err := d.a.tracker.ShouldCircuitBreak(ctx, cli, budget.BreakerThreshold, budget.BreakerWindow); err != nil {
		return fmt.Errorf("spawn budget check: %w", err)
	} else if broken {
		return fmt.Errorf("channel %s circuit is open (recent failures) — check channel_health before dispatching", cli)
	}
	pct, err := d.a.tracker.UsedPct(ctx, cli)
	if err != nil {
		return fmt.Errorf("spawn budget check: %w", err)
	}
	if cap := capPctFor(d.a.routing, cli); cap > 0 && pct >= cap {
		return fmt.Errorf("channel %s is over budget (%.0f%% used, cap %.0f%%) — pick another channel or wait", cli, pct, cap)
	}
	return nil
}
```

Dispatch handler changes. `dispatchArgs` gains:

```go
	Background   bool     `json:"background"`
```

Schema gains:

```go
					"background":    map[string]any{"type": "boolean", "description": "true = return a task_id immediately and run in the background; collect fetches the result"},
```

In the handler, right after the risk validation and before the `start := time.Now()` line, add the background spawn-time rejections and budget check (deliberately BEFORE project resolution — spawn-time failures must be synchronous and cheap):

```go
				if in.Background {
					if in.Risk == "ship" {
						return nil, fmt.Errorf("ship-risk dispatch cannot run in background — the confirmation handshake is interactive; dispatch it synchronously")
					}
					if in.CLI == "ollama" {
						return nil, fmt.Errorf("ollama one-shots are synchronous (local and fast) — drop background")
					}
					if d.reg == nil {
						return nil, fmt.Errorf("background dispatch unavailable (no task registry)")
					}
					if err := d.spawnBudgetCheck(ctx, in.CLI); err != nil {
						return nil, err
					}
				}
```

In the **thread branch**, after `meta := dispatchMeta{...}` (Task 2's shape) and before the `notify, hasNotify := ...` line, insert the background fork and then the sync-only busy guard. Order matters: a background dispatch that collides QUEUES (registry ordering rules — never a hard error, per the spec's decision 2); only a synchronous dispatch that collides errors loudly (it cannot queue — the caller is waiting):

```go
				if in.Background {
					spec := agent.DispatchSpec{
						Thread: in.Thread, CLI: in.CLI, Model: model,
						Message: in.Message, ExtraRoots: in.ExtraRoots,
						ReadOnly: in.Risk == "read",
					}
					runFn := func(bctx context.Context, id string) (map[string]any, error) {
						// bctx is the server's root context: the task survives
						// this tool call returning and dies with the server.
						// No progress notifications mid-flight (this call's
						// JSON-RPC exchange is long gone); completion
						// bookkeeping is the same finishDispatch as sync.
						bmeta := meta
						bmeta.Background = true
						bmeta.TaskID = id
						res, derr := m.mgr.Dispatch(bctx, spec, nil)
						return d.finishDispatch(bctx, bmeta, res, derr)
					}
					id, state := d.reg.Spawn(taskSpec{
						Project: in.Project, ProjectID: m.mgr.ProjectID, Thread: thread,
						CLI: in.CLI, Model: model, Risk: in.Risk,
					}, runFn)
					return map[string]any{"task_id": id, "thread": thread, "cli": in.CLI, "status": state}, nil
				}
				if blocker, busy := d.reg.Busy(m.mgr.ProjectID, thread, in.Risk); busy {
					return nil, fmt.Errorf("thread %q is busy with background task %s — collect it, wait, or use another thread", thread, blocker)
				}
```

`cmd/styx/mcp.go` `cmdMCP` — cancellable root context, registry mirror dir, orphan adoption:

```go
// cmdMCP runs styx as an MCP stdio server. stdout carries the JSON-RPC
// protocol; status goes to stderr via logStatus. The server runs until the
// host closes stdin (EOF); cancel then reaps background dispatch goroutines —
// background work lives and dies with this process.
func cmdMCP(a *app, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := newConductorDeps(a, ctx)
	if dir, err := paths.TasksDir(); err != nil {
		logStatus("task state dir unavailable: %v", err)
	} else if err := paths.EnsureDir(dir); err != nil {
		logStatus("task state dir unavailable: %v", err)
	} else {
		d.reg.dir = dir
		if orphans := adoptOrphanedTaskFiles(dir, 7*24*time.Hour); len(orphans) > 0 {
			d.reg.adoptOrphans(orphans)
			logStatus("%d background task(s) from a previous session were lost — collect reports them", len(orphans))
		}
	}
	tools := append(mcpTools(a), conductorTools(d)...)
	srv := mcpserver.New("styx", mcpServerVersion, tools)
	logStatus("mcp server ready on stdio (route, budget_status, record_usage, channel_health, get_intel, refresh_intel, recall, dispatch, thread_status, memory_save, pipeline_run, rate_dispatch)")
	go preloadOllamaModels(a)
	return srv.Serve(ctx, os.Stdin, os.Stdout)
}
```

Fix the other `newConductorDeps(a)` call sites the compiler reports (there is at least the one in `cmdMCP`; check tests): `grep -rn "newConductorDeps(" --include="*.go" cmd/`.

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run 'TestDispatch|TestRegistry' -v -race` → PASS (all existing dispatch tests too — sync behavior is unchanged when `background` is absent and `reg` is nil)
Run: `make test` → green

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Conductor MCP tools" `dispatch` bullet: `background` param, spawn-time work (validation → ship/ollama rejection → budget + circuit check → project/thread resolution → immediate `{task_id, thread, cli, status}`), root-context lifetime, sync busy-thread guard. "MCP server" section: `cmdMCP`'s cancellable root context + startup orphan adoption. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(mcp): dispatch background=true spawns registry tasks with spawn-time checks"
```

---

### Task 8: `collect` tool + task rows in `thread_status`

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (new tool; thread_status handler)
- Modify: `cmd/styx/mcp.go` (readiness logStatus adds `collect`)
- Modify: `e2e/e2e_test.go` (tool count 12 → 13)
- Test: `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Consumes: registry `Get`/`Claim`/`Snapshot` (Tasks 5-6).
- Produces:
  - `collect({task_id})` → finished: full result map + `status: done|error|orphaned` (claims it); unfinished: `{task_id, status, elapsed_s, queued_behind?}`.
  - `collect({})` → `{results: [...], pending: [...]}` — all done-unclaimed results (claimed on return) + one-line summaries of live tasks.
  - `thread_status` result gains `"tasks": []string` (always an array, `[]` when empty).
  - `func taskLine(t bgTask) string` — shared one-line renderer.

- [x] **Step 1: Write the failing tests**

Add to `cmd/styx/mcp_conductor_test.go`:

```go
func TestCollectSingleTaskLifecycle(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4)
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff), reg: reg}
	run1, started1, release1 := blockingRun(map[string]any{"text": "answer", "cli": "codex"})
	id, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"}, run1)
	<-started1

	// Unfinished: status + elapsed, NOT claimed.
	res, err := callTool(t, d, "collect", map[string]any{"task_id": id})
	if err != nil {
		t.Fatalf("collect running: %v", err)
	}
	if res["status"] != "running" {
		t.Fatalf("want running, got %v", res)
	}
	if _, ok := res["elapsed_s"]; !ok {
		t.Fatal("unfinished collect must report elapsed_s")
	}

	close(release1)
	waitFor(t, "done", func() bool { return state(reg, id) == taskDone })

	// Finished: full result, marks claimed.
	res, err = callTool(t, d, "collect", map[string]any{"task_id": id})
	if err != nil {
		t.Fatalf("collect done: %v", err)
	}
	if res["status"] != "done" || res["text"] != "answer" {
		t.Fatalf("collect must return the dispatch result, got %v", res)
	}
	if tk, _ := reg.Get(id); !tk.Claimed {
		t.Fatal("collecting a finished task must claim it")
	}

	// Unknown id: loud error.
	if _, err := callTool(t, d, "collect", map[string]any{"task_id": "t99"}); err == nil {
		t.Fatal("unknown task id must error")
	}
}

func TestCollectAllAndThreadStatusTasks(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4)
	runA, startedA, releaseA := blockingRun(map[string]any{"text": "A"})
	idA, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "a", CLI: "codex", Risk: "read"}, runA)
	<-startedA
	close(releaseA)
	waitFor(t, "A done", func() bool { return state(reg, idA) == taskDone })
	runB, startedB, releaseB := blockingRun(nil)
	idB, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "b", CLI: "claude", Risk: "read"}, runB)
	<-startedB
	defer close(releaseB)

	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff), reg: reg}
	res, err := callTool(t, d, "collect", map[string]any{})
	if err != nil {
		t.Fatalf("collect all: %v", err)
	}
	results, _ := res["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("want 1 finished result, got %v", res["results"])
	}
	first, _ := results[0].(map[string]any)
	if first["task_id"] != idA || first["text"] != "A" {
		t.Fatalf("finished result mismatch: %v", first)
	}
	pending, _ := res["pending"].([]any)
	if len(pending) != 1 || !strings.Contains(pending[0].(string), idB) {
		t.Fatalf("running task must be summarized in pending, got %v", res["pending"])
	}
	if tk, _ := reg.Get(idA); !tk.Claimed {
		t.Fatal("collect all must claim returned results")
	}
	// Second collect: nothing left to return.
	res, _ = callTool(t, d, "collect", map[string]any{})
	if results, _ := res["results"].([]any); len(results) != 0 {
		t.Fatalf("claimed results must not repeat, got %v", res["results"])
	}
}
```

For `thread_status` task rows, extend the tail of the existing `TestDispatchHappyPath` (after its `thread_status` assertions):

```go
	// thread_status carries background task rows (always an array).
	if _, ok := statusRes["tasks"].([]any); !ok {
		t.Fatalf("thread_status must include a tasks array, got %T", statusRes["tasks"])
	}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run 'TestCollect|TestDispatchHappyPath' -v`
Expected: FAIL — `tool "collect" not registered`; happy-path FAILs on the missing `tasks` key.

- [x] **Step 3: Implement**

Add the shared renderer to `cmd/styx/mcp_tasks.go`:

```go
// taskLine renders one task for thread_status / collect pending summaries.
func taskLine(t bgTask) string {
	switch t.State {
	case taskRunning:
		return fmt.Sprintf("%s %s (%s, thread %s, %s)", t.ID, t.State, t.Spec.CLI, t.Spec.Thread, elapsedShort(time.Since(t.Started)))
	case taskQueued:
		behind := "at cap"
		if t.QueuedBehind != "" {
			behind = "behind " + t.QueuedBehind
		}
		return fmt.Sprintf("%s queued %s (%s, thread %s, %s)", t.ID, behind, t.Spec.CLI, t.Spec.Thread, elapsedShort(time.Since(t.Created)))
	default:
		claimed := ""
		if !t.Claimed {
			claimed = " unclaimed"
		}
		return fmt.Sprintf("%s %s%s (%s, thread %s)", t.ID, t.State, claimed, t.Spec.CLI, t.Spec.Thread)
	}
}
```

Append the `collect` tool to `conductorTools` (after `rate_dispatch`):

```go
		{
			Name: "collect",
			Description: "Fetch background dispatch results. With task_id: that task's result " +
				"(or its live status). Without: every finished-unclaimed result plus one-line " +
				"summaries of running/queued tasks.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "task id from dispatch background:true; omit to collect everything finished"},
			}},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in struct {
					TaskID string `json:"task_id"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("collect args: %w", err)
				}
				if in.TaskID != "" {
					tk, ok := d.reg.Get(in.TaskID)
					if !ok {
						return nil, fmt.Errorf("unknown task %q — thread_status lists live and unclaimed tasks", in.TaskID)
					}
					return collectOne(d.reg, tk), nil
				}
				results := []map[string]any{}
				pending := []string{}
				for _, tk := range d.reg.Snapshot() {
					switch tk.State {
					case taskQueued, taskRunning:
						pending = append(pending, taskLine(tk))
					default:
						if !tk.Claimed {
							results = append(results, collectOne(d.reg, tk))
						}
					}
				}
				return map[string]any{"results": results, "pending": pending}, nil
			},
		},
```

And the shared single-task collector in `cmd/styx/mcp_conductor.go`:

```go
// collectOne shapes one task's collect payload. Finished tasks (done, error,
// orphaned) are claimed by being collected; live tasks report status only.
func collectOne(reg *taskRegistry, tk bgTask) map[string]any {
	switch tk.State {
	case taskQueued, taskRunning:
		out := map[string]any{
			"task_id": tk.ID, "status": tk.State,
			"elapsed_s": math.Round(time.Since(tk.Created).Seconds()*10) / 10,
		}
		if tk.QueuedBehind != "" {
			out["queued_behind"] = tk.QueuedBehind
		}
		return out
	case taskDone:
		out := map[string]any{"task_id": tk.ID, "status": taskDone}
		for k, v := range tk.Result {
			out[k] = v
		}
		reg.Claim(tk.ID)
		return out
	default: // error, orphaned
		reg.Claim(tk.ID)
		return map[string]any{
			"task_id": tk.ID, "status": tk.State, "error": tk.Err,
			"thread": tk.Spec.Thread, "cli": tk.Spec.CLI,
		}
	}
}
```

In the `thread_status` handler, change the return to include task rows:

```go
				tasks := []string{}
				for _, tk := range d.reg.Snapshot() {
					if tk.State == taskQueued || tk.State == taskRunning || !tk.Claimed {
						tasks = append(tasks, taskLine(tk))
					}
				}
				return map[string]any{"threads": m.mgr.StatusLines(), "tasks": tasks}, nil
```

`cmd/styx/mcp.go` readiness line: append `, collect` to the tool list string. `e2e/e2e_test.go` `TestFirstContact`: `if len(tools) != 13`.

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run 'TestCollect|TestDispatch|TestThreadStatus' -v -race` → PASS
Run: `make e2e` → PASS (count 13)

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md`: "MCP server" tool enumeration → thirteen (+`collect`); "Conductor MCP tools" gains the `collect` bullet (both call shapes, claim-on-collect, orphan payload) and the `thread_status` bullet gains the `tasks` array. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test && make e2e
git add -A
git commit -m "feat(mcp): collect tool + background task rows on thread_status"
```

---

### Task 9: piggyback — every conductor tool result carries the background status line

**Files:**
- Modify: `cmd/styx/mcp_tasks.go` (`withBackgroundStatus`)
- Modify: `cmd/styx/mcp.go` (`cmdMCP` wraps the conductor tools)
- Test: `cmd/styx/mcp_tasks_test.go`

**Interfaces:**
- Produces: `func withBackgroundStatus(tools []mcpserver.Tool, reg *taskRegistry) []mcpserver.Tool` — the one decoration point from the spec. Map-shaped results gain `"bg"` when the registry holds live or unclaimed work; non-map results (e.g. the raw `shipgate.Result` pass-through) and errors pass through untouched.

- [x] **Step 1: Write the failing test**

Add to `cmd/styx/mcp_tasks_test.go` (add imports `"encoding/json"` — already added in Task 6 — and `"github.com/ishaanbatra/styx/internal/mcpserver"`, `"errors"`):

```go
func TestWithBackgroundStatusPiggyback(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4)
	tools := withBackgroundStatus([]mcpserver.Tool{
		{Name: "mapper", Handler: func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		}},
		{Name: "structer", Handler: func(context.Context, json.RawMessage) (any, error) {
			return "plain string", nil
		}},
		{Name: "failer", Handler: func(context.Context, json.RawMessage) (any, error) {
			return nil, errors.New("boom")
		}},
	}, reg)
	call := func(name string) (any, error) {
		for _, tl := range tools {
			if tl.Name == name {
				return tl.Handler(context.Background(), nil)
			}
		}
		t.Fatalf("no tool %s", name)
		return nil, nil
	}

	// Idle registry: no bg field.
	res, _ := call("mapper")
	if _, ok := res.(map[string]any)["bg"]; ok {
		t.Fatal("no tasks => no bg field")
	}

	run1, started1, release1 := blockingRun(nil)
	id, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"}, run1)
	<-started1
	defer close(release1)

	res, _ = call("mapper")
	bg, _ := res.(map[string]any)["bg"].(string)
	if !strings.Contains(bg, id) || !strings.Contains(bg, "running") {
		t.Fatalf("live task must piggyback on map results, got %q", bg)
	}
	// Non-map results and errors pass through untouched.
	if res, _ := call("structer"); res != "plain string" {
		t.Fatalf("non-map results must pass through, got %v", res)
	}
	if _, err := call("failer"); err == nil || err.Error() != "boom" {
		t.Fatalf("errors must pass through, got %v", err)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestWithBackgroundStatus -v`
Expected: FAIL — `undefined: withBackgroundStatus`

- [x] **Step 3: Implement**

Add to `cmd/styx/mcp_tasks.go` (import `"encoding/json"` is present from Task 6; add `mcpserver`):

```go
// withBackgroundStatus is the piggyback decoration point (spec §Piggyback):
// whenever the registry holds live or unclaimed tasks, every conductor tool's
// map-shaped result gains a compact "bg" status line, so any tool activity
// resurfaces background work — the conductor cannot forget for long. Non-map
// results (shipgate token relays) and errors pass through untouched; the
// JSON-RPC transport is untouched.
func withBackgroundStatus(tools []mcpserver.Tool, reg *taskRegistry) []mcpserver.Tool {
	out := make([]mcpserver.Tool, len(tools))
	for i, tl := range tools {
		inner := tl.Handler
		tl.Handler = func(ctx context.Context, raw json.RawMessage) (any, error) {
			res, err := inner(ctx, raw)
			if err != nil {
				return res, err
			}
			if line := reg.StatusLine(); line != "" {
				if m, ok := res.(map[string]any); ok {
					m["bg"] = line
				}
			}
			return res, nil
		}
		out[i] = tl
	}
	return out
}
```

In `cmdMCP`, change the tool assembly line:

```go
	tools := append(mcpTools(a), withBackgroundStatus(conductorTools(d), d.reg)...)
```

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -v -race` → PASS

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Conductor MCP tools": add the Piggyback paragraph (`withBackgroundStatus`, the `"bg"` field, map-results-only rule). Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(mcp): piggyback background status line onto every conductor tool result"
```

---

### Task 10: `FAKEAGENT_SLEEP` + conductor-level background→piggyback→collect roundtrip

**Files:**
- Modify: `testdata/fakeagent` (sleep knob)
- Test: `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Produces: fakeagent env knob `FAKEAGENT_SLEEP` (seconds; sleeps before emitting events).

- [x] **Step 1: Extend fakeagent**

In `testdata/fakeagent`, add to the knobs comment block:

```bash
#   FAKEAGENT_SLEEP        sleep N seconds before emitting (background tests)
```

and immediately after the `OUT=${FAKEAGENT_OUT:-20}` defaults line (so both protocols share it):

```bash
if [ -n "$FAKEAGENT_SLEEP" ]; then
  sleep "$FAKEAGENT_SLEEP"
fi
```

- [x] **Step 2: Write the failing roundtrip test**

Add to `cmd/styx/mcp_conductor_test.go`:

```go
func TestBackgroundDispatchRoundtrip(t *testing.T) {
	// Full conductor-level lifecycle against a real (fake) CLI subprocess:
	// dispatch background → immediate task handle → piggyback on thread_status
	// → collect the finished result → outcome row carries the task id.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "bg-done")
	t.Setenv("FAKEAGENT_SLEEP", "1")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	d := &conductorDeps{
		a: &app{
			routing: config.Routing{
				Brain: config.BrainConfig{ContextThresholdPct: 70},
				Tiers: map[string]string{},
			},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{},
		reg:      newTaskRegistry(context.Background(), 4),
	}

	res, err := callTool(t, d, "dispatch", map[string]any{
		"project": "proj1", "cli": "claude", "message": "long job", "risk": "read",
		"background": true,
	})
	if err != nil {
		t.Fatalf("background dispatch: %v", err)
	}
	taskID, _ := res["task_id"].(string)
	if taskID == "" || res["status"] != "running" {
		t.Fatalf("want immediate task handle, got %v", res)
	}

	// While running: thread_status carries the task row (piggyback is
	// covered by TestWithBackgroundStatusPiggyback; here we assert the
	// conductor-level surface).
	ts, err := callTool(t, d, "thread_status", map[string]any{"project": "proj1"})
	if err != nil {
		t.Fatalf("thread_status: %v", err)
	}
	tasks, _ := ts["tasks"].([]any)
	if len(tasks) != 1 || !strings.Contains(tasks[0].(string), taskID) {
		t.Fatalf("running task must appear in thread_status tasks, got %v", ts["tasks"])
	}

	// Poll collect until done (fakeagent sleeps 1s).
	waitFor(t, "task done", func() bool {
		got, err := callTool(t, d, "collect", map[string]any{"task_id": taskID})
		return err == nil && got["status"] == "done"
	})
	// The done collect above claimed it — re-collect by id shows the claimed
	// result again (Get still knows it), but collect({}) has nothing pending.
	all, err := callTool(t, d, "collect", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if results, _ := all["results"].([]any); len(results) != 0 {
		t.Fatalf("claimed task must not resurface in collect all, got %v", all["results"])
	}

	// Outcome row: background flag + task id.
	rows, err := bud.OutcomesSince(context.Background(), time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("want 1 outcome row, got %d (%v)", len(rows), err)
	}
	if !rows[0].Background || rows[0].TaskID != taskID || rows[0].CLI != "claude" {
		t.Fatalf("background outcome row mismatch: %+v", rows[0])
	}
}
```

Note: the `waitFor` collect-poll consumes the "done" collect (claiming it). That is the intended piggyback+collect flow.

- [x] **Step 3: Run the test**

Run: `go test ./cmd/styx/ -run TestBackgroundDispatchRoundtrip -v -race`
Expected: FAIL before Step 1's fakeagent edit is saved (no sleep → possible flake) and PASS after; if it fails on `status != "running"` the fakeagent sleep isn't taking effect — check the knob placement above the protocol blocks.

Run: `go test ./internal/agent/ -v` → PASS (fakeagent change must not disturb runner/manager tests — they don't set FAKEAGENT_SLEEP).

- [x] **Step 4: Commit**

`docs/ARCHITECTURE.md` "Agent threads" fakeagent sentence: add the `FAKEAGENT_SLEEP` knob to the fixture description. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "test(mcp): FAKEAGENT_SLEEP knob + background dispatch roundtrip coverage"
```

---

### Task 11: guidance teaches background dispatch, collect, and rate_dispatch (seed → V3)

**Files:**
- Modify: `internal/guidance/guidance.go`
- Test: `internal/guidance/guidance_test.go` (extend existing seed-upgrade tests — check `grep -n "seedV" internal/guidance/guidance_test.go` for the current pattern)

**Interfaces:**
- Produces: new `Seed` content; previous Seed retained verbatim as `const seedV3`; `Load` recognizes `seedV1 || seedV2 || seedV3` as unmodified.

- [x] **Step 1: Write the failing test**

Add to `internal/guidance/guidance_test.go` (mirroring the existing upgrade test's structure):

```go
func TestSeedV3UpgradesToCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := guidanceFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(seedV3), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != Seed {
		t.Fatal("an unmodified v3 seed must upgrade to the current Seed")
	}
	for _, want := range []string{"background: true", "collect", "rate_dispatch"} {
		if !strings.Contains(Seed, want) {
			t.Fatalf("current Seed must teach %q", want)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/guidance/ -run TestSeedV3 -v`
Expected: FAIL — `undefined: seedV3`

- [x] **Step 3: Implement**

1. Copy the entire current `Seed` constant into a new `const seedV3 = …` (verbatim), with the comment:

```go
// seedV3 is the pre-async-dispatch conductor seed (shipped 2026-07-07,
// before background tasks / collect / rate_dispatch). Kept verbatim so Load
// can detect an unmodified v3 file and upgrade it transparently.
```

2. In `Load`, extend the unmodified-seed check:

```go
	if s := string(b); s == seedV1 || s == seedV2 || s == seedV3 {
```

3. Edit `Seed`:

- Tool list line becomes:

```
AI channels. You have MCP tools: dispatch, collect, thread_status,
budget_status, recall, memory_save, get_intel, refresh_intel,
pipeline_run, route, channel_health, record_usage, rate_dispatch.
```

- Insert a new section after "## Channel best purposes" (before "## Working style"):

```
## Background dispatch (fire and keep talking)

Dispatch independent, multi-minute work with background: true — you get a
task_id back immediately and can keep working. Rules of thumb:
- Fire independent tasks in the background; only dispatch synchronously
  when you need the answer before your next step.
- Every tool result carries a "bg" line while tasks are live or unclaimed;
  call collect (with a task_id, or bare for everything finished) BEFORE
  synthesizing results or when the user asks for status.
- Tasks on the same thread, and edit-risk tasks on the same project, queue
  rather than run in parallel — the status shows what they wait behind.
- risk=ship never runs in background (the token handshake is interactive).
- Background work dies if this styx mcp session ends; losses are reported
  as "orphaned" — re-dispatch if still needed.

## Rating outcomes

When a dispatch turns out notably good or bad (clean first-try implement,
wandered off-plan, wrong channel for the job), call rate_dispatch with the
thread name or task id and a one-line note. Rate only notable outcomes —
not every dispatch. Ratings feed styx's learning loop.
```

- [x] **Step 4: Run tests**

Run: `go test ./internal/guidance/ -v` → PASS (including the existing seedV1/seedV2 upgrade tests)

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Guidance" section: mention the V3→current upgrade and the two new taught behaviors. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(guidance): teach background dispatch, collect, and rate_dispatch (seed v4, v3 retained)"
```

---

### Task 12: e2e background roundtrip over real JSON-RPC

**Files:**
- Modify: `e2e/e2e_test.go` (`startServer` gains extra env; new test)

**Interfaces:**
- Consumes: everything above via a real `styx mcp` subprocess.
- Produces: `startServer(t *testing.T, extraEnv ...string)` (backward-compatible variadic); `TestBackgroundDispatchRoundtrip`.

- [x] **Step 1: Extend startServer**

Change the signature and the env block in `e2e/e2e_test.go`:

```go
func startServer(t *testing.T, extraEnv ...string) (*mcpClient, string) {
```

```go
	srv.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKEAGENT_TEXT=e2e-ok",
	)
	srv.Env = append(srv.Env, extraEnv...)
```

- [x] **Step 2: Write the roundtrip test**

Add to `e2e/e2e_test.go`:

```go
func TestBackgroundDispatchRoundtrip(t *testing.T) {
	c, _ := startServer(t, "FAKEAGENT_SLEEP=2")

	// Fire-and-return: immediate task handle.
	disp, isErr := c.toolCall("dispatch", map[string]any{
		"cli": "claude", "message": "slow job", "risk": "read", "background": true,
	})
	if isErr {
		t.Fatalf("background dispatch errored: %v", disp)
	}
	taskID, _ := disp["task_id"].(string)
	if taskID == "" || disp["status"] != "running" {
		t.Fatalf("want immediate task handle, got %v", disp)
	}

	// Piggyback: any conductor tool result carries the bg line while the
	// task is live.
	ts, isErr := c.toolCall("thread_status", map[string]any{})
	if isErr {
		t.Fatalf("thread_status errored: %v", ts)
	}
	if bg, _ := ts["bg"].(string); !strings.Contains(bg, taskID) {
		t.Fatalf("bg piggyback must name the live task, got %v", ts["bg"])
	}

	// Collect: poll until done (fakeagent sleeps 2s; 30s budget).
	deadline := time.Now().Add(30 * time.Second)
	var got map[string]any
	for {
		if time.Now().After(deadline) {
			t.Fatalf("task %s never finished; last collect: %v", taskID, got)
		}
		var isErr bool
		got, isErr = c.toolCall("collect", map[string]any{"task_id": taskID})
		if isErr {
			t.Fatalf("collect errored: %v", got)
		}
		if got["status"] == "done" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if got["text"] != "e2e-ok" {
		t.Fatalf("collected result must carry the dispatch text, got %v", got)
	}

	// Claimed: collect({}) has nothing left; the bg line is gone.
	all, isErr := c.toolCall("collect", map[string]any{})
	if isErr {
		t.Fatalf("collect all errored: %v", all)
	}
	if results, _ := all["results"].([]any); len(results) != 0 {
		t.Fatalf("claimed task must not resurface, got %v", all["results"])
	}
	if bg, ok := all["bg"]; ok {
		t.Fatalf("no live/unclaimed tasks => no bg line, got %v", bg)
	}
}
```

- [x] **Step 3: Run**

Run: `make e2e`
Expected: `TestFirstContact` (13 tools), `TestVersionVerb`, `TestBackgroundDispatchRoundtrip` PASS; `TestLiveSmoke` SKIP.
Debug tip: server stderr passes through — look for `task t1 started` / `task t1 done` `[styx]` lines; a roundtrip hang usually means the fakeagent sleep knob or the registry promotion loop.

- [x] **Step 4: Commit**

`docs/ARCHITECTURE.md` "Testing conventions" e2e paragraph: add the background roundtrip to the described tool-call sequence. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test && make e2e
git add -A
git commit -m "test(e2e): background dispatch -> piggyback -> collect roundtrip over JSON-RPC"
```

---

## Post-plan verification (whole-phase acceptance)

- [x] `make test && make e2e` green; `go test ./cmd/styx/ -race` green.
- [x] `STYX_E2E_LIVE=1 make e2e` green on this machine (the one sanctioned live run; needs ollama up).
- [x] Manual conductor session: `make install`, then `styx` in this repo; ask the conductor to "dispatch a 2-minute codex investigation in the background, keep chatting, then collect it". Verify: immediate task handle, `bg` lines on subsequent tool results, collect returns the result, `rate_dispatch` on the thread succeeds, and the row shows in sqlite: `sqlite3 ~/.config/styx/state/usage.db 'SELECT cli, background, rating FROM outcomes ORDER BY id DESC LIMIT 3;'`.
- [x] Kill the `styx mcp` process mid-task (quit Claude Code during a background dispatch), relaunch `styx`, and confirm the orphan is reported by the first tool call's `bg` line and by `collect`.
- [x] `docs/ARCHITECTURE.md` `last_verified` is the final commit date; `git log --oneline` shows one commit per task.
