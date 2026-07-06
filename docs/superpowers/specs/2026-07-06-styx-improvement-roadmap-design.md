---
topic: styx improvement roadmap — reliability-first hardening (Phase A), then async dispatch (Phase B)
status: design-approved
created: 2026-07-06
related: 2026-07-01-styx-mcp-brain-v2-design.md, 2026-06-29-styx-mcp-routing-brain-design.md, 2026-06-12-styx-repl-orchestrator-design.md
supersedes_in_part: none
---

# Styx improvement roadmap — Phase A (reliability & drift), Phase B (async dispatch)

## Summary

Styx's real problem is adoption: it often isn't launched at all, and when it is,
the conductor either can't use the toolbelt (first-contact tool calls fail) or
abandons it (dispatch blocks silently for minutes). Live E2E testing on
2026-07-06 reproduced every reported failure mode and found mechanical causes
for each. Separately, web research confirmed styx's capability model of its own
channels has drifted badly stale (codex native resume + exact usage, claude 1M
windows + `--bare`, fable live, agy partial resume), and that the ecosystem has
converged on async task-handle dispatch — the one architectural gap styx has.

The roadmap is two phases, strictly sequenced:

- **Phase A — reliability & drift.** Make a naive conductor's first contact
  with every MCP tool succeed; fix the stale channel assumptions that are
  silently destroying dispatch quality; add a permanent E2E harness so this
  regression class is caught by machine, not by lost user trust. All
  afternoon-sized diffs, each independently shippable.
- **Phase B — async dispatch.** `dispatch(background: true)` returns a task id
  immediately; work runs in goroutines inside the already-long-lived `styx mcp`
  process; a new `collect` tool retrieves results. Built only after A, because
  a conductor that has learned the toolbelt is broken will not call a better
  dispatch.

North star: `styx` must be the obviously-better way to start a session. Today
that means first contact succeeds; next it means parallel cross-CLI dispatch
that bare Claude Code cannot do.

## Evidence (E2E, 2026-07-06, this machine)

Reproduced by driving `./bin/styx mcp` with raw JSON-RPC exactly as Claude
Code would:

1. **`dispatch(cli=ollama, message, risk)` → `ollama 400: model is required`.**
   The one-shot path has no default model. A conductor's most natural call
   fails; it learns the toolbelt is broken. (`cmd/styx/mcp_conductor.go`)
2. **`dispatch(cli=claude, ...)` without `project` → error text written for a
   human at a shell**: "name a project (--project), pass --dir, or cd into a
   repo". An MCP client can do none of those. Inconsistent surface:
   `pipeline_run` resolves via the server's cwd; `dispatch`/`thread_status`/
   `memory_save` refuse to. The launch guidance never states the focus
   project's registry alias, so the conductor cannot know what to pass.
3. **Silent multi-minute blocking.** No progress narration of any kind during
   a dispatch (ollama one-shots narrate to stderr; thread dispatches emit
   nothing). Claude Code stdio MCP calls have no idle timeout (~28h wall
   budget), so a long dispatch is indistinguishable from a hang. This is the
   reported "dispatch hangs" experience.
4. **48,551 input tokens to answer "pong".** A minimal `dispatch(cli=claude)`
   turn boots the full Claude Code session: hooks, CLAUDE.md, skills, MCP
   autodiscovery — including styx's own MCP server, recursively. Claude Code's
   `--bare` flag exists to skip exactly this and is not passed.
5. **Premature distill-and-restart.** `thread_status` reported "context 24%"
   after that single trivial turn — `ClaudeAdapter.ContextWindow()` returns
   200,000, but opus 4.8 / sonnet 5 / fable 5 run a 1M window on the API and
   on Max plans (opt-out env: `CLAUDE_CODE_DISABLE_1M_CONTEXT`). Threads
   therefore distill at ~14% of real capacity — a mechanical cause of the
   reported context-decay / quality problem.
6. **`thread_status` returns `{"threads": null}`** (not `[]`) when a project
   has no threads.
7. **`styx version` / `--version` do not exist**; unknown tokens in a non-TTY
   context fall through toward the conductor launch path and die inside
   claude with a misleading error.
8. **Doctor/adapter drift**: `styx doctor` reports codex "native resume" (card
   is current) while `internal/agent`'s codex adapter still treats codex as a
   stateless no-resume CLI with len/4 token estimates and lossy rolling
   summaries.

What worked: the MCP protocol layer itself (initialize, tools/list, all
read-path tools), routing decisions, budget snapshots, ollama one-shot with
explicit model (5s), claude thread dispatch with explicit project (correct
result, session captured, usage recorded).

## Research findings that shape the design (July 2026)

Full citations live in the research transcripts; load-bearing facts:

- **Codex CLI**: `codex exec resume <thread_id>` / `--last` is Stable and
  documented; `--output-schema` works on resume; `codex exec --json` emits
  JSONL events where `turn.completed` carries exact
  `usage {input_tokens, cached_input_tokens, output_tokens,
  reasoning_output_tokens}`; `thread.started` carries `thread_id`. Default
  model GPT-5.5 (400K context in Codex). `--full-auto` is deprecated.
- **Claude Code CLI**: 1M windows as above; `--bare` (skips hooks/skills/
  plugins/MCP/CLAUDE.md; Anthropic says it will become the `-p` default);
  `--fork-session`; `--effort` now includes `xhigh`/`max`; `fable` is a live
  alias for Claude Fable 5 (safety classifiers can silently fall requests
  back to opus — treat fable as usable, with fallback semantics noted).
  Claude Code's MCP client supports elicitation, progress notifications, and
  `list_changed`, but NOT sampling and NOT the MCP Tasks primitive.
- **agy**: `-c/--continue` and `--conversation <id>` exist, but conversation
  IDs are never surfaced in `--print` output (open upstream issue #7), so
  headless multi-thread resume is impossible today. Styx's rolling-summary
  continuity for agy remains correct. In `-p` mode agy auto-approves all tool
  calls, making `--dangerously-skip-permissions` largely redundant there.
- **Ollama**: `keep_alive` per request (`-1` = resident) + preload-on-start
  eliminates 3–10s cold loads; default context is 4096 tokens unless
  `num_ctx` is set — the brain's ~40-exemplar prompt may be near or over
  this; `/api/embed` accepts batched input (~95ms batched vs ~3.3s
  sequential in one benchmark); flash attention is now automatic; MLX backend
  (0.19+) needs ≥32GB unified memory.
- **Async consensus**: MCP's 2026 spec direction is task handles
  ("call-now, fetch-later"); Claude Code subagents became
  background-by-default (v2.1.198); Anthropic's multi-agent research post
  names serial sub-agent execution as their main bottleneck; OpenHands ships
  both a blocking and a delegating primitive. Since Claude Code's client
  lacks the Tasks primitive, a server-side hand-rolled handle triad is the
  correct 2026 implementation, and the stateless-core spec direction blesses
  passing task/thread handles as ordinary tool args.
- **Sub-agent briefing recipe** (Anthropic): objective, output format,
  tool/source guidance, explicit boundaries. Belongs in guidance for dispatch
  messages.
- **Compaction research** (JetBrains NeurIPS 2025; Anthropic harness post):
  observation-masking beats LLM summarization on SWE-bench trajectories;
  Anthropic dropped context-resets entirely on Opus 4.5+. Distill-and-restart
  is the right *safety net* for weaker channels but must not fire at 14% of a
  1M window.

## Phase A — reliability & drift

### A1. First-contact dispatch ergonomics (`cmd/styx/mcp_conductor.go`)

- `dispatch(cli=ollama)` with empty `model` defaults to the routing table's
  ollama target (resolution order: `[brain]` model → routing default rule →
  `qwen2.5-coder:7b` constant), never a 400.
- `dispatch`, `thread_status`, and `memory_save` gain the same cwd fallback
  `pipeline_run` already has: empty `project` resolves via
  `resolveGlobalTarget("")` (the launcher starts `styx mcp` in the project
  dir, so cwd is the caller's project in the conductor case). The
  architecture doc's "no cwd fallback" contract is revised: strict resolution
  applies to *named* projects (unknown alias is still a loud error); empty
  now means "the launch project". Narrate the fallback via `logStatus`.
- Error messages become MCP-appropriate: on unresolvable project, list the
  registered project names (`unknown project ""; registered: styx, ai-ta, …`)
  instead of shell advice.
- `launchConductor` appends one guidance line naming the focus project:
  "This session's project alias is `<name>`; pass it as `project` on
  dispatch/thread_status/memory_save." (belt-and-suspenders with the cwd
  fallback; costs one line).
- `thread_status` returns `{"threads": []}`, never `null` (initialize the
  slice; audit other conductor tools for the same nil-slice wart).

### A2. Channel capability drift (`internal/agent`, `internal/channel`, cards)

- **Claude context window**: `ClaudeAdapter.ContextWindow()` becomes
  model-aware — 1,000,000 for opus/sonnet/fable class aliases (current
  Anthropic API + Max-plan behavior), 200,000 retained for haiku and as the
  conservative unknown-model default. Honor
  `CLAUDE_CODE_DISABLE_1M_CONTEXT=1` in the environment by keeping 200k.
  The distill threshold stays 70% but now measures the real window.
- **`--bare` on dispatched claude turns**: the agent adapter's headless args
  gain `--bare` (hooks, skills, CLAUDE.md, and MCP autodiscovery are the
  *conductor session's* job, not the dispatched thread's; this also stops the
  recursive styx-MCP load and pre-adapts to Anthropic flipping `--bare`
  default-on for `-p`). Intel context injection, when wanted, is explicit:
  the dispatch message/system seed, not ambient session bootstrap.
  Verify `--bare` coexists with `--resume` + `stream-json` in the adapter
  test against a scripted fakeagent and one live smoke run.
- **Codex becomes a resume-capable adapter**: capture `thread_id` from
  `codex exec --json`'s `thread.started` event; resume turns run
  `codex exec resume <thread_id>`; parse `turn.completed.usage` for exact
  token accounting (replacing len/4 estimates); dead-session recovery mirrors
  the claude path (clear ID, reseed from last distillation). Rolling
  summaries remain only as the distill-handoff text, not per-turn continuity.
  `SupportsResume()` returns true; the codex capability card and doctor
  probes update to match (doctor already detects native resume — the adapter
  now uses it).
- **Fable un-suspended**: `[tiers]` maps `fable` to `fable` (seeded config +
  `default_routing.go` + upgrade path per the drift contract). Note in the
  card: safety classifiers may transparently serve opus for flagged
  requests — no styx handling needed beyond documentation.
- **Effort passthrough**: no validation change needed (styx already passes
  strings through); document `xhigh`/`max` as valid claude values in
  routing.toml comments.
- **agy unchanged functionally** (rolling summaries stay — IDs aren't
  capturable headlessly; upstream issue google-antigravity/antigravity-cli#7
  noted in the card so doctor drift-probes catch it when it lands).
- **Ollama latency + correctness**:
  - `keep_alive: "30m"` on brain and embedder requests; fire a preload ping
    when a REPL/conductor session opens (overlaps model load with user
    typing).
  - Set `num_ctx` explicitly on brain calls (size to the rendered prompt;
    audit whether the ~40-exemplar preamble already exceeds the 4096
    default — if it does, this is a live routing-accuracy bug and gets a
    regression test).
  - Batch memory/intel embedding calls through `/api/embed`'s array input.

### A3. Dispatch observability

- The MCP server emits `notifications/progress` during dispatch when the
  client supplied a `progressToken` (Claude Code consumes these); stderr
  `logStatus` stage narration accompanies it (started / streaming / n tokens /
  elapsed). `internal/mcpserver` grows minimal notification support —
  transport-only, stdlib JSON, no SDK.
- Dispatch results gain `duration_s` and `model` fields (additive).
- `styx version` verb + `--version` flag (print version, exit 0). Bare
  `styx`/launch on a non-TTY stdin errors with a clear message instead of
  exec'ing claude into a cryptic `--print` failure.

### A4. E2E harness (`e2e/` package, build-tagged; `make e2e`)

- A Go test that builds `./bin/styx`, spawns `styx mcp` as a subprocess with
  `testdata/fakeagent`-backed CLIs and an httptest ollama, and drives the
  exact JSON-RPC sequences a conductor performs. Asserts the A1–A3 contracts:
  tools/list shape, ollama default-model dispatch, empty-project cwd
  fallback, unknown-project error text (lists registry), `[]`-not-`null`,
  progress notifications present, dispatch result fields.
- `make e2e` runs it hermetically (no quota, CI-able).
- `STYX_E2E_LIVE=1` extends it: real `styx doctor`, one ollama dispatch, one
  minimal `--bare` claude dispatch with usage assertions. Opt-in, run on this
  machine before releases.
- Docker is deliberately not used: styx's substance is subscription-authed
  CLIs, Keychain, and local ollama — none available in a container. Hermetic
  fakes + host-live smoke is the honest equivalent.

### Phase A acceptance

A fresh `styx` launch in a registered repo, followed by the conductor's most
naive tool sequence — `route`, `budget_status`, `dispatch(cli=ollama,message)`,
`dispatch(cli=claude,message)`, `thread_status()` — succeeds end-to-end with
zero required-but-undocumented arguments, visible progress, honest context
percentages, and codex threads that remember their previous turn. `make e2e`
is green and gates commits touching the MCP surface.

## Phase B — async dispatch

Built strictly after A ships. Design (detail deferred to its own plan):

- `dispatch` gains optional `background: true` → immediately returns
  `{task_id, thread, cli, status: "running"}`. Work runs in a goroutine inside
  the `styx mcp` process (which lives for the whole conductor session — no
  daemon violation). Default remains synchronous.
- New `collect(task_id?)` tool: with an id, returns the finished result (or
  `{status: running, elapsed_s}`); without, returns all completed-unclaimed
  results plus running-task statuses. `thread_status` lists task rows.
- Guidance teaches the pattern: fire background dispatches for independent
  work, keep talking, `collect` before synthesizing or when the user asks.
- Concurrency cap (default 4); budget/circuit check at spawn time (reuse
  `Router`/`Tracker` seams); per-task state mirrored to
  `~/.config/styx/state/tasks/<run-id>.json` (tmp+rename) so a crashed server
  reports losses honestly rather than silently dropping work.
- `risk=ship` never runs in background (ship-gate handshake is interactive by
  design).
- **B2 (conditional on B1 proving out)**: worktree isolation for parallel
  write-risk dispatches against the same repo (`git worktree` per task,
  merge-back flow), following the claude-squad/Claude Code consensus pattern.

## Explicitly deferred (backlog, with reasons)

- **Budget truth** — parse host CLI session logs (`~/.claude`, `~/.codex`) to
  mirror real subscription usage; timely given Anthropic's 2026 third-party
  subscription-quota policy, but it's measurement, not function. (Tracked in
  memory: budget-tracks-subscriptions.)
- **Brain upgrades** — embedding-kNN hybrid router with per-route thresholds
  (research: production embedding routers hit 92–96% at 16–100ms; hybrid
  cascade ≈ LLM accuracy at half latency), Qwen3.5-4B swap, semantic routing
  cache. Deferred because conductor-first demotes the 7b brain to
  one-shots/REPL. The `num_ctx` audit in A2 is the only brain work in scope.
- **Memory vault** — Obsidian-compatible markdown export of memory stores;
  good demo, not a reliability lever.
- **Distill tuning** — observation-masking before full distillation
  (JetBrains), per-model thresholds; mostly mooted for claude by honest 1M
  windows, still relevant for codex/agy later.
- **Riding host parallelism (rejected)** — leaning on Claude Code agent
  teams/background subagents instead of styx dispatch would cede cross-CLI
  quota arbitrage, styx's core value, and burn claude quota invisibly.

## Testing

- Every A1–A3 behavior lands with table-driven unit tests (existing fakes:
  `testdata/fakeagent`, httptest ollama, in-memory budget stubs).
- The A4 E2E harness is the integration gate; live mode is release-gate.
- Codex adapter resume/usage parsing gets fakeagent scripts emitting the
  documented JSONL events (`thread.started`, `turn.completed` with usage).
- Drift contract: `docs/ARCHITECTURE.md` (channels, agent threads, conductor
  tools, MCP server sections), capability cards, and README verb table update
  in the same commits as the code they describe.
