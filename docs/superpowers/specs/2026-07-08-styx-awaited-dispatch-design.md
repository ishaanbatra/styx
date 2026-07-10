---
topic: Awaited Dispatch — Live In-Chat Progress and Inline Findings for the Conductor
date: 2026-07-08
status: design
author: styx (brainstormed with ishaanbatra)
---

# Awaited Dispatch for the Conductor

## Problem

In a Claude Code conductor session, a background dispatch (`dispatch` with
`background: true`) goes completely dark in the chat. The concrete trigger: a
read-only dead-code scan was dispatched as task t1; for ~7 minutes the chat
showed nothing, and when t1 finished, nothing appeared until the user typed
"status?" — only then did the conductor call a styx tool and surface the
findings from the piggyback `bg` line.

The requirement, verbatim in spirit: *the chat interface needs to work; the
user must not have to keep asking Claude to report findings.* Live progress
and final results must land in the Claude Code chat automatically. OS/desktop
notifications are explicitly not acceptable. Ollama cost/cadence is not a
concern (local and free).

Root causes (verified in code):

1. **Completion is announced to nobody.** `cmd/styx/mcp_tasks.go` announces a
   finished background task via `logStatus` — the MCP server's own stderr. The
   conductor model learns of it only when *it* next takes a turn. MCP is
   request/response: a server cannot inject a message into an idle chat.
2. **The disk mirror freezes mid-run.** Background dispatch passes a `nil`
   `onEvent`, and `mirrorNow()` is called only in a bracket around the
   dispatch. The `activity.Board` captures every tool event (Runner-side,
   PR #15), but nothing refreshes the mirror between start and end — so even
   `styx watch` in a second terminal shows only the opening frame. This was
   the Task-9 deferred limitation.
3. **The synchronous path can't substitute.** It streams progress but only
   coarse "streaming (N events)" lines, drops tool events, and is single-agent
   — which is why the conductor's policy reaches for `background: true`.
4. **The server is serial.** `mcpserver.Serve` handles one request to
   completion before reading the next line. A minutes-long blocking call would
   freeze every other tool call *and* the read loop itself — even a
   cancellation notification would sit unread.

## The protocol constraint that shapes everything

The findings can reach the chat in exactly two ways: (a) the user takes a turn
(polling — rejected), or (b) **the tool call does not return until the work is
done**, streaming `notifications/progress` while it runs and returning the
findings as its result. Progress notifications are rendered by the Claude Code
UI directly — they never enter the model's context, so they cost zero
subscription tokens. Only the final result does, once.

Therefore: awaited dispatch is the default mode; everything else in this
design exists to make awaiting robust (concurrency, cancellation) and to keep
deliberately-detached work observable (mechanical pulse).

## Goals

- Work the conductor waits on streams **live, tool-by-tool progress into the
  chat** and delivers findings **inline the moment it finishes** — for one
  agent or N in parallel, with zero extra model turns and zero polling.
- Progress content is **mechanical truth every ~2s** (board-derived per-agent
  lines with the stall flag) plus **ollama narration every ~15s** (the
  existing watcher note) — free, local, and absent-but-harmless when ollama
  is off.
- Deliberately-detached work (`background: true`) stays fully observable:
  `styx watch` becomes **live mid-run**, and completions surface at the
  earliest protocol-legal moment (any in-flight awaited call's progress
  stream, else the piggyback line on the next call).
- A long awaited call must not freeze the server: other tool calls, other
  sessions, and cancellation keep working.
- User interrupt (Esc) during an await **detaches** the agents — work is never
  lost; it continues as collectible background tasks. (User decision.)

## Non-Goals

- Waking an idle chat when a background task finishes with no call in flight.
  MCP has no push channel; the compensation is awaited-by-default plus instant
  surfacing on the next activity. Not fixable, by protocol.
- OS/desktop notifications (explicitly rejected by the user).
- REPL-side changes. The REPL already streams via `OnEvent` and has `/watch`;
  this design is conductor/MCP-side. The activity/mirror substrate is shared.
- Changing the ollama watcher's role. It remains narration-only, 15s cadence,
  gated on `watch.ollama_enabled`, strictly best-effort — per the
  observability spec's mechanical-layer principle.

## Chosen Approach: awaited = observed background

Do not build a second dispatch path. An awaited dispatch **spawns ordinary
registry background tasks and observes them until done**. The handler becomes
a viewer over machinery that already exists:

- The registry already provides task IDs, per-thread serialization, the
  per-project write queue, crash honesty (orphan files), and rootCtx-scoped
  lifetimes.
- Detach-on-Esc is free: the agents *are* background tasks; cancelling the
  call stops the observer, not the work.
- Completion detection is a registry state read (in-process), not board
  diffing.
- Sync-vs-background code drift ends: one pipeline, two views (awaited /
  detached).

Rejected alternatives:

- **Fold mirror refresh + completion detection into the ollama watcher** (the
  original direction). Fatal coupling: the watcher is gated on
  `watch.ollama_enabled`, and the mechanical layer must never depend on a
  model being up. Fixing that means running the loop regardless of ollama — at
  which point it *is* the mechanical pulse below, just tangled into a
  narration component. Also the cadences disagree (mirror wants ~1–2s, ollama
  ~15s).
- **Concurrent server + N parallel single-dispatch calls from the client.**
  Relies on Claude Code choosing to parallelize MCP calls, which is not
  guaranteed. (The server still becomes concurrent — see Component 4 — but as
  robustness, not as the fan-out mechanism.)
- **Minimal sync-path polish only.** No fan-out, server still frozen during a
  long call, Esc semantics unresolved. Fixes one scenario, not the contract.

## Architecture

```
                       dispatch (awaited, N=1)  /  dispatch_parallel (awaited, N≥1)
                                        │ spawn via taskRegistry (existing rules)
                                        ▼
   agent CLIs ──► Runner ──► activity.Board          taskRegistry (running → done)
                                   │  ▲                     │
                                   │  │ note (15s,          │
                                   │  │ best-effort)        │
                                   │  ollama watcher        │
                                   ▼                        ▼
        ┌──────────── awaiter loop (per awaited call, ticks ~1s) ────────────┐
        │  compose progress line: per-agent last action + stall flag +       │
        │  watcher note + other-task transitions → notifications/progress    │
        │  all awaited tasks terminal → claim → return combined results      │
        └─────────────────────────────────────────────────────────────────────┘

        mechanical pulse (server goroutine, ticks 1s while anything is live)
            └─► mirrorNow() ──► throttled disk mirror ──► styx watch (live)

        mcpserver: tools/call on goroutines; notifications/cancelled cancels
        the matching call's context (Esc → awaiter returns, tasks continue)
```

## Components

### 1 — Tool surface (`cmd/styx/mcp_conductor.go`)

- **`dispatch` (existing).** For claude/codex/agy without `background: true`,
  the handler becomes spawn-one-task + await it. Visible changes: rich
  progress instead of "streaming (N events)"; a thread-busy collision now
  **queues with narration** ("queued behind t2") instead of erroring; outcome
  rows gain a task id for formerly-sync dispatches. `background: true`,
  ollama one-shots, and the ship-risk confirm handshake keep their current
  shapes (ship runs through the same spawn+await after the handshake passes;
  the registry treats ship like edit for ordering).
- **`dispatch_parallel` (new).** `tasks: [{cli, message, thread?, model?,
  risk, project?, extra_roots?}]`. Rules per task mirror background spawn:
  no ship risk, no ollama, spawn-time budget/circuit check per task (a failed
  check fails that task at spawn, reported in the combined result — it does
  not abort siblings already spawned). Spawns all, awaits all, returns
  `{results: [...], detached: bool}` with per-task status/text/tokens/
  duration; partial failures are per-task entries, never a top-level error.
- **Tool descriptions steer the conductor**: awaited is the default for
  anything the user is waiting on; `background: true` only when the user
  explicitly wants to keep working while it runs. This guidance is part of
  the fix — the conductor's policy caused the original silence.

### 2 — Awaiter (`cmd/styx/mcp_await.go`, new)

One function: given spawned task IDs, a progress emitter, and the call ctx,
tick ~1s until every awaited task is terminal.

- Each tick: read `reg.Get` for awaited ids + `board.Snapshot()` +
  `board.WatcherNote()`. Compose one compact line, e.g.
  `1/3 done · claude ▸ Grep internal/router (4s) · codex ⚠ idle 96s · watch: both healthy`.
  Emit via `notify` only when the line changed (no identical-notification
  spam). Reuse `internal/activity`'s render vocabulary (▸ / ⚠ / ✓, idle
  thresholds) rather than inventing a second format.
- Transitions of **non-awaited** tasks observed between ticks are appended
  once ("t2 done — collect"), so an in-flight await also surfaces unrelated
  background completions — the earliest protocol-legal moment.
- All awaited tasks terminal → `reg.Claim` each (results are being delivered
  inline; they must not resurface in `collect`/piggyback) → return combined
  results.
- `ctx.Done()` before terminal → **detach**: return immediately with
  `{detached: true, tasks: [ids...]}`, claim nothing. The tasks run on
  rootCtx and remain visible via piggyback, `collect`, and `styx watch`.
  (If the call was cancelled the client discards the response; if it was a
  server-side deadline the conductor reads the detach notice.)
- No progress token from the client → same loop, no notifications; inline
  results still work.

### 3 — Registry adjustments (`cmd/styx/mcp_tasks.go`)

- Awaited spawns reuse `Spawn` unchanged (polling `Get` at 1s replaces any
  need for completion channels — in-memory mutex reads are free).
- Ship-risk tasks become spawnable (awaited path only; background spawn still
  rejects ship). The conflict rule already treats non-read as the write lock.
- `logStatus` completion lines stay (server stderr is still the operator log).

### 4 — Concurrent server (`internal/mcpserver/server.go`)

- `tools/call` handlers run on their own goroutine; the response is written on
  completion (writes already serialize on `s.mu`). `initialize` / `tools/list`
  stay inline (fast). JSON-RPC correlates by id, so out-of-order responses are
  protocol-legal.
- Track in-flight calls: map request-id → `context.CancelFunc`. Handle the
  `notifications/cancelled` notification by cancelling the matching context —
  this is what makes Esc reach the awaiter. Per MCP, no response is required
  for a cancelled request; we still return the detach notice (harmless if
  discarded).
- `Tool` gains `Serial bool`: serial tools share one mutex, protecting
  handlers not audited for concurrency (`pipeline_run` — it drives whole
  pipelines with shared app state). Dispatch, `dispatch_parallel`, `collect`,
  `thread_status`, `memory_save`, `rate_dispatch` run concurrent (their shared
  state — registry, managers map, budget sqlite — is mutex/driver-guarded).
- Two concurrent sync dispatches on one thread were impossible under the
  serial server; they are now safe *because* sync dispatch goes through the
  registry's ordering rules (Component 1) — the second queues.

### 5 — Mechanical pulse (`cmd/styx/mcp_conductor.go`)

A goroutine on the server's rootCtx (started in `newConductorDeps`, dies with
the server — no daemons):

- Ticks every 1s. When the board has any non-done agent or the registry has
  any live task → `d.mirrorNow()` (already throttled to 2s internally).
- On the live→idle transition, one final flush so `styx watch` shows ✓ done
  states instead of a stale last action.
- **No ollama dependency, no config gate.** This is the mechanical layer; it
  runs whenever the server runs. The ollama watcher goroutine is untouched —
  still `watch.ollama_enabled`-gated, still narration-only; its note simply
  gets picked up by the pulse's mirror writes and the awaiter's progress
  lines, on its own 15s cadence. This resolves the cadence-decoupling caveat.

### 6 — What the user sees

- **Chat, awaited call in flight:** the tool call stays visibly running with
  a live status line updating beneath it (mechanical every ~1–2s, narration
  every ~15s). On finish, findings arrive as the tool result and the model
  reports them immediately. No "status?", no tokens spent on progress.
- **Chat, Esc mid-await:** the conductor is interrupted; agents keep working.
  The next tool call's `bg` line (existing piggyback) shows them; `collect`
  fetches results; explicitly asking the conductor to cancel is how they die.
- **Second terminal:** `styx watch` now updates tool-by-tool for background
  and awaited work alike (Task-9 limitation closed).
- **Ollama off:** identical experience minus the narration sentence.

## Error handling / degradation

- Agent failure inside an awaited task: for `dispatch_parallel` the failure
  is a per-task entry in the combined result, never a call-level error unless
  every task failed at spawn. Single-agent `dispatch` keeps today's shape —
  the classified error is returned as the tool error.
- Progress emission failures are best-effort (write errors on a dying pipe
  are ignorable; the result path is what matters).
- Mirror write failures: narrated via `logStatus`, never fail dispatch
  (existing convention, unchanged).
- Ollama down: watcher silently idle; awaiter lines simply omit the note;
  pulse unaffected.
- Server crash mid-await: existing orphan honesty covers it — next server
  adopts task files as orphans and reports the loss.
- Every agent subprocess remains bounded by `Manager.Timeout` (10 min default)
  — an awaited call's duration is bounded by its slowest task. README gains a
  note on raising the MCP client tool-call timeout (`MCP_TOOL_TIMEOUT`) for
  long dispatches; progress notifications also signal liveness to clients
  that reset timeouts on progress.

## Configuration

No new knobs. Pulse (1s) and awaiter tick (1s) are constants; the mirror
throttle (2s) and watcher interval/gate (`watch.interval_seconds`,
`watch.ollama_enabled`) already exist. YAGNI until a real need appears.

## Testing (repo standard: table-driven, fakes over mocks)

- `mcpserver`: two concurrent `tools/call`s interleave (slow tool finishes
  after fast tool's response is written); `notifications/cancelled` cancels
  the right context; `Serial` tools mutually exclude; progress notifications
  from concurrent calls don't interleave corruptly (encoder mutex).
- Awaiter: scripted registry + board → progress lines compose and dedupe;
  non-awaited task transition narrated once; all-terminal → claimed + combined
  results; ctx cancel → immediate detach return, tasks unclaimed and running.
- Dispatch unification: sync `dispatch` queues behind a busy thread with
  narration instead of erroring; ship handshake still gates; outcome rows
  carry task ids.
- `dispatch_parallel`: spawn-time budget failure fails one task, siblings
  proceed; combined result shapes; ship/ollama rejected.
- Pulse: fake clock — mirror refreshed while a task is live with ollama off;
  final flush on live→idle; no writes while idle.

## Doc obligations (drift contract, same commit as code)

- `docs/ARCHITECTURE.md`: conductor tool table (+`dispatch_parallel`, awaited
  `dispatch` semantics), mcpserver concurrency + cancellation, mechanical
  pulse under the activity section; bump `last_verified`.
- `README.md`: conductor/MCP section — awaited default, background as opt-in,
  `MCP_TOOL_TIMEOUT` note; bump `last_verified`.
- `docs/superpowers/specs/2026-07-07-styx-dispatch-observability-design.md`
  gets a forward pointer noting Task-9's mid-run mirror limitation is closed
  by this design.

## Resolved decisions (from brainstorming)

- Scope: universal contract, not the single reported scenario (user).
- Esc during await: detach, never kill (user).
- Narration source: local ollama only — no subscription usage for progress;
  achieved structurally since progress notifications bypass the model (user
  requirement).
- Watcher stays ollama-gated; the pulse carries the mechanical duties (caveat
  resolution).
- Fan-out lives inside one tool call; client-side parallelism is not relied
  upon (serial-client uncertainty).
