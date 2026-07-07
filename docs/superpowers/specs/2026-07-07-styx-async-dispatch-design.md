---
topic: async dispatch (Phase B1) — background task handles for conductor dispatches
date: 2026-07-07
status: approved-design, plan pending
supersedes: the "Phase B — async dispatch" sketch in 2026-07-06-styx-improvement-roadmap-design.md
---

# Styx async dispatch (B1)

**Goal:** a conductor can fire a dispatch, keep talking, and pick up the
result later — a multi-minute codex run no longer occupies the conversation.
No daemons: background work lives and dies with the `styx mcp` process, and
losses are reported loudly, never silently.

**Non-goals (explicitly out of scope):**
- **B2 worktree isolation** for parallel edit-risk dispatches on one repo —
  gated on B1 proving out, as the roadmap decided. B1's answer to the write
  clash is a queue (below).
- **Detached runner processes** that survive session end — rejected as a
  daemon in denial (orphan/PID management, cross-process budget coordination,
  breaks "state is files + no daemons").
- **`risk=ship` in background** — the ship-gate handshake is interactive by
  design; rejected at spawn with a clear error.
- **MCP push notifications as the completion channel** — hosts do not
  re-prompt the model on notifications; a notification the conductor never
  reads is not a completion signal. Piggyback + collect instead.

## Decisions (from design review, 2026-07-07)

1. Completion model: **piggyback + collect** — every conductor tool result
   carries a compact background-status line whenever tasks exist, plus an
   explicit `collect` tool for results. Any tool activity resurfaces
   finished work; the model cannot forget for long.
2. Edit-risk collision on one project: **queue the second** (per-project
   write lock), never parallel writes, never a hard error.
3. Outcome capture: **every dispatch completion (sync or background)
   appends an outcome row** — the shared substrate with the self-improvement
   spec (2026-07-07-styx-self-improvement-design.md).

## Design

### Tool surface

- `dispatch` gains `background: true` (boolean, default false — synchronous
  behavior is unchanged). Spawn-time work happens before returning:
  project/thread resolution, budget + circuit-breaker check (existing
  `Router`/`Tracker` seams), ship-gate rejection for `risk=ship`. Immediate
  return: `{task_id, thread, cli, status: "running"|"queued"}`.
- New `collect` tool:
  - `collect({task_id})` → the finished task's full result (same shape as a
    sync dispatch result, plus `status: "done"|"error"|"orphaned"`), or
    `{status: "running"|"queued", elapsed_s, queued_behind?}` if unfinished.
    Collecting a finished task marks it claimed.
  - `collect({})` → all done-unclaimed results, plus one-line summaries of
    running/queued tasks.
- `thread_status` gains task rows: id, thread, cli, state, elapsed.

### Piggyback

Conductor tool registration gains one decoration point that wraps every tool
handler's result map: when the registry holds any live or unclaimed task,
the result gains a `"bg"` string field, e.g.
`"bg": "t3 running (codex, 4m); t2 done unclaimed — call collect"`.
No new wire machinery; the JSON-RPC transport is untouched.

### Registry, cap, and ordering

- In-memory `taskRegistry` on the conductor deps: mutex-guarded map of task
  structs (id, project, thread, cli, model, risk, state
  queued|running|done|error|orphaned, created/started/finished timestamps,
  result map, error text). Task ids are short and monotonic (`t1`, `t2`…)
  within a server lifetime; state files carry a collision-free run id.
- **Global concurrency cap 4**, seeded as a config knob (routing.toml
  `[conductor]` section via the existing seeded-default + upgrade path).
  Over-cap tasks sit `queued`.
- **Ordering rule 1 — per-thread serialization:** two dispatches to the same
  thread never run concurrently (session resume is stateful).
- **Ordering rule 2 — per-project write queue:** an edit-risk task waits for
  any running edit-risk task on the same project. Its status shows
  `queued behind <task-id>` so waiting is never mysterious. Read-risk tasks
  run freely in parallel.

### Lifecycle and crash honesty

- Background goroutines derive their context from the **server's root
  context**, not the originating tool call — they survive the call
  returning and die when the server dies. Every subprocess keeps its
  timeout/interruptible context (house rule).
- Per-task state mirrored to `~/.config/styx/state/tasks/<run-id>.json`
  (atomic tmp+rename) at spawn, completion, and claim.
- At server start, any state file still marked `running` from a dead server
  flips to `orphaned`; orphans are reported in the first piggyback line and
  by `collect`. Claimed files older than ~7 days are pruned at startup.
- Background tasks write nothing to stdout mid-flight (JSON-RPC stays
  clean); narration goes to stderr via `logStatus`.
- On completion the goroutine performs the same bookkeeping as a sync
  dispatch (budget event, outcome row) — the post-dispatch bookkeeping is
  extracted into one function shared by both paths.

### Outcome records (shared joint with self-improvement)

Every dispatch completion appends one row to a new append-only `outcomes`
table in the existing budget sqlite: timestamp, project, thread, cli, model,
routing signals, risk, duration_s, tokens in/out, classified error label
(empty on success), background flag, nullable rating + note.

New `rate_dispatch` tool: `{thread_or_task, ok: bool, note?}` stamps a
rating onto the most recent matching outcome row (the one sanctioned
mutation of the table). Guidance tells the conductor to rate only notably
good or bad outcomes — not every dispatch.

### Guidance

Conductor guidance (bump via the existing seedVN upgrade mechanic) teaches:
fire independent work with `background: true`, keep talking, `collect`
before synthesizing or when the user asks; rate notable outcomes via
`rate_dispatch`.

### Errors

- Spawn-time failures are synchronous errors: budget exhausted / circuit
  open, unknown project (lists the registry), ship-in-background.
- Runtime failures become the task's result with the classified channel
  error — collectable, never swallowed.
- Queue starvation is visible: piggyback and `collect` show queue position
  and elapsed wait.

## Testing

- Registry unit tests with fakes: cap enforcement, both ordering rules,
  orphan scan, claim/prune.
- `testdata/fakeagent` gains a `FAKEAGENT_SLEEP` knob; conductor-level tests
  drive a real background → piggyback → collect roundtrip.
- One new e2e test in the existing harness (`e2e/`) performs that roundtrip
  over JSON-RPC against a real `styx mcp` subprocess.
- Drift contract: ARCHITECTURE.md (conductor tools, MCP server, on-disk
  layout for `state/tasks/`), README verb table untouched (no new verbs),
  capability cards untouched.
