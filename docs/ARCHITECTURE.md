---
owns:
  - "cmd/styx/**"
  - "internal/**"
  - "testdata/**"
  - "eval/**"
last_verified: 2026-06-15
---

# Styx Architecture

As-built architecture of the styx CLI. This doc is the authority on every file
matched by the `owns:` globs above — read the relevant section before editing
those files, and update it (plus `last_verified`) in the same commit as any
code change that alters behavior described here.

## System overview

Styx is a Go CLI that routes dev work between four AI channels — claude
(Anthropic CLI), codex (OpenAI CLI), agy (Google Antigravity CLI), and ollama
(local HTTP) — using a hand-curated rules table with budget-aware fallback.

```
argv ──► cmd/styx/main.go (global flags: --quiet --verbose)
              │ bare `styx` opens the REPL; otherwise ensureFirstRun():
              │ seed ~/.config/styx/routing.toml, v0.1→v0.2 upgrade
              ▼
        cmd/styx/dispatch.go
              │ no-app verbs (help, doctor, project, route, budget, check, runs, execute…)
              │ app verbs → loadApp(): routing.toml + budget.Tracker + router + channels
              ▼
        internal/router.Route(verb, signals) ──► Decision{channel, model, fallback…}
              │                                       ▲
              │                          internal/signals.Extract (pure tagger)
              ▼
        internal/channel (decorated: WithProgress wrapping WithTimeout/raw adapter)
              ├── channel/claude   exec `claude -p` / interactive
              ├── channel/codex    exec `codex exec`
              ├── channel/agy      exec `agy -p --dangerously-skip-permissions`
              └── channel/ollama   HTTP localhost:11434 (auto-launches the app)
```

Every send is recorded in the budget DB; routing degrades down each rule's
fallback chain when a channel is over its message caps.

## cmd/styx — verbs and app wiring

One file per verb (`research.go`, `plan.go`, `build.go`, `review.go`,
`auto.go`, `grunt.go`, `intel.go`, `budget.go`, `check.go`, `doctor.go`,
`repl.go`, `runs.go`, …).
Shared pieces:

- `main.go` — `parseGlobalFlags` strips `--quiet`/`--verbose`; `ensureFirstRun`
  seeds config; bare `styx` constructs the app and opens the REPL; errors exit
  1 with a `styx:` prefix.
- `dispatch.go` — verb switch in two tiers: verbs that don't need the full app
  run first; the rest construct `app{routing, tracker, router, channels,
  progress}` via `loadApp()`. `loadApp()` shares the budget tracker with the
  router for both cap checks and 3-failures-in-10-minutes circuit breaking.
  `rawChannel()` unwraps the progress decorator for orchestration verbs that
  narrate themselves, leaving timeout protection in place. `seedMessageLimits`
  applies routing.toml message caps (with built-in fallbacks) to the budget
  tracker. Unknown verbs fall through to one-shot brain turns, so
  `styx "fix the flaky test"` is treated as an utterance rather than an error.
- `default_routing.go` — the seeded `routing.toml` content (`defaultRoutingTOML`).
- `grunt.go` — `cmdOneShot` serves grunt/think/explain/summarize/critique;
  `sendWithFallback` walks the Decision's fallback chain, recording each
  attempt in the budget DB with a classified error kind.
- `doctor.go` — `cmdDoctor` preflights local CLIs against the brain capability
  cards, reports whether each CLI runs with native resume or styx-maintained
  continuity, checks distinct configured Claude tier aliases with a cheap
  one-shot call, and verifies that Ollama has both the brain model
  (`llama3.2:3b` by default) and embedding model pulled. `--fix` pulls missing
  Ollama models.
- `repl.go` — the conversational frontend and session core. `cmdREPL` runs the
  persistent bare-`styx` loop with `/status`, `/budget`, `/threads`, `/why`,
  and `/quit`; `cmdBrainTurn` runs a single utterance and exits. Each turn
  recalls project/global memory, asks the local brain for an action, then
  replies, dispatches to persistent agent threads, runs a wired pipeline,
  performs an interactive handoff, or stores explicit memory. If the brain is
  unavailable, the session asks the user for a manual thread choice instead of
  failing closed. It also resolves brain tier names through `[tiers]` and
  degrades hot fable usage to opus via `budget.Tracker.ModelCount`. Session
  cleanup stores a best-effort distillation back to project memory.
- `logStatus()` writes `[styx]` status lines to stderr unless `--quiet`;
  final results go to stdout and are never suppressed.

## Channels (internal/channel + adapters)

`channel.Channel` is the provider abstraction: `Name()`, `Send(ctx, Request)`,
`BudgetState(ctx)`. `Request` carries model, system, prompt, attachments,
`Interactive` (hand the TTY to the child — build verb), `WorkingDir`, and
`Write` (let the channel edit files / run commands autonomously — the
`implement` verb). Token counts in `Response` are `len/4` estimates.

- Subprocess adapters (claude, codex, agy) classify exec failures into
  `channel.ClassifiedError{Kind: timeout|429|5xx|other}` so the router/budget
  can label them. agy is headless-only and always passes
  `--dangerously-skip-permissions`.
- `Write` requests grant autonomous file access: claude prepends
  `--dangerously-skip-permissions`; codex runs `exec --sandbox workspace-write`.
  This is what lets the router send `implement` work to codex.
- `ollama` speaks `/api/chat`, pings `/api/tags`, and auto-launches the macOS
  Ollama app with a 20s wait if it's down.
- `decorator.go` — `WithProgress` narrates each Send as a progress stage;
  skipped for interactive sends (spinner would fight the child for the TTY).
  `WithTimeout` gives non-interactive sends a deadline while leaving
  interactive handoffs unbounded.
- `gemini` is a registered alias for agy (v0.1 routing-rule compat).

## Routing (internal/router, internal/signals, internal/config/routing.go)

`routing.toml` (`~/.config/styx/`) parses into `config.Routing{Budget, Rules,
Brain, Tiers}`. Rules match on `verb` + required `signals`; **first match
wins**. A rule is either `use = "channel:model"` with an ordered `fallback`
chain, or a parallel rule (`parallel` + `synthesize_with`, used by `review`).
No match defaults to `ollama:qwen2.5-coder:14b`.

The `implement` verb routes autonomous plan application: codex is primary
(well-scoped execution), claude is the fallback, and the `complex` signal
(architecture/refactor/migrate/redesign/rewrite) keeps the work on claude.
`config.UpgradeRoutingFile` injects these rules into pre-v0.3 configs
(`EnsureImplementRules`) on next run, alongside the v0.2 gemini→agy rewrite.

`signals.Extract` is a pure tagger: `lang:<x>` from the project record,
`trivial` (≤50 chars), `complex` (architecture/refactor/migrate/redesign/
rewrite keywords), etc. `styx route --explain` prints the full trace via
`Router.Explain`.

Budget/reliability degradation: if the chosen channel's `UsedPct` (max of
5h/weekly message percentages) ≥ its `cap_pct`, or its failure circuit is open,
the router walks the fallback chain and marks the Decision `Degraded`.
Per-channel caps also carry optional `timeout_minutes` for non-interactive
subprocess sends; unset claude/codex/agy timeouts default to 10 minutes in app
wiring. `Brain` configures the planned local ollama routing brain and memory
embedding model; `Tiers` maps brain tier names to claude CLI model aliases, with
`fable` currently mapped to `opus` while the fable tier is suspended.

## Brain (internal/brain)

The REPL brain emits schema-constrained `Action` JSON from a small, fast,
non-reasoning local ollama instruct model (default `llama3.2:3b`; reasoning
models such as qwen3 are deliberately avoided — they add many seconds per turn).
`BuildPrompt`'s preamble is an example-led routing spec tuned for a 3B model: it
defines each action, draws the high-confusion boundaries explicitly (pipeline
verbs are reserved for the four exact styx operations and never general code
work; well-scoped implementation from a clear plan/spec is `dispatch:codex` (codex is the primary implementer), while ambiguous/architectural/refactor work, debugging with repo context, plan/design critique, and "explain what X does" are `dispatch:claude`; `research` is for answers that
live *outside* the repo; `review` is the current diff/changes vs a PR/design;
status questions are `reply`; "remember/note" facts are `remember`, not an
acknowledging `reply`; size routes large-file explains to `agy`), and carries
~40 few-shot examples (including codex-implementation, reply/review/intel/auto, handoff, and `parallel_dispatch` anchors) that empirically
matter more than prose rules for steering a 3B. This preamble previously scored **96% on `TestRoutingAccuracy`** (up from 84.8%) on the original 99-utterance set under the prior code-work->claude policy. Adopting codex-as-implementer (2026-06-15) reworked the preamble/cards AND the labelled set: `testdata/brain/utterances.json` was expanded to **190 utterances** (well-scoped implementation fixtures relabelled to `codex`, plus new fixtures for how the user actually prompts -- exa/websearch/deep `research`, superpowers handoff-vs-plan -- and previously-untested `escalate` and internal-vs-external "find out" boundaries). On the expanded set the pre-rework prompt scored 80% (it routes the new/relabelled `codex` cases to claude by the old policy); the reworked-but-untuned preamble scored 83.7% (159/190). Re-tuning it with **few-shot example anchors only** (no model/dataset/code/label change, no new prose rules) brought the shipped preamble to **91% (173/190) on `TestRoutingAccuracy`**, stable across two runs with an identical 17-miss set. The re-tune was driven by the byte-faithful promptfoo harness in `eval/promptfoo/` (see its `README.md`/`RESULTS.md`), which reproduces the Go gate's request shape and match logic exactly and predicted the gate's miss set byte-for-byte -- but the Go test stays canonical. Residual misses are dominated by the codex/claude implementation frontier and a handful of documented-hard/contentious cases (the `cosine()` structured-output limit, the 2 `escalate` exemplars, compound terminal-intent, and label disputes); the 3B has a hard "example budget" where anchoring one bucket destabilizes another, so further accuracy needs a bigger brain or more fixtures, not more rules.
Task-level actions are structural decisions: direct reply, single or parallel
agent dispatch, pipeline invocation, interactive handoff, memory write, or
confidence escalation. `Action.Valid` performs local structural validation
before the REPL trusts a model response; `ActionSchema` is sent to ollama as the
structured-output format. Capability cards describe claude, codex, agy, and
ollama on every brain turn; `styx doctor` uses the same cards as drift probes
for expected CLI flags and resume support. `BuildPrompt` combines those cards
with the current user utterance, rolling summary, recent turns, live-thread
status, and memory hits. The installed Codex CLI exposes `exec`, `--model`,
`--add-dir`, and `resume`; styx v1 still presents codex to the brain as a
headless `codex exec` dispatch target rather than an interactive handoff target.
`Ollama.Decide` posts the prompt to `/api/chat` with `ActionSchema` as the
structured-output format and `think: false` (routing is schema-constrained
classification, not a reasoning task; reasoning-model thinking — qwen3, r1 —
adds many seconds per turn, blowing the sub-second target and the request
timeout, and bleeds into the structured output, mis-slotting fields). It
retries once on invalid JSON/action output, and returns `ErrNeedUser` when
local routing cannot produce a decision. Low
confidence or explicit `escalate` actions can route the same prompt through
`ClaudeEscalator` on the haiku tier; escalation failures fall back to the local
valid action so the REPL can keep moving.

## Agent threads (internal/agent)

Agent threads are the durable conversation layer for the planned REPL
orchestrator. Adapters encode how styx invokes each CLI. Claude runs in
headless `stream-json` mode with native session resume (`--resume`), a 200k
token context window, verbose JSON output, and pre-granted permissions matching
the existing execute path. Codex and agy are plain v1 adapters with no native
resume/stream support from styx's perspective: codex runs `codex exec`, agy
runs `agy -p --dangerously-skip-permissions`, and continuity will be maintained
by styx summaries.

The package defines the shared event shape and parses Claude's stream protocol:
`system/init` captures session IDs, assistant text chunks stream intermediate
output, and final `result` events carry the answer plus real usage. Context
size counts normal input, cache creation input, and cache-read input tokens so
future thread compaction is metered against the actual Claude context window
rather than rough character estimates. Hook, tool-use-only, and malformed
stream lines are ignored.

Each project has a JSON thread store under
`~/.config/styx/state/threads/<project>.json`. Threads are named durable
conversations with a CLI, optional Claude session ID, rolling summary for
non-resume CLIs, last distillation checkpoint, context-token meter, turn count,
and update timestamp. Stores are created lazily and saved with tmp+rename.

`Runner` executes one turn by spawning the adapter's CLI with an optional
timeout and working directory. For stream-capable adapters it scans stdout
line-by-line, emits parsed events to the caller, captures Claude session IDs,
and records real input/output token counts from the final result. For plain
adapters it treats full stdout as the result and falls back to len/4 token
estimates until those CLIs expose structured usage. Every successful turn
updates the thread's context meter, turn count, and timestamp in memory; callers
persist the store after lifecycle decisions. `testdata/fakeagent` is an
executable stream-json fixture for runner and manager lifecycle tests, including
resume argument assertions and dead-session simulation.

`Manager` owns a project's thread lifecycle. `Dispatch` resolves the adapter,
creates the thread on first use, seeds fresh/restarted sessions with a project
role line or last distillation, runs the turn, records real token usage and the
routed model to the budget log under verb `thread`, maintains rolling summaries
for plain adapters, and saves the thread store. If a resume-capable CLI reports
a dead session, the manager clears the session ID and retries once using the
last distillation as the handoff seed. When a resume-capable thread crosses its
configured context threshold, the manager asks the live session for a structured
handoff using the distill model, writes that distillation to memory when an
embedder/store are configured, clears the session ID, and starts the next turn
fresh. `StatusLines` renders compact thread state for the brain and `/status`.
`Handoff` opens an interactive Claude session for an existing Claude thread and
then best-effort ingests a summary back into thread state and memory.

## Budget (internal/budget)

Append-only SQLite log at `~/.config/styx/state/usage.db` (`usage` table:
ts/channel/verb/model/tokens/success/error_kind; `cooldown` table). `Tracker`
computes `State` per channel: legacy token percentages plus message counts in
rolling 5h (`WindowSession`) and 168h (`WindowWeek`) windows against limits
from routing.toml. `ModelCount(channel, model, window)` counts per-model rows
for tier-aware degradation. `ShouldCircuitBreak(channel, threshold, window)`
counts recent failures; app routing opens a channel circuit after 3 failures in
10 minutes.

## Projects & paths (internal/project, internal/config/projects.go, internal/paths)

`project.Current()` walks up to the git root and auto-registers unknown repos
into `~/.config/styx/projects.toml` (slugged name, sniffed language, default
`styx/research` + `styx/plans` dirs). `paths` resolves XDG-style locations:
ConfigDir, StateDir, CacheDir, LogDir, RoutingPath, ProjectsPath, UsageDBPath,
MemoryDir, and ThreadsDir. All file writes in config/brief/intel use atomic
tmp+rename.

## Intel (internal/intel)

Builds a per-project codebase index (`~/.config/styx/state/intel/<project>/
index.json`, schema-versioned): file tree, module map + purposes, conventions,
key symbols, recent commits, TODOs, external deps. Module summaries and key
symbols come from agy calls with per-call timeouts. Staleness: >5 commits or
>7 days triggers auto-refresh in plan/build flows. `render.go` renders the
index to markdown and writes `<project>/.claude/context.md` (or
`context.styx.md` + `@import` when a user-authored context.md exists) so
Claude Code auto-loads project context.

## Memory (internal/memory)

Long-term memory is stored in SQLite databases under
`~/.config/styx/state/memory/`. Each store has a `memory` table of typed items
(`decision`, `todo`, `distillation`, `brief`, `fact`, or
`routing-preference`) with source metadata, creation time, and a float32
embedding packed as a little-endian blob. The initial store API supports open,
close, insert, and newest-first full scans. `Recall` embeds a query and ranks
items across one or more stores by brute-force cosine similarity at personal
scale. `Embedder` abstracts text to float32 vectors; the production
`OllamaEmbedder` posts to `/api/embed` with a 30s HTTP client timeout and
caller-provided context.

## Pipelines (internal/pipeline + cmd/styx/auto.go)

`styx auto <goal>` runs 7 stages: research → intel → plan → execute → test →
review → ship. State persists at `<project>/.styx/runs/<run-id>/state.json`
after every stage; a lock file prevents concurrent runs; `auto --resume`
re-enters at the first non-completed stage. Stage behaviors are closures on
`Runner` injected by `auto.go` (e.g. `RunReview` = git diff → synthesized
claude+codex review → `research.Parse` counts blocking/important findings and
logs when parsing degrades to the raw-text fallback; failed reviews loop through
fix attempts via `execute.Apply`). The execute and fix-loop stages route through
the `implement` verb: `implementOptions` resolves the channel (codex for
well-scoped work, claude for `complex` goals) and injects it into
`execute.Options.Channel` — except claude, which is left nil so `Apply` uses its
built-in live-streaming claude path.

## Research (internal/research, internal/brief)

Convergence loop: drafter (agy) drafts, critic (codex) critiques as structured
`Critique{Blocking, Important, Nits}`, loop revises until converged (no
blocking/important), oscillation detected by draft-hash comparison, max 6
rounds. `Parse` accepts strict JSON, embedded JSON, or keyword sections, and
falls back to treating garbage as one IMPORTANT finding (never silently
converges); parse fallback errors are surfaced through progress/status instead
of being swallowed. `deep.go` extracts cited URLs, fetches (80KB cap), and
appends a Sources Appendix. `brief` writes timestamped briefs/plans into the
project's configured dirs and resolves the most recent brief.

## Execute (internal/execute)

`Apply` applies a plan autonomously with an "implement this plan" prompt. When
`Options.Channel` is set (the router picked codex for `implement`), it routes
through that channel with `Write: true` and captures output; when nil it uses
the built-in claude path (`--dangerously-skip-permissions -p`), which streams
claude's stderr live. `Ship` handles commit/push/PR (via `gh`), honoring
`--no-pr`/`--no-push`.

## Progress (internal/progress)

TTY-aware narrator: animated braille spinner on a terminal, plain lines
otherwise, no-op when quiet. One `Tracker` per invocation; `Stage` lifecycle
is Done/Fail/Info, opening a stage implicitly closes the previous one. All
lines prefixed `[styx]` on stderr.

## Secrets (internal/config/secrets.go)

macOS Keychain under service `styx`; `migrate-secrets` moves plaintext env
vars out of shell rc files.

## On-disk layout

```
~/.config/styx/routing.toml                 routing rules + caps (user-edited)
                                             plus brain/tier defaults for REPL routing
~/.config/styx/projects.toml                project registry (auto-managed)
~/.config/styx/state/usage.db               sqlite usage log
~/.config/styx/state/memory/                per-project memory sqlite databases
~/.config/styx/state/threads/               agent-thread state
~/.config/styx/state/intel/<proj>/index.json
<project>/.claude/context.md                rendered intel (Claude Code loads it)
<project>/.styx/runs/<run-id>/state.json    pipeline state
<project>/styx/research, styx/plans         briefs + plans (per-project config)
```

## Testing conventions

Table-driven tests with `t.Run`; `httptest` fakes for ollama; channel/router
tests use in-memory stubs (`BudgetSource`, fake channels); `testdata/` holds
fixtures (`routing/`, `brain/`, plus `fakeagent` once agent threads land).
`TestRoutingAccuracy` is env-gated behind `STYX_BRAIN_IT=1` and runs the real
local ollama brain against `testdata/brain/utterances.json` (190 labelled utterances); it should be run
only where ollama is up and the brain model (`llama3.2:3b`) is pulled. `make test` = `go test ./...`.
It is the **canonical** routing-accuracy gate. For fast prompt iteration only,
`eval/promptfoo/` holds a byte-faithful promptfoo harness (dev tool, run via
`npx`, no `go.mod` deps) that replicates the brain's `/api/chat` request and the
gate's match logic and generates its tests from the same `utterances.json` — so
it can't disagree with the gate. `eval/promptfoo/braindump` regenerates the
harness's code-mirrored artifacts from `cards.go`/`action.go`/`prompt.go`; rerun
it after editing those so the eval never drifts.

## Planned work

Checkpoint B dogfooding and later safety/provenance/trust hardening are tracked
in `docs/superpowers/plans/2026-06-12-styx-repl-orchestrator.md`.
