---
topic: styx MCP brain v2 — the unique-to-styx surface (channel-health, task-fit floor routing, project knowledge)
status: design-approved
created: 2026-07-01
related: 2026-06-29-styx-mcp-routing-brain-design.md, free-tier-tracker.md
supersedes_in_part: none
---

# styx MCP Brain v2 — the unique-to-styx surface

## Summary

v1 (`specs/2026-06-29-styx-mcp-routing-brain-design.md`) exposed styx's routing +
budget brain over MCP with three tools — `route`, `budget_status`, `record_usage`.
The v1 spec framed exactly two things a generic agent runtime like OpenClaw
**structurally cannot do**: (1) budget-aware, task-fit agent selection, and
(2) durable per-project knowledge. v1 claimed the first. v2 finishes the first
(it was surfaced too shallowly) and claims the second.

Concretely, v2 adds four things to the MCP surface, in two phases:

- **Phase 1 (sharpen the brain — small).** `route` v2 exposes styx's own
  task→signal classifier and a **task-fit capability floor** so a complex task is
  never silently degraded to a model that cannot do it; and a new `channel_health`
  tool lets a consumer avoid a flaky provider *before* dispatch.
- **Phase 2 (claim the knowledge half — medium).** `get_intel` / `refresh_intel`
  expose the per-project codebase intelligence index, and `recall` exposes
  semantic long-term memory — knowledge styx already persists on disk that a
  consumer would otherwise re-derive from scratch every session.

Everything here **extends** the v1 server. No provider SDKs, no new state stores:
each tool reads existing on-disk data (the append-only usage log, the intel index,
the memory sqlite). One small `internal/router` change is required to make the
capability floor real (and it fixes a latent bug — see §3).

## Baseline, scope, and branch

- **Baseline.** v1 (`route`/`budget_status`/`record_usage`) is merged to `main`
  first; v2 branches off `main` as `feature/styx-mcp-brain-v2`. Rationale: a clean
  base means v2 is developed and tested against a real, merged MCP server rather
  than stacked on unreviewed work.
- **In scope.** `route` v2 (signal classification + tier plan + capability floor +
  loud budget-block), `channel_health`, `get_intel` + `refresh_intel`, `recall`,
  the router floor/refusal change, and the drift-contract doc updates.
- **Deferred (unchanged from v1 or newly parked):**
  - **Parallel / multi-agent routing output** (`Parallel` + `SynthesizeWith`). The
    router already computes it, but OpenClaw runs one agent per session and has no
    synthesis primitive, so advisory `parallel_targets` would be a contract nobody
    can act on. Its natural home is the future **`run`** tool, where styx executes
    the fan-out itself (styx's `review` verb already does claude+codex→synthesize
    internally). Parked next to `run`, not lost.
  - **`run` execution tool** (styx's hands over MCP) — still deferred per v1;
    overlaps OpenClaw's own CLI runner.
  - **`reconcile`** (record_usage drift intelligence beyond the `Stale` bool),
    **research/brief tools**, **thread-continuity resource** — real but lower
    leverage; revisit after v2.
  - **Remote HTTP/SSE transport** — stdio only, single-machine (unchanged).

## Phase 1 — sharpen the brain

### 1. `route` v2 — task-fit floor, exposed classifier, loud budget-block

**Input (unchanged from v1):** `{ task: string (required), verb?: string,
signals?: string[], project?: string }`. New behavior: when `signals` is omitted,
styx runs its **own** signal classifier over `task` (+ `verb`/`project`) rather
than requiring the consumer to hand-tag. This exposes the task→signal mapping that
is the task-fit half of the brain and was hidden in v1 (the consumer could only
pass signals *in*).

**Output — additive over v1.** v1 consumers ignore the new fields; nothing in the
v1 output is renamed or removed.

```jsonc
{
  // --- v1 fields (unchanged) ---
  "channel": "claude",
  "model": "sonnet",
  "effort": "high",
  "fallback_chain": ["codex", "claude:opus"],
  "reasoning": "…",
  "budget": { /* BudgetSnapshot */ },
  "degraded": true,

  // --- v2 additive fields ---
  "classified_signals": ["complex", "lang:go"],   // styx's own task→signal tags
  "floor": "claude:sonnet",                         // MINIMUM tier the task requires
  "tier_plan": {
    "acceptable": ["claude:opus", "claude:sonnet", "codex"], // all clear the floor
    "chosen":     "claude:sonnet",                            // cheapest acceptable within budget
    "escalate_to": "claude:opus"                              // next step up if the cheaper tier proves insufficient
  },
  "blocked_by_budget": true,    // true ONLY when every >=floor channel is over-cap
  "retry_after_s": 7200
}
```

**Semantics (the core of this spec).** Task-fit sets the band; budget chooses
within it; the floor is never crossed silently.

1. **The classifier-derived signals set an explicit capability floor.** `complex`
   / `deep` ⇒ the floor is a capable tier (e.g. `>= claude:sonnet`-class);
   below-floor channels (`ollama`, cheaper tiers) are *excluded from the
   acceptable set*, not merely deprioritized. A trivial task has a low floor and a
   wide acceptable set.
2. **`tier_plan.chosen` is the cheapest channel in `acceptable` that is within
   budget** — task-fit first, cost second (this is the validated "justify on
   task-fit, not cost" routing principle made mechanical).
3. **At the floor, degrade loud — never below it, never a silent lie.** When every
   channel in `acceptable` is over-cap or circuit-open:
   - `chosen` = the floor tier (the cheapest acceptable channel, even though it is
     over budget) — a concrete recommendation, not a null,
   - `degraded = true`, `blocked_by_budget = true`,
   - `reasoning` states the block ("task is complex; all capable channels over
     weekly cap"), and `retry_after_s` gives the earliest window when the floor
     tier frees up.
   The consumer (OpenClaw or, through it, the user) decides: wait, override, or
   proceed and accept the likely 429. styx never returns a below-floor channel and
   never returns an over-cap channel *as if it were fine*.

This directly answers "what if a task needs a complex workflow a lower tier can't
handle": such a task gets a guaranteed floor and will never be silently handed to
a model that cannot do it — styx either picks the cheapest *capable* model within
budget, or blocks loud at the floor with a retry hint.

### 2. `channel_health` — proactive circuit-breaker query

New tool. Lets a consumer avoid a provider that is currently erroring *before*
dispatch, instead of only learning reactively via `route` degradation after a
failed call.

- **Input:** `{ channel?: string }` (omit → all channels).
- **Output (per channel):**
  ```jsonc
  {
    "channel": "claude",
    "circuit_open": false,
    "failures_recent": 1,
    "window_s": 600,
    "error_kinds": { "timeout": 0, "rate_limit": 1, "server": 0 },
    "cooldown_remaining_s": 0
  }
  ```
- **Implementation:** a pure read over the existing append-only usage log +
  `ShouldCircuitBreak` (failure count over the breaker window). `error_kinds`
  buckets the classified error labels already recorded per row. No new state.

## Phase 2 — claim the knowledge half

### 3. `get_intel` + `refresh_intel` — per-project codebase intelligence

- **`get_intel(project, section?)`** returns the per-project intel index that styx
  already builds and auto-refreshes: file tree, module summaries
  (purpose / entry-points / dependencies), detected conventions (test framework,
  type system), key symbols with centrality rationale, recent commits, open TODOs,
  git HEAD. `section?` (e.g. `"conventions"`, `"key_symbols"`, `"modules"`) returns
  just that slice so a consumer can control injected context size; omitted →
  the whole index.
  - Returns `stale: bool` + `staleness_reason` (the existing freshness rule:
    `> 5` commits since build or `> 7` days old). It does **not** auto-rebuild on
    read — a read never blocks on an index rebuild.
- **`refresh_intel(project)`** triggers an explicit rebuild of the index and
  returns the fresh result. This is the deliberate write/refresh path.

**Surface decision:** intel is exposed as a **tool** (`get_intel`), not an MCP
*resource*. Rationale: universal MCP-host support, `section` filtering, and
consistency with the other tool-shaped surfaces. Exposing the index additionally
as a read-only resource (for hosts that prefer file-like context attachment) is a
clean later add, not required for v2.

### 4. `recall` — semantic long-term memory

- **`recall(project, query, k?)`** returns the top-`k` (default a small constant)
  project-scoped memory items — decisions, facts, preferences, distillations —
  via Ollama-embedding cosine recall with recency/confidence decay (the existing
  memory store). Lets a consumer ask "what do we already know / what was decided
  here?" instead of cold-starting.
- **Degrade loud:** `recall` requires local Ollama embeddings. If Ollama is
  unavailable, `recall` returns a **classified error** ("recall unavailable:
  ollama embeddings down"), never an empty result presented as success.

## The router change (required for §1)

Today `internal/router` encodes the capability floor only **implicitly**, in each
rule's hand-authored fallback chain (e.g. `plan`+`complex` → `claude:opus`,
fallback `[claude:sonnet, codex]` — deliberately never `ollama`). Two problems this
spec fixes:

1. **The floor is convention, not enforcement.** Nothing stops a rule from listing
   a below-floor channel in a complex chain; the router would degrade there.
2. **Chain-exhaustion returns a lie (latent bug, `router.go` ~120–130).** When the
   primary *and every fallback* are unavailable, the degradation loop finds nothing
   available, so `chosen` stays equal to the **over-cap primary** and the router
   returns it with `degraded = true`. It never actually refuses — a consumer that
   trusts `degraded` and runs it gets a 429.

**Change.** Derive an explicit floor from the request's signals; restrict the
acceptable set to channels that clear the floor; choose the cheapest acceptable
that is available/in-budget; and when the acceptable set is exhausted, mark the
decision `blocked_by_budget = true` with the floor tier as `chosen` and a
`retry_after_s`, rather than returning an over-cap primary as if it were fine. The
new `Decision` fields (`ClassifiedSignals`, `Floor`, `TierPlan`,
`BlockedByBudget`, `RetryAfterS`) are additive; the existing `styx route --explain`
and REPL paths keep working (they render the richer decision, unchanged in the
non-blocked case).

The floor mapping (which signals imply which minimum tier) lives next to the
existing signal definitions so it is table-driven and testable, not scattered.

## Cross-cutting

- **Project resolution.** `get_intel` / `refresh_intel` / `recall` require a
  project; `route` / `channel_health` take it optionally. All resolution reuses the
  `internal/target` resolver (registered alias *or* path), so behavior matches the
  CLI. An unknown project is a **classified error, never a silent fallback** to the
  cwd or a default.
- **Errors.** Every tool error surfaces as a classified MCP tool error (reuse
  `channel.ClassifiedError` labels where relevant); malformed tool input →
  structured validation error, not a panic; nothing swallowed (`x, _ :=` forbidden
  per CLAUDE.md).
- **No new state / no SDKs.** v2 adds an MCP tool shell over existing subsystems
  (router, budget, intel, memory). It adds an MCP server-side library only — not an
  Anthropic/OpenAI API client — so the "CLIs/HTTP, never SDKs" rule is intact.
- **Backward compatibility.** `route` output is additive; v1 consumers and the
  OpenClaw registration from the v1 doc keep working with no changes.

## Testing (TDD)

- **Router (`internal/router`).** Table-driven `t.Run` cases for: floor derivation
  per signal set; cheapest-acceptable-within-budget selection; **chain-exhaustion
  now blocks loud** (asserts `BlockedByBudget`, floor `chosen`, `retry_after_s` —
  the bug-fix regression guard); trivial task keeps a wide acceptable set.
- **MCP server (`internal/mcpserver`).** In-memory MCP client harness over a stdio
  pipe (mirrors the v1 tests) for each new tool/field: `route` v2 additive fields
  including the blocked path; `channel_health` all-channels and single-channel,
  including a circuit-open channel; `get_intel` whole + `section` + `stale` flag;
  `refresh_intel` rebuild; `recall` normal hit and the ollama-down loud error;
  unknown-project classified error for the project-scoped tools.
- **Reuse existing fakes** (router/budget/intel/memory); no live network. Server
  layer is tested as an adapter (does it map MCP calls onto the subsystems); the
  underlying logic is already covered by each subsystem's own tests.

## Docs (drift contract — same commit as code)

- `docs/ARCHITECTURE.md` (owns `cmd/styx/**`, `internal/**`): extend the
  `mcpserver` section with the four new tools; add the router **capability-floor**
  behavior to the Router section; bump `last_verified`.
- `README.md`: document the new MCP tools alongside the v1 `styx mcp` entry.
- `free-tier-tracker.md`: note v2 as the follow-on to topic #7.

## Success criteria

1. `route` on a **complex** task returns `floor` and a `tier_plan` whose `chosen`
   clears the floor; it is **never** a below-floor channel.
2. When every capable channel is over-cap, `route` returns `blocked_by_budget=true`
   with the floor tier and a `retry_after_s` — and the router unit test proves the
   old chain-exhaustion "return the over-cap primary silently" path is gone.
3. `route` with no `signals` returns styx-`classified_signals` derived from `task`.
4. `channel_health` reports circuit-open / cooldown / error-kind buckets per
   channel from the existing usage log, with no new state.
5. `get_intel(project)` returns the index (and honors `section`), flags staleness,
   and never rebuilds on read; `refresh_intel(project)` rebuilds.
6. `recall(project, query)` returns decayed top-k memory; ollama-down yields a loud
   classified error, never empty-as-success.
7. Unknown `project` on any project-scoped tool is a classified error.
8. `make test` green; v1 output and OpenClaw registration unchanged;
   `ARCHITECTURE.md` + `README.md` + tracker updated in the same commits as code.
