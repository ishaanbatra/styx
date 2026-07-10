---
topic: Live Dispatch Observability — Activity Board, Heartbeat, and Ollama Watcher
date: 2026-07-07
status: design
author: styx (brainstormed with ishaanbatra)
---

# Live Dispatch Observability for Styx

> **Update (2026-07-08):** the Task-9 deferred limitation (disk mirror frozen
> between a background task's start and end) is closed by the awaited-dispatch
> design — see `2026-07-08-styx-awaited-dispatch-design.md`: a mechanical
> pulse in the conductor refreshes the mirror every second while anything is
> live, and dispatches now await by default with inline results.

## Problem

When a styx session dispatches work to agent CLIs — especially a parallel
fan-out to more than one channel — the run goes dark. The concrete trigger:
a `styx` session was given *"research the best ways to expand styx,"* fanned it
out to a claude thread and a codex thread, and then produced no output for the
whole multi-minute run. There was no way to tell a healthy, hard-working agent
from a hung one.

The darkness is structural, not cosmetic:

1. **Tool work is invisible by construction.** `internal/agent/event.go` parses
   only `init` / `text` / `result` events and explicitly drops `tool_use` lines
   (a test locks `tool_use → ok=false`). While an agent spends minutes running
   tools without emitting assistant text, **zero events surface**.
2. **Background dispatch is the darkest path.** `cmd/styx/mcp_conductor.go:449`
   passes `nil` as the `OnEvent` callback for background tasks — the comment says
   it out loud: *"No progress notifications mid-flight."* The parallel research
   run went through this path, so it captured no events at all; the task registry
   knew only `running` + an elapsed clock.
3. **There is no "last activity" concept anywhere.** "Hung" and "slow" are
   indistinguishable — the progress spinner (`internal/progress`) ticks
   identically either way. No idle timer, no stall threshold.

## Goals

- Surface a **live activity heartbeat** for every dispatched agent — what tool it
  is running right now, and how long since its last action.
- Render **one status line per agent** during a parallel fan-out, replacing the
  single opaque spinner.
- Provide a **free, local "watcher"** using ollama that keeps an eye on all
  dispatched agents at once and reports (in plain language) whether they look
  healthy or stuck.
- Make in-flight work observable both **inline** (while the session waits) and
  from a **second terminal** (`styx watch`).

## Non-Goals

- The watcher **never acts** — no auto-abort, no auto-restart. It reports; the
  human decides. (Rejected "supervisor with intervention" approach: risky, and
  against styx's transparent, user-in-control ethos.)
- No new provider SDKs, no daemons. Local ollama over its existing HTTP client;
  in-process state with a throttled on-disk mirror.
- Not a replacement for `styx runs` (pipeline stage state) — this is live
  *agent-turn* liveness, a different axis.

## Chosen Approach (B): mechanical truth underneath, ollama narration on top

Two layers, cleanly separated:

- A **mechanical layer** that is 100% deterministic, never depends on any model
  being up, and never lies: parsed tool events → a shared activity board →
  per-agent lines + an idle-based stall flag.
- An **ollama watcher layer** that is strictly additive: it reads the same board
  and produces a natural-language health read, catching soft anomalies the
  mechanics can't (loops, repeated re-reads, a stall that is actually fine). If
  ollama is unavailable, this layer silently disappears and the mechanical layer
  stands alone.

### The key structural move

Activity capture moves **out of the caller's `OnEvent` callback and into the
`Runner`**, which writes every parsed event to a shared board. This instruments
sync *and* background dispatch in one change — the `nil` at
`mcp_conductor.go:449` stops mattering, because liveness no longer flows through
that callback. `OnEvent` is left exactly as-is for the REPL's existing streaming;
the board is a new, parallel substrate.

## Architecture

```
agent CLI stream ──► Runner (parses events) ──┬─► OnEvent  (unchanged: REPL streaming)
                                              └─► activity.Board.Record(label, summary)
                                                        │
                        ┌───────────────────────────────┼───────────────────────────────┐
                        ▼                               ▼                                 ▼
             mechanical renderer            ollama watcher goroutine            throttled disk mirror
             (per-agent lines + ⚠ stall)    (health note → board.WatcherNote)   (task-mirror files)
                        │                               │                                 │
              inline board (a) + REPL /watch (b)  ◄─────┘                       styx watch (c, 2nd terminal)
```

### Component 1 — Event plumbing (`internal/agent/event.go`)

- New `EventTool EventType = "tool"`. `Event` gains `Tool string` (tool name);
  the target reuses the existing `Text` field.
- `ParseClaudeEvent`: decode `tool_use` content blocks (currently dropped) —
  extract `name` plus a best-effort target from `input`:
  Bash→`command`, Read/Edit→`file_path`, WebFetch→`url`, else tool name only.
  Emit `EventTool{Tool: "Bash", Text: "go test ./..."}`.
- `ParseCodexEvent`: add a case for codex's command / tool `item.completed`
  types (only `agent_message` is handled today) → `EventTool`. Exact item-type
  strings are verified against a real `codex exec --json` stream during
  implementation.
- The test asserting `tool_use → ok=false` flips to assert `EventTool`.

### Component 2 — Activity board (`internal/activity`, new package)

A thread-safe `Board`, one per styx session, keyed by agent **label** (thread
name / task id). Stores **strings + timestamps only** — no `agent.Event` — so
there is no import cycle; the `Runner` formats the one-line summary and calls
`Record`.

```go
type Board struct { /* mu; per-label ring buffers + last-seen */ }

func (b *Board) Record(label, summary string)   // stamps time.Now()
func (b *Board) Done(label string, elapsed time.Duration)
func (b *Board) Snapshot() []AgentState          // for the renderer
func (b *Board) Recent(label string) []string    // last ~20 lines, for the watcher
func (b *Board) SetWatcherNote(note string)
func (b *Board) WatcherNote() string

type AgentState struct {
    Label, Last string
    LastAt      time.Time
    Done        bool
    Elapsed     time.Duration
    Recent      []string
}
func (s AgentState) IdleFor() time.Duration
```

Ring buffer capped (~20/agent) so the watcher prompt stays tiny.

### Component 3 — Wiring

`Runner` gains `Board *activity.Board` + `Label string`; on every parsed event
it formats a summary and calls `board.Record`, independent of `OnEvent`.
`Manager` gains a `Board` field and passes it (plus the thread name as label)
into each `Runner`. `loadApp` / the conductor construct one `Board` per session
and hand it to the renderer and the watcher. **Background dispatch needs no
change** — it already goes through `Manager.Dispatch` → `Runner`, so it is
instrumented automatically.

### Component 4 — Mechanical renderer + stall flag

`internal/activity` owns a pure formatter `Render(snapshot) []string`. A small
TTY-aware refresh loop (clear N lines, repaint ~1s, mirroring `progress.go`'s
`isatty` handling) drives it; non-TTY / `--quiet` falls back to occasional plain
lines with no repaint.

```
claude  ▸ WebFetch example.com          2s ago
codex   ⚠ idle 94s   (last: go test ./...)
```

Stall flag: `IdleFor() > threshold` (default 90s, configurable) → `⚠`. A **soft
warning that never aborts** — some tools legitimately run for minutes. Finished
agents show `✓ done (3m12s)`. This layer never touches ollama.

### Component 5 — Ollama watcher

A goroutine started only when ≥1 agent is live *and* ollama is reachable. Every
~15–20s — and immediately when an agent first crosses the stall threshold — it:

1. reads `Snapshot()` + `Recent(label)` per agent,
2. builds a tiny prompt (per-agent last-N actions + idle time),
3. calls local ollama **directly via the existing ollama HTTP client, bypassing
   the routing table and budget ledger** — this is observation, not dispatch:
   free and unmetered,
4. gets a 1–2 sentence health read, stores it via `SetWatcherNote`,
5. the renderer paints it under the per-agent lines:

```
watch (ollama): both healthy; claude deep in web research (12 fetches),
                codex idle since go test — likely still compiling, not stuck.
```

Prompt shape — system: *"You are a watch process observing parallel AI coding
agents. In 1–2 sentences say whether they look healthy or stuck. Be terse. Flag
loops, repeated identical actions, and long idles."* User: per-agent recent
activity lines.

Strictly best-effort: ollama down / slow / erroring → the watcher silently
disables and the mechanical layer stands alone. It never blocks dispatch and it
never acts. Model configurable, default a small fast local model discovered from
`ollama tags` with graceful fallback.

### Component 6 — Surfaces

- **(a) Inline while the conductor waits on dispatched agents.** The live board
  replaces the lone spinner. Direct fix for the reported scenario (session fanned
  out to claude+codex, then went silent while waiting).
- **(b) REPL `/watch` command.** Pull up the live board on demand when tasks are
  running detached; also enrich the existing piggyback `bg` line
  (`mcp_tasks.go`) with last-event + idle so even passive tool calls show richer
  status.
- **(c) Standalone `styx watch` CLI (second terminal, cross-process).** Reads a
  throttled on-disk mirror of the board. The existing task-mirror files
  (`mcp_tasks.go`) are extended to carry `last_event`, `last_activity_at`, and
  `watcher_note`; writes are throttled (debounced ~2s) to avoid per-event churn.
  The same `Render` formatter paints the mirror a second process reads.

## Configuration (`~/.config/styx/`)

- `watch.stall_threshold` (default `90s`)
- `watch.ollama_enabled` (default `true`; auto-off when ollama unreachable)
- `watch.ollama_model` (default: first small model from `ollama tags`)
- `watch.interval` (default `15s`)
- All respect `--quiet` and non-TTY (plain periodic lines, no repaint).

## Error Handling / Degradation

- Ollama unreachable or erroring → watcher disables silently; mechanical layer
  unaffected.
- Board writes never fail dispatch (in-memory; mirror writes are best-effort,
  narrated on failure per `writeTaskFile` convention — never swallowed).
- Stall flag is advisory only; no automatic termination.
- Disk mirror is best-effort; a missing/stale mirror makes `styx watch` report
  "(no live activity)" rather than erroring.

## Testing

Table-driven, `t.Run`, fakes over mocks (repo standard):

- `event_test.go` — claude tool-use target extraction (Bash/Read/Edit/WebFetch),
  codex command items, the flipped `tool_use → EventTool` case.
- `activity` — `Record`/`Snapshot`/`Recent` under parallel writers, ring-buffer
  cap, `IdleFor`, stall threshold, pure `Render` output.
- Watcher — fake ollama via `httptest`: note lands on the board on success; a
  down/erroring ollama degrades silently with the mechanical layer intact.
- `styx watch` — throttled disk mirror round-trips; renderer paints the same
  board a second process sees.

## Doc Obligations (drift contract, same commit as code)

- `docs/ARCHITECTURE.md` — add `internal/activity` + the ollama watcher to the
  subsystem list; document the extended task-mirror layout; bump `last_verified`.
- `README.md` — add `styx watch` to the verb table; bump `last_verified`.

## Rejected Alternatives

- **Mechanical-only (no LLM).** Simpler, but ignores the free-local-watcher idea
  and offers no judgment about whether an activity *pattern* is healthy.
- **Ollama supervisor that intervenes.** Auto-abort/restart of wedged agents —
  rejected as risky and against styx's transparent-control ethos.
