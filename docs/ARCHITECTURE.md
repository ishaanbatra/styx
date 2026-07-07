---
owns:
  - "cmd/styx/**"
  - "internal/**"
  - "testdata/**"
  - "eval/**"
last_verified: 2026-07-06
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
argv ──► cmd/styx/main.go (global flags: --quiet --verbose --project --dir)
              │ bare `styx` launches the conductor (`styx repl` for the
              │ classic REPL); otherwise ensureFirstRun():
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
`repl.go`, `launch.go`, `runs.go`, …).
Shared pieces:

- `main.go` — `parseGlobalFlags` strips `--quiet`/`--verbose` plus
  `--project <alias>` / `--dir <path>`; `ensureFirstRun` seeds config; bare
  `styx` constructs the app and calls `cmdLaunch(a)`, handing off to the
  Claude Code conductor (see "Launcher" below) — `styx repl` is the only way
  to reach the classic v0.2 REPL loop now; errors exit 1 with a `styx:`
  prefix. Declares `const styxVersion = "0.4.0-dev"`, printed by the `version`
  verb (bump on tagged releases).
- `dispatch.go` — verb switch in two tiers: verbs that don't need the full app
  run first (including `help`/`-h`/`--help` and `version`/`--version`/`-V`,
  which prints `"styx " + styxVersion` and returns immediately — no app, no
  conductor); the rest construct `app{routing, tracker, router, channels,
  progress}` via `loadApp()`. `loadApp()` runs a best-effort model refresh when
  `models.json` is stale and reloads routing if a de-pin migration ran, then
  shares the budget tracker with the router for both cap checks and
  3-failures-in-10-minutes circuit breaking. `rawChannel()` unwraps the
  progress decorator for orchestration verbs that narrate themselves, leaving
  timeout protection in place. `resolveGlobalTarget(arg)` combines a verb's
  positional target with global `--project` / `--dir` flags and routes every
  project-scoped verb through `internal/target.Resolve`; this replaces the old
  `resolveTarget` / `resolveProjectArg` split and removes the silent cwd
  fallback for failed explicit targets. `seedMessageLimits` applies
  routing.toml message caps (with built-in fallbacks) to the budget tracker.
  The verb switch has explicit `repl` (→ `cmdREPL`, the classic v0.2 loop),
  `launch` (→ `cmdLaunch`), and `resume` (→ `cmdResume`, first positional arg
  as the optional session ID) cases. When no verb matches and every positional
  token resolves to a registered project (`allReposResolve`), the dispatcher
  now launches the conductor bound to those repos (first becomes the focus,
  via `cmdLaunch`) rather than opening the REPL; otherwise the tokens are
  treated as a one-shot utterance (`styx "fix the flaky test"` is an
  utterance, not an error) handled by `cmdBrainTurn`. `mcp` is also an app
  verb (needs the full `app{router, tracker}` from `loadApp()`) and is
  dispatched in the second switch alongside `auto`, `research`, etc.
- `launch.go` — `cmdLaunch(a *app, repos ...string) error`, the conductor
  front door, and `cmdResume(a *app, sessionID string) error`, which
  relaunches the conductor resuming a Claude Code session: `--resume <id>`
  with a session ID, `--continue` (the directory's most recent session)
  without. Both are thin wrappers over the shared
  `launchConductor(a, repos, extraArgs)` helper — resume exists because the
  toolbelt flags are per-invocation, so a plain `claude --resume` would
  restore the conversation but lose the styx MCP server and guidance. Resume
  takes no repo arguments (sessions are per-directory, so it is cwd-anchored
  like bare `styx`; extra repos are out of scope) and passes its flags via
  `launcher.Opts.ExtraArgs`. `launchConductor`'s first line is
  `ensureInteractiveTTY()`: it refuses to launch (returning an actionable
  error instead of letting Claude Code exec on a pipe and die with a cryptic
  `--print` failure) whenever stdin isn't a character device, per the
  var-swappable `stdinIsTTY func() bool` (tests stub it; production stats
  `os.Stdin` and checks `os.ModeCharDevice`). It then resolves the focus project
  exactly like `newREPLSession`'s seed
  resolution (first repo by alias, or `resolveGlobalTarget("")` for bare
  `styx` so cwd still works). Uniquely among verbs, bare `styx` outside any
  git repository does not error: `resolveLaunchTarget` catches
  `project.ErrNotInGitRepo` (implicit-cwd case only — explicit repo args and
  `--project`/`--dir` stay strict) and launches in the plain directory,
  synthesizing an unregistered `Project{Name: base(cwd), Path: cwd}` and
  narrating via `logStatus`; project-scoped MCP tools then require a
  registered repo per-call. Resolves any extra repos — passed to the
  launcher as `Opts.ExtraRepos` (rendered as `--add-dir` flags on the Claude
  Code session) and folded into the guidance as a note telling the brain to
  also pass them as the MCP `dispatch` tool's `extra_roots` so dispatched
  agent threads get the same access — loads `internal/guidance.Load(project.Path)`,
  appends `recallRoutingPrefs(a)` output when non-empty, resolves the running
  `styx` binary via `os.Executable()`, and calls
  `(&launcher.ClaudeHost{}).Launch(ctx, launcher.Opts{...})`.
  `recallRoutingPrefs(a *app) string` opens the global memory store + ollama
  embedder exactly as `newREPLSession` does (`repl.go`), calls
  `memory.Recall(ctx, emb, "routing preference", 5, glob)`, and joins hit text
  with `"\n- "`. It is a pure enhancement: any failure (store open, recall) is
  narrated via `logStatus` and yields `""` rather than blocking the launch.
- `default_routing.go` — the seeded `routing.toml` content (`defaultRoutingTOML`).
- `grunt.go` — `cmdOneShot` serves grunt/think/explain/summarize/critique;
  `sendWithFallback` walks the Decision's fallback chain, recording each
  attempt in the budget DB with a classified error kind.
- `doctor.go` — `cmdDoctor` runs model refresh/de-pin migration, preflights
  local CLIs against the brain capability cards, reports whether each CLI runs
  with native resume or styx-maintained continuity, checks distinct configured
  Claude tier aliases with a cheap one-shot call, and verifies that Ollama has
  both the brain model (`qwen2.5-coder:7b` by default) and embedding model
  pulled. `--fix` pulls missing Ollama models.
- `repl.go` — the conversational frontend and session core, now reached via
  `styx repl` rather than bare `styx`. `cmdREPL` runs the persistent loop with
  `/status`, `/budget`, `/threads`, `/why`,
  `/audit`, `/repos`, `/focus`, and `/quit`; `cmdBrainTurn` runs a single utterance and exits. Each turn
  recalls project/global memory, asks the local brain for an action, then
  replies, dispatches to persistent agent threads, runs a wired pipeline,
  performs an interactive handoff, or stores explicit memory. If the brain is
  unavailable, the session asks the user for a manual thread choice instead of
  failing closed. It also resolves brain tier names through `[tiers]` and
  degrades hot fable usage to opus via `budget.Tracker.ModelCount`. Each
  session also opens a per-project audit log and `/audit` tails the last 20
  records. Session cleanup stores a best-effort distillation back to project
  memory and closes open stores/logs.
- `mcp.go` — MCP tool handlers, JSON schemas, tool assembly, and the `styx mcp`
  command entry point. Defines `routeArgs` (Task, Verb, Signals, Project),
  `routeResult` (Channel, Model, Effort, FallbackChain, Reasoning, Budget,
  Degraded, plus v2 additive `ClassifiedSignals`, `Floor`, `TierPlan`,
  `BlockedByBudget`, `RetryAfterS`), `budgetStatusArgs` (Channel),
  `recordUsageArgs` (Channel, Messages, TokensIn, TokensOut, Verb, Model,
  Success, Project, RunID), `recordResult` (Recorded, Budget), `budgetSnapshot`
  (Channel, SessionCount/Limit, WeeklyCount/Limit, percentages, CooldownUntil,
  Stale flag), `channelHealthArgs`/`channelHealthResult`, `getIntelArgs`/
  `refreshIntelArgs`/`intelResult`, and `recallArgs`/`recallResult`; handler
  functions `handleRoute()`, `handleBudgetStatus()`, `handleRecordUsage()`,
  `handleChannelHealth()`, `handleGetIntel()`, `handleRefreshIntel()`,
  `handleRecall()`, and `budgetSnapshotFor()` (package main). Handler logic is
  kept simple: route decision + snapshot for one task, budget snapshot for one
  or all four channels, record usage rows (Messages>1 appends N rows with
  token totals on the first), channel health/failure/cooldown per channel,
  intel read/rebuild per project, and decayed top-k memory recall. Project-
  scoped tools (`get_intel`, `refresh_intel`, `recall`) resolve their
  `project` argument via `resolveProjectStrict`, which never falls back to the
  server's own cwd and returns a `channel.ClassifiedError` for an empty or
  unknown project. `routeSchema`, `budgetStatusSchema`, `recordUsageSchema`,
  `channelHealthSchema`, `getIntelSchema`, `refreshIntelSchema`, and
  `recallSchema` are `map[string]any` JSON Schema objects passed as
  `Tool.InputSchema`. `mcpTools(a *app)` assembles all seven handlers into
  `[]mcpserver.Tool`, unmarshaling raw JSON arguments before each dispatch.
  `cmdMCP(a *app, args []string)` constructs the server via `mcpserver.New("styx",
  mcpServerVersion, append(mcpTools(a), conductorTools(newConductorDeps(a))...))`,
  logs readiness to stderr via `logStatus` (naming all eleven tools), and runs
  `srv.Serve(ctx, os.Stdin, os.Stdout)` — stdout carries the JSON-RPC protocol
  only, nothing else. `const mcpServerVersion = "0.1.0"`. See the "MCP
  server" and "Conductor MCP tools" sections below for the
  route-v2 field shapes and value-shape decisions consumers must know.

### Multi-project session

A REPL session can be bound to more than one repo at a time. `replSession.bound` is
a map from stable project ID to a per-repo slot (agent `Manager`, memory store,
thread store) created on first reference by `bind(p)` — a lazy memoized helper
that initialises only what is needed for that repo. `s.focus` is a pointer into
`bound` naming the current primary repo; `proj()`, `mgr()`, and `mem()` are
accessors that delegate to the focus slot. One session-global embedder, budget
tracker, brain, run-id, and audit logger are shared across every bound repo.

**Per-dispatch repo routing.** When the brain emits a dispatch with a non-empty
`Dispatch.Project` field, the REPL resolves that name via `internal/target`,
lazily binds the resulting repo, and dispatches on that repo's `Manager` instead
of the focus repo's. Any `Dispatch.ExtraRoots` are forwarded through
`DispatchSpec.ExtraRoots` and rendered as `--add-dir` flags by the agent layer
(see `## Agent threads` and `## Channels`). An unresolvable name is wrapped in
`errUnresolvedRepo` and routed through `askUserRoute` — the same escalation path
as a brain `ErrNeedUser` — so the session surfaces the problem rather than
silently falling back to the focus repo.

**Cross-repo recall.** `Recall` spans every bound repo's memory store plus the
global store, so a fact learned while working in one repo surfaces when the focus
is elsewhere. Memory writes target the focus repo's store, tagged with its
project ID.

**Project-tagged audit.** A single audit logger is shared for the whole session;
each record carries a `Project` field (the stable project ID of the repo
actually touched) so one session audit stream can span repos and `/audit` can
render the per-record project tag.

**Slash commands.** `/repos` lists all bound repos, marking the current focus.
`/focus <name>` resolves the given name, lazily binds it if needed, and flips
`s.focus` to that repo. `/status` and `/threads` iterate all bound repos.

**Launch binding.** `styx <repo...>` with every token resolving to a registered
project opens the REPL bound to those repos (first becomes focus); see the
dispatch.go bullet above. Naming a repo mid-conversation binds it lazily without
restarting the session.

- `logStatus()` writes `[styx]` status lines to stderr unless `--quiet`;
  final results go to stdout and are never suppressed.

## Channels (internal/channel + adapters)

`channel.Channel` is the provider abstraction: `Name()`, `Send(ctx, Request)`,
`BudgetState(ctx)`. `Request` carries model, optional pass-through reasoning
effort, system, prompt, attachments, `Interactive` (hand the TTY to the child —
build verb), `WorkingDir`, `Write` (let the channel edit files / run commands
autonomously — the `implement` verb), and `ExtraRoots []string` (additional repo
roots for cross-repo work: claude, codex, and agy each emit `--add-dir <root>`
per non-empty entry; the process cwd stays `WorkingDir` — ExtraRoots is purely
additive; for codex, `--add-dir` flags sit after the `exec` subcommand). Token
counts in `Response` are `len/4` estimates.

- Subprocess adapters (claude, codex, agy) classify exec failures into
  `channel.ClassifiedError{Kind: timeout|429|5xx|other}` so the router/budget
  can label them. agy is headless-only and always passes
  `--dangerously-skip-permissions`.
- `Write` requests grant autonomous file access: claude prepends
  `--dangerously-skip-permissions`; codex runs `exec --sandbox workspace-write`.
  This is what lets the router send `implement` work to codex.
- Codex one-shot requests omit `--model` when routing supplies an empty model,
  deferring to the Codex CLI default; when `Request.Effort` is set, the adapter
  passes `-c model_reasoning_effort=<effort>`.
- Claude one-shot requests keep `--model <alias>` when routed to a class alias
  and pass `--effort <effort>` when `Request.Effort` is set.
- `ollama` speaks `/api/chat`, pings `/api/tags`, and auto-launches the macOS
  Ollama app with a 20s wait if it's down.
- `decorator.go` — `WithProgress` narrates each Send as a progress stage;
  skipped for interactive sends (spinner would fight the child for the TTY).
  `WithTimeout` gives non-interactive sends a deadline while leaving
  interactive handoffs unbounded.
- `gemini` is a registered alias for agy (v0.1 routing-rule compat).

## Routing (internal/router, internal/signals, internal/config/routing.go)

`routing.toml` (`~/.config/styx/`) parses into `config.Routing{Budget, Rules,
Models, Brain, Conductor, Tiers}`. Rules match on `verb` + required `signals`; **first match
wins**. A rule is either `use = "channel:model"` with an ordered `fallback`
chain, or a parallel rule (`parallel` + `synthesize_with`, used by `review`).
No match defaults to `ollama:qwen2.5-coder:14b`. Rules may also carry an
optional pass-through `effort` string; styx stores it without validating
provider-specific values and the router copies it onto `Decision.Effort`.
Bare channel tokens such as `codex` are valid and mean "let that CLI choose its
current default model." `[models].refresh_interval_hours` controls the
model-refresh staleness threshold and defaults to 24 hours. The seeded
`default_routing.go` table is already in that de-pinned form, with
`research.critic` showing `effort = "high"` as the pass-through example.

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
embedding model; `Conductor` configures the frontier-brain launcher and MCP
toolbelt (e.g. `ship_gate`: handshake | tty | off, default handshake, controlling
ship-risk confirmation for `dispatch(risk=ship)` and `pipeline_run auto`);
`Tiers` maps brain tier names to claude CLI model aliases, with `fable` currently
mapped to `opus` while the fable tier is suspended.

**Capability floor (v2).** `internal/signals/floor.go` defines `Tier`
(`TierLocal < TierHaiku < TierSonnet < TierOpus`), `TierOf(channel, model)`
(hand-curated channel/model → tier map, biased toward inclusion — an unknown
cloud channel is treated as sonnet-class so it's never wrongly excluded), and
`Floor(sigs []string)`, which looks up each signal in a `signalFloor` map kept
beside the signal definitions (currently `complex` and `deep` → `TierSonnet`)
and returns the highest tier any signal requires (`TierLocal` = no floor).
`Router.Route` computes `floor := signals.Floor(req.Signals)` and restricts
fallback-chain degradation to floor-clearing candidates only — a request with
a `complex`/`deep` signal will never degrade down to an ollama/local target,
even under budget pressure. `Decision` carries three additive v2 fields:
`Floor` (the tier keyword string), `TierPlan{Acceptable, Chosen, EscalateTo}`
(the floor-clearing candidates, the one actually chosen within budget, and the
next higher tier to escalate to if all floor-clearing targets are exhausted),
and `BlockedByBudget` — set true, with `Decision` still populated with its
best-effort chosen channel, when every floor-clearing target is over budget or
circuit-open. This replaces the old silent-return-first-degraded-target
behavior on chain exhaustion with a loud refusal signal a caller can check
before dispatching. `Router.Explain` prints `floor: <tier>` (when not
`local`) and a `blocked: ...` line when `BlockedByBudget`.

## Guidance (internal/guidance)

Data-driven routing guidance replacing the v0.2 brain's compiled-in preamble.
A global guidance file is seeded at `~/.config/styx/guidance.md` on first call
to `Load()` and is user-editable. User edits are never overwritten, but a file
whose content exactly matches a previous seed version (the retained `seedV1`
constant) is recognized as unmodified and transparently upgraded to the
current `Seed` on load. `Load(projectPath string)` returns the global guidance
with an optional per-repo override appended from `<repo>/styx/guidance.md` if
it exists. The `Seed` constant contains the default shipped guidance: a
dispatch-by-default rule (substantive work — implementation, research, review,
large summarization — goes through `dispatch`/`pipeline_run` rather than the
host's built-in Agent/Task subagents, which burn the interactive session's
Claude quota invisibly to styx's budget ledger; built-ins are reserved for
work too small to brief), a research-task mapping (`pipeline_run research`
for brief-producing research, `dispatch cli=claude` for repo-focused
investigation, agy when very large), channel best purposes (codex as primary
implementer for well-scoped work, claude for ambiguous/architectural/refactor
work, agy for large-file explains, ollama for trivial one-shots), model tier
guidance, working style conventions (plan before dispatch, reuse threads,
consult memory, check budget), and ship policy (confirmation token handoff).

## Model Sync (internal/modelsync)

`modelsync` owns model discovery state that keeps routing in the
defer-to-latest form. The package defines a small `Discoverer` interface and
per-channel `Result` records. The shipped discoverers are `CodexDiscoverer`,
which reads the top-level `model` setting from the Codex CLI config for
transparency, and `ClaudeDiscoverer`, which reports the stable class aliases
`opus`, `sonnet`, `haiku`, and `fable`. Discovery output is persisted in an
atomic `models.json` cache with a `refreshed_at` timestamp so callers can skip
refresh work until `[models].refresh_interval_hours` says the cache is stale.
Its migration pass is a surgical, idempotent text rewrite of legacy routing
tokens: `codex:<version>` becomes bare `codex`, and pinned Claude versions such
as `claude:opus-4-7` collapse to their class alias. Interactive entries,
`agy`, and `ollama` routes are left untouched. `Refresh` orchestrates discovery,
migration, cache writes, and global routing-correction memories; each
discoverer is isolated with a short timeout so one failed channel only logs a
warning and the rest of the refresh continues. `styx doctor` runs this refresh
on every invocation and prints the applied routing de-pins as status output;
`loadApp()` runs it only when the cache is stale, covering verbs, one-shot
turns, and the REPL, and re-reads routing only when a migration actually
rewrote the file. Both call sites pass a lazy `correctionStoreOpener` so each
de-pin is recorded as a `routing-preference` memory in the global store; the
store and ollama embedder are opened only on the stale/refresh path, keeping the
common fresh-cache hot path free of any sqlite or embedder setup. Effort remains a separate dispatch-time axis:
`Rule.Effort` flows through `Decision.Effort` into `channel.Request.Effort`,
where codex maps it to `model_reasoning_effort` and claude maps it to
`--effort` without styx validating provider-specific values. agy already
ignores routed model ids and ollama uses explicit local model names that doctor
validates, so neither participates in auto-discovery.

## Brain (internal/brain)

The REPL brain emits schema-constrained `Action` JSON from a small, fast,
non-reasoning local ollama instruct model (default `qwen2.5-coder:7b`; reasoning
models such as qwen3 are deliberately avoided — they add many seconds per turn).
`BuildPrompt`'s preamble is an example-led routing spec tuned for a small local model: it
defines each action, draws the high-confusion boundaries explicitly (pipeline
verbs are reserved for the four exact styx operations and never general code
work; well-scoped implementation from a clear plan/spec is `dispatch:codex` (codex is the primary implementer), while ambiguous/architectural/refactor work, debugging with repo context, plan/design critique, and "explain what X does" are `dispatch:claude`; `research` is for answers that
live *outside* the repo; `review` is the current diff/changes vs a PR/design;
status questions are `reply`; "remember/note" facts are `remember`, not an
acknowledging `reply`; size routes large-file explains to `agy`), and carries
~40 few-shot examples (including codex-implementation, reply/review/intel/auto, handoff, and `parallel_dispatch` anchors) that empirically
matter more than prose rules for steering a 3B. This preamble previously scored **96% on `TestRoutingAccuracy`** (up from 84.8%) on the original 99-utterance set under the prior code-work->claude policy. Adopting codex-as-implementer (2026-06-15) reworked the preamble/cards AND the labelled set: `testdata/brain/utterances.json` was expanded to **190 utterances** (well-scoped implementation fixtures relabelled to `codex`, plus new fixtures for how the user actually prompts -- exa/websearch/deep `research`, superpowers handoff-vs-plan -- and previously-untested `escalate` and internal-vs-external "find out" boundaries). On the expanded set the pre-rework prompt scored 80% (it routes the new/relabelled `codex` cases to claude by the old policy); the reworked-but-untuned preamble scored 83.7% (159/190). Re-tuning it with **few-shot example anchors only** (no model/dataset/code/label change, no new prose rules) brought the shipped preamble to **91% (173/190) on `TestRoutingAccuracy`**, stable across two runs with an identical 17-miss set. The re-tune was driven by the byte-faithful promptfoo harness in `eval/promptfoo/` (see its `README.md`/`RESULTS.md`), which reproduces the Go gate's request shape and match logic exactly and predicted the gate's miss set byte-for-byte -- but the Go test stays canonical. Residual misses are dominated by the codex/claude implementation frontier and a handful of documented-hard/contentious cases (the `cosine()` structured-output limit, the 2 `escalate` exemplars, compound terminal-intent, and label disputes); the 3B has a hard "example budget" where anchoring one bucket destabilizes another, so further accuracy needs a bigger brain or more fixtures, not more rules. Acting on that, the default brain was upgraded `llama3.2:3b` → `qwen2.5-coder:7b` (2026-06-16) and the set extended to **192** (adding 2 explicit-`ship` fixtures; 8 now carry a `want_risk` label). On the 7b, the shipped preamble (`v15`) scores **routing 178/192 (93%), risk-emission 6/8 (75%), folded gate 176/192 (92%)** on `TestRoutingAccuracy`, reproduced byte-for-byte by the promptfoo harness; adding the per-dispatch risk prose was routing-neutral (the no-risk `v14` baseline scored 177/192) while lifting risk emission from 2/8 to 6/8. Residual misses are the codex/claude implementation frontier, pipeline-`review`/`research` keyword leakage, and a few `reply`-vs-`claude` label disputes (is-this-sound / blast-radius); the 2 risk misses are `read`-class "explain … end to end" cases where the model omits `risk` and falls back to the safe `edit` default.
Task-level actions are structural decisions: direct reply, single or parallel
agent dispatch, pipeline invocation, interactive handoff, memory write, or
confidence escalation. Each dispatch carries two optional cross-repo fields:
`Project string` (json `project,omitempty`) names the primary repo the agent
should work in (empty = current focus repo), and `ExtraRoots []string` (json
`extra_roots,omitempty`) lists additional repo roots to attach (consumed by
the REPL's per-dispatch routing). Both are included in `ActionSchema` as
optional dispatch-item properties; `required` stays `["thread","message"]`.
Each dispatch also carries an optional coarse `RiskLevel`
(`read` | `edit` | `ship`) the brain proposes and the REPL enforces:
`Action.EffectiveRisk` takes the max across dispatches and forces the `auto`
pipeline to `ship`; the REPL confirms with the user before any `ship` action and
drops claude's pre-granted write permission for a `read` dispatch. Risk rides
**per-dispatch** in `ActionSchema` — a top-level risk scalar makes the model drop
the required `dispatches` array (routing collapsed to 51% in `llama3.2:3b` testing), so the
model-facing schema exposes risk only on dispatch items, taught via a few `read`/
`ship` few-shot anchors (`edit` is the omitted default); the top-level
`Action.Risk` exists but is code-derived (e.g. `auto` → ship), never model-set.
`Action.Valid` performs local structural validation (including the risk enum)
before the REPL trusts a model response; `ActionSchema` is sent to ollama as the
structured-output format. Capability cards describe claude, codex, agy, and
ollama on every brain turn; `styx doctor` uses the same cards as drift probes
for expected CLI flags and resume support. `BuildPrompt` combines those cards
with the current user utterance, rolling summary, recent turns, live-thread
status, and memory hits; it also injects a project registry — `Turn.BoundProjects`
and `Turn.KnownProjects` (pre-rendered one-liners) are emitted as "Bound projects:" /
"Known projects:" blocks in the user prompt (after memory hits, before the utterance)
so the model can map repo names the user mentions onto the `project`/`extra_roots`
dispatch fields. The brain package stays free of `internal/config`/`internal/project`:
`cmd/styx/repl.go` renders the registry one-liners via `renderProject`,
`renderBoundProjects`, and `renderKnownProjects` and passes them in as plain `[]string`.
The installed Codex CLI exposes `exec`, `--model`,
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
`~/.config/styx/state/threads/<id>.json`, keyed by the stable project ID rather
than the mutable registry name. Threads are named durable
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

`Manager` owns a project's thread lifecycle. `DispatchSpec` carries an
`ExtraRoots []string` field (absolute repo roots for cross-repo dispatch);
`Manager.Dispatch` renders them via `addDirArgs` into `--add-dir <root>` pairs,
merges them once into the `extra` slice, and passes that same merged slice at
both the first-attempt and crash-recovery `run.Send` sites. The codex agent
adapter's `ArgsFn` places the merged `--add-dir` flags after `exec` — the same
arg-order rule as the channel layer (the installed Codex CLI exposes `exec`,
`--model`, `--add-dir`, and `resume`, as noted above).
`Dispatch` resolves the adapter,
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
ts/channel/verb/model/tokens/success/error_kind/project/run_id; `cooldown`
table). `Tracker` opens the database with `journal_mode(WAL)` and
`busy_timeout(5000)` so multiple styx processes can append without immediate
`SQLITE_BUSY` failures. It computes `State` per channel: legacy token
percentages plus message counts in rolling 5h (`WindowSession`) and 168h
(`WindowWeek`) windows against limits from routing.toml. `ModelCount(channel,
model, window)` counts per-model rows for tier-aware degradation.
`ShouldCircuitBreak(channel, threshold, window)` counts recent failures; app
routing opens a channel circuit after 3 failures in 10 minutes — the shared
`BreakerThreshold`/`BreakerWindow` consts (used by both `dispatch.go`'s
`budgetSource.Broken` and the read snapshots below).

Two pure read methods expose that same posture without adding state.
`ChannelHealth(channel, threshold, window)` returns a `ChannelHealth` snapshot:
`CircuitOpen`, `FailuresRecent`, per-kind `ErrorKinds` buckets (raw
`error_kind`s folded into the friendly, zero-filled labels
timeout/rate_limit/server/other via `healthKind`), and
`CooldownRemainingSeconds` — the failure count and the kind buckets share one
window cutoff. `RetryAfter(channel)` estimates seconds until a channel regains
capacity: an active cooldown's remaining seconds first, else (via `windowRetry`)
the time until the oldest in-window message ages out under a *hit* message cap,
else 0 (unknown / no limit). Both are consumed by the MCP brain's
`channel_health` / `retry_after` tools.

`Event` carries two attribution fields added in v0.4: `Project` (the resolved
stable project ID, "" if none) and `RunID` (a per-session/per-verb correlation
string, "" if none). Run-ids are minted via `pipeline.NewRunID` — once per REPL
session (`repl-<name>`), once per `sendWithFallback` invocation (keyed by verb),
and the auto pipeline reuses its own `State.RunID` for the review stage. Every
budget `Record` call site now tags its event, making `project`/`run_id` the seam
the planned self-improvement tooling reads back to attribute usage. Both columns
are added via idempotent `ALTER TABLE` on open, so existing `usage.db` files are
migrated transparently on first access.

The other multi-terminal state surfaces were already hardened before the budget
WAL change: `projects.toml` is written via `config.SaveProjects` tmp+rename,
the model cache is written via `modelsync.Cache.Save` tmp+rename, and
same-repo pipeline runs are serialized by `internal/pipeline/lock.go`.

## Projects & paths (internal/project, internal/config/projects.go, internal/paths)

`project.Current()` walks up to the git root and auto-registers unknown repos
into `~/.config/styx/projects.toml` (stable `id`, slugged name, sniffed
language, default `styx/research` + `styx/plans` dirs). `config.Project.ID` is
a stable 12-hex-character hash of the absolute project path, produced by
`config.ProjectID(path)`. `LoadProjects` backfills missing IDs for legacy
registry entries, so old `projects.toml` files remain loadable while new
per-project state keys stop depending on mutable project names.

`styx project scan [root] [--depth N]` bulk-discovers repositories by walking
down from `root` (default `~`) to a bounded depth (default 4), pruning
`node_modules`, `vendor`, `.git`, virtualenv, and build-output directories. Once
it finds a git root, it registers that repo and does not descend into it, so
nested or vendored repos are not accidentally imported.

`paths` resolves XDG-style locations: ConfigDir, StateDir, CacheDir, LogDir,
RoutingPath, ProjectsPath, UsageDBPath, MemoryDir, AuditDir, and ThreadsDir. All
file writes in config/brief/intel use atomic tmp+rename.

`config.MigrateProjectState` is an idempotent startup and `doctor` migration
that renames legacy name-keyed memory DBs, audit dirs, intel dirs, and thread
files to stable ID-keyed paths. It only renames when the old path exists and the
new path does not; if both exist, it leaves the legacy copy in place and warns
rather than deleting user data.

## Target resolution (internal/target)

`target.Resolve(Spec{Alias, Dir, Cwd})` is the single seam every verb and the
REPL use to turn a `--project alias` / `--dir path` / cwd into a `Project`.
Precedence is Alias -> Dir -> Cwd; alias resolution is exact-name -> unique
prefix -> existing-path -> error listing candidates. It never silently falls
back to the cwd when an explicit target was given and failed. `cmd/styx` wraps
it as `resolveGlobalTarget(arg)`, combining a verb's positional target with the
global flags.

## Intel (internal/intel)

Builds a per-project codebase index (`~/.config/styx/state/intel/<id>/
index.json`, schema-versioned): file tree, module map + purposes, conventions,
key symbols, recent commits, TODOs, external deps. Module summaries and key
symbols come from agy calls with per-call timeouts. Staleness: >5 commits or
>7 days triggers auto-refresh in plan/build flows. `Staleness(proj, idx)`
reports staleness for an already-loaded index without re-reading from disk
(same age-then-commits rule); `IsStale(proj)` wraps it for callers that only
have a project, loading the index first. `render.go` renders the
index to markdown and writes `<project>/.claude/context.md` (or
`context.styx.md` + `@import` when a user-authored context.md exists) so
Claude Code auto-loads project context.

## Memory (internal/memory)

Long-term memory is stored in SQLite databases under
`~/.config/styx/state/memory/`: `<id>.db` for per-project memory and
`global.db` for shared cross-project memory. Each store has a `memory` table of
typed items (`decision`, `todo`, `distillation`, `brief`, `fact`, or
`routing-preference`) with source metadata, provenance columns (`project`,
`scope`, `confidence`, `last_used_at`), creation time, and a float32 embedding
packed as a little-endian blob. Old memory DBs are migrated additively on open;
unset confidence defaults to `1`, while one-off routing preferences enter at
lower confidence and may carry a scope hint such as `reviews`. The store API
supports open, close, insert, and newest-first full scans. `Recall` embeds a
query and ranks items across one or more stores by brute-force cosine
similarity weighted by confidence and recency (`confidence * 0.5^(age/90d)`),
so stale or low-confidence corrections fade at personal scale. In a
multi-project REPL session, recall spans every bound repo's store plus the
global store, giving cross-repo recall without an explicit scope hint. `Embedder`
abstracts text to float32 vectors; the production `OllamaEmbedder` posts to
`/api/embed` with a 30s HTTP client timeout and caller-provided context.

## Audit (internal/audit)

Per-session REPL audit trails are append-only JSONL files under
`~/.config/styx/state/audit/<id>/YYYYMMDD-HHMMSS.jsonl`. Each record has
an RFC3339 timestamp, kind, detail, optional string metadata, and a `Project`
field (the stable project ID of the repo actually touched). A single session
audit stream can therefore span multiple bound repos, with each record attributed
to the project it touched. `/audit` tails the last 20 records and renders the
project tag alongside each entry.

## Pipelines (internal/pipeline + cmd/styx/auto.go)

`styx auto <goal>` runs 7 stages: research → intel → plan → execute → test →
review → ship. State persists at `<project>/.styx/runs/<run-id>/state.json`
after every stage; a lock file prevents concurrent runs; `auto --resume`
re-enters at the first non-completed stage. `State.Version` (additive,
`omitempty`, current value `StateVersion = 1`) records the schema generation;
`LoadState` normalizes missing/zero versions to `StateVersion` on read so
pre-v1 `state.json` files load without error — the forward-compat contract
for `--resume`. Stage behaviors are closures on
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
rounds. The command routes drafter and critic separately and passes each
decision's model and optional effort through `channel.Request`. `Parse` accepts
strict JSON, embedded JSON, or keyword sections, and falls back to treating
garbage as one IMPORTANT finding (never silently converges); parse fallback
errors are surfaced through progress/status instead of being swallowed.
`deep.go` extracts cited URLs, fetches (80KB cap), and appends a Sources
Appendix. Before fetching, `hostBlocked` rejects non-http(s) schemes and
private/loopback/link-local hosts (SSRF guard on the citation chaser, e.g.
`169.254.169.254` cloud metadata, `127.0.0.1`, RFC1918 ranges, `.local`); the
`ChaseSources` loop reports a blocked URL as a distinct "skipped" outcome
(never silently) alongside its existing succeeded/failed narration and
tallies. `hostBlocked` only vets the initial URL, so `curlFetch` (the
extracted curl invocation used by `AgySummarizer`) runs curl with
`--max-redirs 0`: a page that 302s to a private/loopback/link-local host
(bypassing the guard) makes curl fail hard (exit 47) instead of silently
returning the redirect target's body, so a redirect always surfaces as a
fetch failure, never a silent success with wrong content. The truncated page
body is embedded in the summarize prompt via
`buildSummarizePrompt`, fenced between `BEGIN UNTRUSTED CONTENT`/
`END UNTRUSTED CONTENT` markers with an explicit instruction to treat it as
data, not instructions (prompt-injection mitigation: fetched pages are
attacker-controlled input). `brief` writes timestamped briefs/plans into the
project's configured dirs and resolves the most recent brief.

## Execute (internal/execute)

`Apply` applies a plan autonomously with an "implement this plan" prompt. When
`Options.Channel` is set (the router picked codex for `implement`), it routes
through that channel with `Write: true` and captures output; when nil it uses
the built-in claude path (`--dangerously-skip-permissions -p`), which streams
claude's stderr live. `Ship` handles commit/push/PR (via `gh`), honoring
`--no-pr`/`--no-push`.

## Shipgate (internal/shipgate)

Server-side confirmation for ship-risk MCP actions — commit/push/PR — before the MCP server executes them. The gate is isolated from styx business logic (stdlib only) so it holds for any MCP host. Supports three modes: `handshake` (default) relays a single-use token through the brain for user confirmation; `tty` prompts on `/dev/tty` directly, bypassing the brain; `off` allows all actions (scripting). Tokens expire after 10 minutes and are bound to their action — reuse is denied, and a token for one action does not unlock another. See conductor spec §6.

## Launcher (internal/launcher)

The conductor front door: opens a frontier-brain host session (Claude Code
first) with styx attached as an MCP toolbelt. `Host` (`Name() string`,
`Launch(ctx, Opts) error`) is the seam for future hosts; `ClaudeHost{Bin
string}` (empty `Bin` means `"claude"` on `PATH`) is the only host-specific
code in the conductor — everything else downstream is portable MCP surface
(`internal/mcpserver` + the conductor tools). `Opts{ProjectPath, StyxBin,
Guidance, ExtraRepos, ExtraArgs}` is everything a host needs.
`ClaudeHost.Launch`:
1. resolves `paths.StateDir()` and `paths.EnsureDir`s it;
2. writes `{"mcpServers": {"styx": {"command": StyxBin, "args": ["mcp"]}}}`
   to `<stateDir>/conductor-mcp.json` via atomic tmp+rename;
3. execs `claude --mcp-config <path> --append-system-prompt <Guidance>`
   (plus `--add-dir <repo>` per `ExtraRepos`, then any `ExtraArgs` verbatim —
   `styx resume` uses this for `--resume <id>` / `--continue`) via
   `exec.CommandContext` with
   `cmd.Dir = ProjectPath` and stdio passed through directly (`cmd.Stdin`,
   `cmd.Stdout`, `cmd.Stderr` = the process's own), so the user drives the
   resulting Claude Code session interactively; the launch call returns only
   when that session exits.

**Conductor data flow.** `cmd/styx/launch.go`'s
`launchConductor(a, repos, extraArgs)` is the only caller (reached via
`cmdLaunch(a, repos...)` and `cmdResume(a, sessionID)`, the latter passing
`--resume <id>` / `--continue` as `ExtraArgs`): it resolves the focus project
(`target.Resolve` on the
first repo, or `resolveGlobalTarget("")` for bare `styx`, falling back to
the plain cwd when that fails with `ErrNotInGitRepo` and no explicit
target was given), loads
`internal/guidance.Load(project.Path)` for the base system-prompt content,
appends a note about any extra repos and `recallRoutingPrefs(a)`'s learned
routing-preference memories, resolves the running binary via
`os.Executable()` (so the spawned Claude Code always shells back out to
*this* styx, not a stale `PATH` copy), and calls `ClaudeHost.Launch`. Once
Claude Code is running, it talks back to styx exclusively through the MCP
server it just configured (`styx mcp`, started as a subprocess by Claude
Code itself per the written config — see "MCP server" and "Conductor MCP
tools" below): `route`/`budget_status`/`channel_health`/`get_intel`/
`refresh_intel`/`recall` for the routing brain and memory, `dispatch`/
`thread_status` for delegating to persistent claude/codex/agy/ollama
threads, and `memory_save`/`pipeline_run` for writing memories and running
the research/review/intel/auto pipelines (the last gated by
`internal/shipgate`). No code path in the launcher itself talks to a
provider API or the MCP protocol — it only shells out to the `claude` CLI
and writes a config file for it to read.

## MCP server (internal/mcpserver + cmd/styx/mcp.go + cmd/styx/mcp_conductor.go)

A transport-only JSON-RPC-over-stdio MCP server (`styx mcp`) exposing the
routing brain and the conductor dispatch surface as eleven tools for MCP
hosts like OpenClaw or Claude Code: `route`, `budget_status`, `record_usage`,
`channel_health`, `get_intel`, `refresh_intel`, `recall`, `dispatch`,
`thread_status`, `memory_save`, and `pipeline_run`. Pure stdlib, no provider
SDK; stdout carries the protocol,
status stays on stderr. `cmd/styx/mcp.go` adapts tool args onto
`internal/router`, `internal/budget`, `internal/intel`, and `internal/memory`.
`cmd/styx/cmdMCP` builds the tool set as `append(mcpTools(a),
conductorTools(newConductorDeps(a))...)`.

**`route` v2 additive fields.** `routeResult` gained five fields that v1
consumers safely ignore (all `omitempty` except `blocked_by_budget`):
`classified_signals` (the signal slice actually used for routing — either the
caller's `signals` or, when omitted, whatever `signals.Extract` derived from
the task, so a consumer can see what drove the decision), `floor`, `tier_plan`
(`{acceptable, chosen, escalate_to}`), `blocked_by_budget`, and
`retry_after_s` (populated only when `blocked_by_budget` is true — the
smallest positive `RetryAfter` across the acceptable targets' channels, via
`minRetryAfter`, or 0 if unknown). Two value-shape decisions consumers must
respect:
- **`floor` is a bare capability-tier keyword** (`local` | `haiku` | `sonnet`
  | `opus`), never a `channel:model` string — it names a minimum rank on
  `signals.Tier`, not a specific target. `tier_plan.acceptable`/`chosen`/
  `escalate_to`, by contrast, ARE `channel:model` strings (or bare channel
  tokens) naming actual routing targets.
- **`channel_health.error_kinds` uses friendly, zero-filled keys** —
  `timeout`/`rate_limit`/`server`/`other` — mapped from the raw stored
  `429`/`5xx`/etc. labels via `budget.healthKind`; a consumer can always index
  all four keys without a presence check.

**Four new tools.**
- `channel_health` — per-channel (or all four) circuit-breaker state,
  recent failure count, the zero-filled error-kind buckets above, and
  remaining cooldown seconds, read straight off the existing usage log (no
  new state). Backed by `budget.Tracker.ChannelHealth`.
- `get_intel(project, section?)` — returns the persisted per-project intel
  index (or one named section: `conventions`, `key_symbols`, `modules`,
  `file_tree`, `recent_commits`, `open_todos`) plus `stale`/
  `staleness_reason`. A read never rebuilds; a missing index reports
  `stale: true` with reason `"no index built yet"` rather than erroring.
- `refresh_intel(project)` — the deliberate write path: rebuilds the index
  via `intel.Build` (agy module/key-symbol summaries) and rewrites
  `.claude/context.md`.
- `recall(project, query, k?)` — returns decayed top-k project + global
  memory hits (`memory.Recall`, confidence × recency decay). Degrades loud:
  an unavailable local-Ollama embedder becomes a `channel.ClassifiedError`,
  never an empty result presented as success.

`get_intel`, `refresh_intel`, and `recall` all resolve their `project`
argument through `resolveProjectStrict`, which has no cwd fallback (an MCP
server's cwd is not the caller's project) and turns an empty/unknown project
into a `channel.ClassifiedError` rather than a silent default — the shared
cross-cutting contract for every project-scoped v2 tool.

## Conductor MCP tools (cmd/styx/mcp_conductor.go)

`dispatch` and `thread_status` give a frontier-brain MCP consumer (Claude
Code, per the conductor spec) a dispatch surface onto the same
`internal/agent` thread machinery the REPL uses, without going through the
REPL loop.

- `conductorDeps` (`a *app`, `gate *shipgate.Gate`, `emb memory.Embedder`,
  and a mutex-guarded `managers map[string]*managed` cache keyed by project
  ID) is built once per `styx mcp` invocation via `newConductorDeps(a)`.
  `conductorTools(d)` returns four tools: `dispatch`, `thread_status`,
  `memory_save`, and `pipeline_run`.
- `conductorDeps.managerFor(alias)` lazily binds a project exactly the way
  `replSession.bind` does (`cmd/styx/repl.go`): opens `<memDir>/<projectID>.db`
  via `memory.Open`, loads `agent.LoadThreads(projectID)`, wires the
  claude/codex/agy adapters (`agent.NewClaudeAdapter/NewCodexAdapter/
  NewAgyAdapter`), the shared budget tracker, an ollama-backed `Summarize`
  closure for distill-and-restart, and the `[budget.claude].timeout_minutes`
  subprocess timeout (default 10m). Like `resolveProjectStrict`, resolution
  goes through `target.Resolve(target.Spec{Alias: alias})` with **no cwd
  fallback**; an empty or unregistered alias is a loud error, not a
  server-side default project.
- `dispatch(project?, thread?, cli, message, model?, risk, extra_roots?,
  confirm_token?)` — `cli` is one of `claude|codex|agy|ollama`; `risk` is
  `read|edit|ship`. Validation (unknown cli, empty message, invalid risk)
  runs first and returns a plain error. For `risk: ship`, the
  **`internal/shipgate` check runs before project resolution** — a ship-risk
  call against an unbound project still gets gated, never silently resolved
  first. `cli: ollama` is a one-shot call through `a.channels["ollama"]`
  bypassing thread machinery entirely (no project needed); an empty `model`
  defaults to `a.routing.Brain.Model` (falling back to the hardcoded
  `qwen2.5-coder:7b` if that's also empty), so omitting `model` no longer
  hits `ollama 400: model is required`. It records a
  `{Channel: "ollama", Verb: "one-shot"}` event — with the resolved model,
  not the raw request field — on the budget tracker for
  success and failure alike (record errors are narrated via `logStatus`,
  never fail the dispatch), so local one-shots show up in `styx budget`
  like every other channel. Otherwise the
  call routes through `managerFor` + `agent.Manager.Dispatch`, returning
  `{thread, cli, text, tokens_in, tokens_out}` (`thread` defaults to `cli`
  when unset, matching `Manager.Dispatch`'s own thread-naming default) or,
  when gated and denied, the raw `shipgate.Result` (`{allowed, token,
  message}`) so the brain can relay the confirmation token to the user.
- `thread_status(project?)` — resolves the project via the same
  `managerFor` and returns `{threads: []string}` from
  `agent.Manager.StatusLines()` (name, CLI, turn count, context-window
  percent per thread). `StatusLines()` guarantees a non-nil `[]string{}`
  when a project has no threads, so the JSON shape is always
  `{"threads": []}`, never `{"threads": null}` — MCP consumers can rely on
  the key always being an array.
- `memory_save(project?, kind, text, scope?)` — validates `kind` against
  `memory.KindFact/KindDecision/KindTodo/KindRoutingPreference` (any other
  value errors loudly) and requires non-empty `text`, then embeds via
  `d.emb.Embed` and writes through `managerFor(project).mem.Add` with
  `Source: "conductor"`, `Confidence: 0.9`, and `scope` defaulting to
  `"project"`. Returns `{saved, id}`.
- `pipeline_run(pipeline, arg?, confirm_token?)` — `pipeline` is one of
  `research|review|intel|auto`; an unknown value is rejected **before** the
  ship gate so it errors loudly regardless of gate mode. `auto` (which
  ships: branch→push→PR) then runs the same `internal/shipgate` handshake
  as `dispatch` risk=ship, keyed `"pipeline:auto"` — denied gates return the
  raw `shipgate.Result` for the brain to relay. The calls mirror the REPL's
  `pipelines` map (`cmd/styx/repl.go` around line 625) exactly: `research`
  → `cmdResearch(d.a, []string{arg})` then, on success, `indexNewestBrief`
  into the project's memory store (best-effort like the REPL's entry;
  failures are narrated via `logStatus`, never fail the completed
  research); `review` → `cmdReview(d.a, nil)`; `intel` → `cmdIntel(d.a,
  []string{proj.Name})`; `auto` → `cmdAuto(d.a, []string{arg})`. Where the
  REPL uses its focused project, `pipeline_run` uses the server's **cwd
  project** via `resolveGlobalTarget("")` (the launcher starts `styx mcp`
  in the project dir) — the same resolution research/review/auto perform
  internally. The project-scoped tools (`dispatch`, `thread_status`,
  `memory_save`) keep the strict no-cwd-fallback `managerFor(alias)`
  contract; `managerForProject` binds an already-resolved project for the
  research indexing step. On success returns `{pipeline, done: true, note}`
  pointing at `styx/research/` and `styx/plans/` for artifacts.

## Progress (internal/progress)

TTY-aware narrator: animated braille spinner on a terminal, plain lines
otherwise, no-op when quiet. One `Tracker` per invocation; `Stage` lifecycle
is Done/Fail/Info, opening a stage implicitly closes the previous one. All
lines prefixed `[styx]` on stderr.

## Secrets (internal/config/secrets.go)

macOS Keychain under service `styx`; `migrate-secrets` verb moves plaintext env
vars out of shell rc files. For each secret-shaped export (matching
`_API_KEY|_TOKEN|_SECRET`), the verb prompts the user to confirm, stores the value
in Keychain, **deletes the line entirely** from the rc file (no commented copy;
removal is per-occurrence — a declined duplicate of an identical line survives),
writes a one-time `<rc>.styx-bak` backup (0600) if not already present, and sets
the rc file to 0600 perms (atomic tmp+rename). After successful migration, prints
a note that old values may survive in shell history and Time Machine — users
should rotate the migrated keys.

## On-disk layout

```
~/.config/styx/routing.toml                 routing rules + caps (user-edited)
                                             plus brain/tier defaults for REPL routing
~/.config/styx/models.json                  model discovery cache timestamp + results
~/.config/styx/projects.toml                project registry (auto-managed)
~/.config/styx/state/usage.db               sqlite usage log (WAL + busy_timeout)
~/.config/styx/state/memory/<id>.db         per-project memory sqlite
~/.config/styx/state/memory/global.db       shared cross-project memory
~/.config/styx/state/audit/<id>/*.jsonl     per-session REPL audit trails
~/.config/styx/state/threads/<id>.json      agent-thread state
~/.config/styx/state/intel/<id>/index.json  per-project codebase intel
<project>/.claude/context.md                rendered intel (Claude Code loads it)
<project>/.styx/runs/<run-id>/state.json    pipeline state
<project>/styx/research, styx/plans         briefs + plans (per-project config)
```

## Testing conventions

Table-driven tests with `t.Run`; `httptest` fakes for ollama; channel/router
tests use in-memory stubs (`BudgetSource`, fake channels); `testdata/` holds
fixtures (`routing/`, `brain/`, plus `fakeagent` once agent threads land).
`TestRoutingAccuracy` is env-gated behind `STYX_BRAIN_IT=1` and runs the real
local ollama brain against `testdata/brain/utterances.json` (192 labelled utterances, 8 also
carrying a `want_risk` label); it reports routing accuracy, risk-emission accuracy, and a folded
gate accuracy, and should be run only where ollama is up and the brain model (`qwen2.5-coder:7b`) is
pulled. `make test` = `go test ./...`.
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
