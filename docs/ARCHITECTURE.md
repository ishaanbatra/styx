---
owns:
  - "cmd/styx/**"
  - "internal/**"
  - "testdata/**"
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
              │ ensureFirstRun(): seed ~/.config/styx/routing.toml, v0.1→v0.2 upgrade
              ▼
        cmd/styx/dispatch.go
              │ no-app verbs (help, doctor*, project, route, budget, check, runs, execute…)
              │ app verbs → loadApp(): routing.toml + budget.Tracker + router + channels
              ▼
        internal/router.Route(verb, signals) ──► Decision{channel, model, fallback…}
              │                                       ▲
              │                          internal/signals.Extract (pure tagger)
              ▼
        internal/channel (decorated: WithProgress wrapping the raw adapter)
              ├── channel/claude   exec `claude -p` / interactive
              ├── channel/codex    exec `codex exec`
              ├── channel/agy      exec `agy -p --dangerously-skip-permissions`
              └── channel/ollama   HTTP localhost:11434 (auto-launches the app)
```

Every send is recorded in the budget DB; routing degrades down each rule's
fallback chain when a channel is over its message caps.

(* `doctor` exists once the REPL-orchestrator plan lands — see "Planned work".)

## cmd/styx — verbs and app wiring

One file per verb (`research.go`, `plan.go`, `build.go`, `review.go`,
`auto.go`, `grunt.go`, `intel.go`, `budget.go`, `check.go`, `runs.go`, …).
Shared pieces:

- `main.go` — `parseGlobalFlags` strips `--quiet`/`--verbose`; `ensureFirstRun`
  seeds config; errors exit 1 with a `styx:` prefix.
- `dispatch.go` — verb switch in two tiers: verbs that don't need the full app
  run first; the rest construct `app{routing, tracker, router, channels,
  progress}` via `loadApp()`. `rawChannel()` unwraps the progress decorator for
  orchestration verbs that narrate themselves. `seedMessageLimits` applies
  routing.toml message caps (with built-in fallbacks) to the budget tracker.
- `default_routing.go` — the seeded `routing.toml` content (`defaultRoutingTOML`).
- `grunt.go` — `cmdOneShot` serves grunt/think/explain/summarize/critique;
  `sendWithFallback` walks the Decision's fallback chain, recording each
  attempt in the budget DB with a classified error kind.
- `logStatus()` writes `[styx]` status lines to stderr unless `--quiet`;
  final results go to stdout and are never suppressed.

## Channels (internal/channel + adapters)

`channel.Channel` is the provider abstraction: `Name()`, `Send(ctx, Request)`,
`BudgetState(ctx)`. `Request` carries model, system, prompt, attachments,
`Interactive` (hand the TTY to the child — build verb), and `WorkingDir`.
Token counts in `Response` are `len/4` estimates.

- Subprocess adapters (claude, codex, agy) classify exec failures into
  `channel.ClassifiedError{Kind: timeout|429|5xx|other}` so the router/budget
  can label them. agy is headless-only and always passes
  `--dangerously-skip-permissions`.
- `ollama` speaks `/api/chat`, pings `/api/tags`, and auto-launches the macOS
  Ollama app with a 20s wait if it's down.
- `decorator.go` — `WithProgress` narrates each Send as a progress stage;
  skipped for interactive sends (spinner would fight the child for the TTY).
- `gemini` is a registered alias for agy (v0.1 routing-rule compat).

## Routing (internal/router, internal/signals, internal/config/routing.go)

`routing.toml` (`~/.config/styx/`) parses into `config.Routing{Budget, Rules,
Brain, Tiers}`. Rules match on `verb` + required `signals`; **first match
wins**. A rule is either `use = "channel:model"` with an ordered `fallback`
chain, or a parallel rule (`parallel` + `synthesize_with`, used by `review`).
No match defaults to `ollama:qwen2.5-coder:14b`.

`signals.Extract` is a pure tagger: `lang:<x>` from the project record,
`trivial` (≤50 chars), `complex` (architecture/refactor/migrate/redesign/
rewrite keywords), etc. `styx route --explain` prints the full trace via
`Router.Explain`.

Budget degradation: if the chosen channel's `UsedPct` (max of 5h/weekly
message percentages) ≥ its `cap_pct`, the router walks the fallback chain and
marks the Decision `Degraded`. Per-channel caps also carry optional
`timeout_minutes` for REPL/orchestrator subprocess budgets. `Brain` configures
the planned local ollama routing brain and memory embedding model; `Tiers` maps
brain tier names to claude CLI model aliases, with `fable` currently mapped to
`opus` while the fable tier is suspended.

## Brain (internal/brain)

The REPL brain emits schema-constrained `Action` JSON from a local ollama model.
Task-level actions are structural decisions: direct reply, single or parallel
agent dispatch, pipeline invocation, interactive handoff, memory write, or
confidence escalation. `Action.Valid` performs local structural validation
before the REPL trusts a model response; `ActionSchema` is sent to ollama as the
structured-output format. Capability cards describe claude, codex, agy, and
ollama on every brain turn; they also define the future `doctor` drift probes
for expected CLI flags and resume support. `BuildPrompt` combines those cards
with the current user utterance, rolling summary, recent turns, live-thread
status, and memory hits. The installed Codex CLI exposes `exec`, `--model`,
`--add-dir`, and `resume`; styx v1 still presents codex to the brain as a
headless `codex exec` dispatch target rather than an interactive handoff target.

## Budget (internal/budget)

Append-only SQLite log at `~/.config/styx/state/usage.db` (`usage` table:
ts/channel/verb/tokens/success/error_kind; `cooldown` table). `Tracker`
computes `State` per channel: legacy token percentages plus message counts in
rolling 5h (`WindowSession`) and 168h (`WindowWeek`) windows against limits
from routing.toml. `ShouldCircuitBreak(channel, threshold, window)` counts
recent failures (wired into routing by the REPL-orchestrator plan).

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
claude+codex review → `research.Parse` counts blocking/important findings;
failed reviews loop through fix attempts via `execute.Apply`).

## Research (internal/research, internal/brief)

Convergence loop: drafter (agy) drafts, critic (codex) critiques as structured
`Critique{Blocking, Important, Nits}`, loop revises until converged (no
blocking/important), oscillation detected by draft-hash comparison, max 6
rounds. `Parse` accepts strict JSON, embedded JSON, or keyword sections, and
falls back to treating garbage as one IMPORTANT finding (never silently
converges). `deep.go` extracts cited URLs, fetches (80KB cap), and appends a
Sources Appendix. `brief` writes timestamped briefs/plans into the project's
configured dirs and resolves the most recent brief.

## Execute (internal/execute)

`Apply` runs claude headless (`--dangerously-skip-permissions -p`) with an
"implement this plan" prompt. `Ship` handles commit/push/PR (via `gh`),
honoring `--no-pr`/`--no-push`.

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
fixtures (`routing/`, plus `fakeagent` + `brain/` once the REPL plan lands).
`make test` = `go test ./...`.

## Planned work (not yet built)

The remaining REPL orchestrator work — persistent conversational `styx` with
durable agent threads (per-turn `--resume`), frontend loop, and `styx doctor`
— is specced in `docs/superpowers/specs/2026-06-12-styx-repl-orchestrator-
design.md` and planned task-by-task in `docs/superpowers/plans/
2026-06-12-styx-repl-orchestrator.md`. It still needs `internal/agent`,
`cmd/styx/repl.go`, and `styx doctor`. When those land, add sections here and
update the overview diagram.
