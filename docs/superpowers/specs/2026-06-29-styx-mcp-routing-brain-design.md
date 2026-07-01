---
topic: styx as a standalone MCP routing brain (OpenClaw as consumer)
status: design-approved
created: 2026-06-29
supersedes_in_part: free-tier topics #2 (remote trigger) and #6 (CI offload) — dropped
related: free-tier-tracker.md
---

# styx as a Standalone MCP Routing Brain

## Summary

styx exposes its **routing + budget brain** over the Model Context Protocol so
other agent runtimes — first and foremost **OpenClaw** — can ask styx *which* AI
coding agent to use for a task and *whether the user's subscription budget allows
it*. styx stays a full standalone CLI (it keeps its "hands": `styx auto`,
`research`, execution all unchanged); this adds a brain-API on the side.

The shape of the integration is deliberate: **styx is the component, OpenClaw is
the consumer.** OpenClaw calls styx, never the reverse. styx works with or without
OpenClaw, which keeps it decoupled from a young, fast-moving dependency.

This is the **primary priority** of the current free-tier effort. Building it
drops two previously-spec'd topics as redundant (see *Scope*).

## Why this pivot (background)

A spike on OpenClaw (2026-06-29) established three load-bearing facts:

1. **OpenClaw consumes external MCP servers.** Register one in
   `~/.openclaw/openclaw.json` under `mcpServers` (stdio, or remote
   `streamable-http`/`sse`), restart the gateway, and its tools become callable by
   any OpenClaw agent. So styx-as-MCP-server plugs straight in.
2. **OpenClaw has no budget-aware, task-fit agent selection.** Its "routing" is
   session→agent isolation plus auth-profile rotation and error-based fallback;
   for cost it tells users to "set spending alerts at the provider level." It does
   *not* decide "use Codex for this implement task, but you're near your 5h Claude
   cap, so degrade." **That decision is exactly styx's core** — so styx fills a
   real gap rather than duplicating OpenClaw.
3. **OpenClaw can drive the CLIs on subscription auth (the styx way),** via a
   plugin that runs the genuine `claude`/`codex` CLIs as subprocesses on the
   user's existing OAuth login ("OpenClaw never touches the API directly"). The
   caveat: Anthropic is actively tightening which tools count as subscription
   quota (first-party CLI/apps in, third-party tools out), and the policy is in
   flux. This is *why styx keeps its hands* — its own CLI execution is the hedge
   if OpenClaw's subscription path tightens.

## Goals

- A new `styx mcp` command that runs styx as a **stdio MCP server**.
- Three tools — `route`, `budget_status`, `record_usage` — that expose the
  existing `internal/router` and `internal/budget` packages over MCP. Minimal new
  logic; this is an adapter, not a new subsystem.
- Keep budget tracking accurate even when OpenClaw (not styx) runs the agent, via
  `record_usage`.
- Nothing in standalone styx is removed or regressed.
- Docs: a short "register styx in OpenClaw" guide; `ARCHITECTURE.md` + `README.md`
  updated in the same commit as the implementation (drift contract).

## Non-goals

- **No remote transport in v1.** Single-machine (topic #5 confirmed dropped), so
  stdio only. No HTTP/SSE, no tunnel, no auth tokens.
- **No `run`/execution tool in v1.** Exposing styx's hands over MCP (so OpenClaw
  hands styx the whole job) is the natural future extension, but it overlaps
  OpenClaw's own CLI runner. Deferred (see *Future*).
- **No daemon.** The MCP server is a foreground subprocess OpenClaw spawns and
  manages; styx itself starts no background process (consistent with CLAUDE.md
  "no daemons").
- **No provider SDKs.** This adds an *MCP server* library, not an Anthropic/OpenAI
  API client — the CLAUDE.md "CLIs/HTTP, never SDKs" rule targets provider clients
  and is not violated. Called out explicitly to preempt the drift hook.
- **Not a replacement for the REPL brain** or its routing classification.

## Architecture

```
OpenClaw gateway
   │  spawns subprocess: `styx mcp`  (stdio, JSON-RPC)
   ▼
styx mcp server  (new: cmd/styx/mcp.go + internal/mcpserver/)
   ├── tool: route          ──► internal/router   (first-match rules + budget degrade)
   ├── tool: budget_status  ──► internal/budget    (windows / cooldown / circuit-breaker)
   └── tool: record_usage   ──► internal/budget    (append-only usage log)
```

The MCP server is a thin protocol shell. `route` reuses the router (which already
consults the budget tracker to degrade down fallback chains); `budget_status` and
`record_usage` are direct reads/writes against the existing append-only sqlite
usage log. No routing or budget logic is reimplemented.

### Transport: stdio (decided)

OpenClaw runs on the same machine as styx, so the server speaks JSON-RPC over
stdio and OpenClaw spawns it as a managed child. Rationale: no tunnel, no auth,
no daemon, no new secrets — fits styx's files-on-disk / no-daemons / macOS-first
model. Remote HTTP/SSE is only needed cross-machine, which is out of scope.

### The three tools

**`route`** — the core decision.
- Input: `{ task: string (required), verb?: string, signals?: string[], project?: string }`
  (`verb`/`signals` mirror how routing.toml rules match today; `project` gives the
  router optional context.)
- Output: `{ channel: string, model?: string, fallback_chain: string[],
  reasoning: string, budget: BudgetSnapshot, degraded: bool }`.
  `reasoning` is the transparent explanation (parity with `styx route --explain`),
  so a consumer can show *why* a channel was chosen. `degraded` is true when the
  first-choice channel was skipped for budget/cooldown reasons.

**`budget_status`** — introspection.
- Input: `{ channel?: string }` (omit → all channels).
- Output: per-channel `BudgetSnapshot { window_5h: {used, limit}, weekly:
  {used, limit}, cooldown_remaining_s?: number, circuit_open: bool }`.

**`record_usage`** — keeps the brain honest (the decision we confirmed to include).
- Input: `{ channel: string (required), messages?: number, tokens?: number,
  task?: string, outcome?: string }`.
- Output: `{ recorded: bool, budget: BudgetSnapshot }`.
- Writes one row to the append-only usage log. **Without this, styx's budget half
  goes blind whenever OpenClaw runs the agent** — a budget-aware router that can't
  see usage is a guesser. With it, styx stays accurate even though OpenClaw is the
  hands. The "register styx in OpenClaw" doc instructs consumers to call
  `record_usage` after each run; if a consumer doesn't, budget degrades **loud**
  (a flagged staleness warning in `budget_status`), never silently.

## Error handling

- Tool errors surface as MCP tool errors with classified messages (reuse
  `channel.ClassifiedError` labels where relevant); never swallowed.
- If the budget DB is unavailable, `route` still returns a routing decision but
  marks `budget.circuit_open`/stale explicitly and degrades loud.
- Malformed tool input → a structured validation error, not a panic.

## Testing

- Table-driven tests (`t.Run`) per the repo standard.
- An in-memory MCP client harness drives the server over a stdio pipe; assert each
  tool's request→response, including the degraded-routing and stale-budget paths.
- Reuse existing router/budget fakes; no live network. The server layer is tested
  as an adapter (does it map MCP calls onto router/budget correctly), since the
  underlying logic is already covered by `internal/router` and `internal/budget`
  tests.

## OpenClaw integration (consumer side)

Documented snippet for `~/.openclaw/openclaw.json`:

```json
{
  "mcpServers": {
    "styx": { "command": "styx", "args": ["mcp"] }
  }
}
```

Restart the gateway; OpenClaw agents can now call `styx.route`,
`styx.budget_status`, and `styx.record_usage`. The "register styx" doc also
covers the `record_usage`-after-each-run convention.

## Scope

**In v1:** `styx mcp` (stdio) with `route` + `budget_status` + `record_usage`;
the OpenClaw registration doc; `ARCHITECTURE.md` + `README.md` updates.

**Dropped (redundant with OpenClaw):**
- **#2 remote trigger / `styx serve` + tunnel** — OpenClaw is the daemon/chat
  surface now.
- **#6 CI / remote compute offload** — OpenClaw covers remote execution.

**Deferred (revisit after v1):**
- **#3 web search** and **#4 secretary** — these *sharpen the brain*, so they stay
  on the roadmap; reconsider once the MCP surface is live.
- **#1 notifications** — largely covered by OpenClaw's own channels; revisit only
  if standalone styx still wants its own ping.

## Future (not now)

- A `run` tool exposing styx's hands over MCP, so OpenClaw can delegate a whole
  job to styx (styx routes + runs the CLI + tracks budget exactly — no
  `record_usage` cooperation needed). Deferred because it overlaps OpenClaw's own
  runner; revisit if the `record_usage` convention proves unreliable in practice.
- Remote HTTP/SSE transport, only if multi-machine ever becomes real.

## Risks / open questions

- **Subscription ToS flux.** Whether OpenClaw-driving-the-real-CLI stays within
  subscription quota is unsettled (same gray zone applies to styx itself). Keeping
  styx's hands is the hedge; no action needed in this spec beyond not deleting
  them.
- **`record_usage` cooperation.** styx can't force OpenClaw to report usage. v1
  mitigates with loud staleness; the `run`-tool future fully resolves it.
- **Go MCP library choice.** Use a maintained Go MCP server library (e.g. the
  official `modelcontextprotocol/go-sdk` or `mark3labs/mcp-go`) vs. hand-rolling
  JSON-RPC over stdio — decide in the implementation plan.

## Drift contract

On implementation: update `docs/ARCHITECTURE.md` (owns `cmd/styx/**`,
`internal/**`) and `README.md` (new `styx mcp` verb) in the same commit, and bump
`last_verified`. Update `free-tier-tracker.md` to mark #2/#6 dropped and add this
topic.
