---
owns:
  - "cmd/styx/**"
  - "internal/**"
  - "testdata/**"
  - "eval/**"
  - "e2e/**"
last_verified: 2026-07-11
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
`learn.go`, `repl.go`, `launch.go`, `runs.go`, …).
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
  agent threads get the same access → fire background graph builds for stale bound repos
  (`ensureGraphsFresh`, see Graph section) → loads `internal/guidance.Load(project.Path)`,
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
- `learn.go` — `cmdLearn(a *app, args []string) error`, the `styx learn`
  verb surface (second-tier switch, right after `intel`). Flags:
  `--scorecard` (renders `internal/learn.Build(rows, 30).Render()` over
  `a.tracker.OutcomesSince` for the trailing `scorecardWindow` = 30 days, no
  memory store touched), `--list` (renders `KindRoutingPreference` +
  `KindUserPreference` items via `memory.Store.TopByKind`, with id/source/
  date/confidence so entries are addressable), `--forget <id>` (hard-deletes
  via `memory.Store.Delete` — the reversibility guarantee), and bare
  `styx learn`/`--dry-run` which route to `runLearn`, a deliberate
  not-implemented stub (`"styx learn digest not implemented yet — use
  --scorecard, --list, or --forget"`) until the digest lands. `--list` and
  `--forget` share `openGlobalMemory()`, which opens (creating if needed)
  `~/.config/styx/state/memory/global.db` — the same global store the
  launcher's `recallRoutingPrefs` reads for guidance injection. Unknown
  flags and a missing/non-numeric `--forget` id error immediately, naming
  the bad flag/id.
- `repl.go` — the conversational frontend and session core, now reached via
  `styx repl` rather than bare `styx`. `cmdREPL` runs the persistent loop with
  `/status`, `/budget`, `/threads`, `/watch`, `/why`,
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
  Dispatch observability (Task 8): the session holds a `board *activity.Board`
  wired into every bound `Manager` (`Board: s.board`) so every agent turn —
  sync or background — records liveness. `/watch` renders
  `activity.Render(board.Snapshot(), board.WatcherNote(), watchStall(), now)`.
  On construction the session starts an `activity.Watcher` goroutine (off a
  session-lifetime `ctx`/`cancel`, cancelled in cleanup) gated on
  `routing.Watch.OllamaEnabled`, summarizing cross-agent liveness into the
  board note. `runDispatches` streams a single dispatch inline as before, but
  the parallel fan-out (>1 dispatch, board present) runs "quiet" — `printEvent`
  suppressed — under an inline `activity.LiveRenderer` so a TTY's in-place
  repaint never collides with streamed text; each agent's final `res.Text` is
  printed after `LiveRenderer.Stop()`. `runOneDispatch` gained a `quiet` param
  and returns that final text (empty when streaming inline). Watch config
  helpers (`watchModel`/`watchInterval`/`watchStall`) read pre-extracted
  routing fields with defaults so test sessions (no routing) fall back to
  `activity.DefaultStall`/15s.
  Disk mirror (Task 9): the session also holds `mirror func() error`, built in
  `newREPLSession` via `activity.MirrorThrottle(s.board,
  <StateDir>/watch/<seed.ID>.json, 2*time.Second)` — keyed by `seed.ID`, the
  project resolved from cwd at session construction, not the mutable
  `s.focus` (see the Activity section for why that's the path-consistency
  guarantee `styx watch` relies on). `mirrorNow()` calls it and narrates any
  write error via `logStatus`; it's called from `printEvent` (every streamed
  event) and, during the quiet parallel span where `printEvent` never fires,
  from a dedicated one-second `startMirrorTicker` goroutine bracketing the
  `LiveRenderer` span in `runDispatches`.
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
  `cmdMCP(a *app, args []string)` derives a cancellable root context
  (`ctx, cancel := context.WithCancel(...)`, `defer cancel()`), builds
  `d := newConductorDeps(a, ctx)` (rooting the background-task registry on that
  ctx — no daemons), and constructs the server via `mcpserver.New("styx",
  mcpServerVersion, append(mcpTools(a), withBackgroundStatus(conductorTools(d), d.reg)...))`
  — the conductor tools are wrapped so every map result carries the background
  status line (see the Piggyback note under "Conductor MCP tools"). It
  logs readiness to stderr via `logStatus` (naming all fourteen tools), and runs
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
  can label them. Classification is platform-split: SIGKILL/SIGTERM detection
  lives in build-tagged `internal/channel/exit_unix.go` / `exit_other.go`
  (`channel.KilledBySignal`), and each adapter's `classifyExecError` is
  context-aware so on Windows (no POSIX signals) a dead context classifies the
  killed subprocess as a timeout. agy is headless-only and always passes
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
  Ollama app with a 20s wait if it's down. Every chat request carries
  `keep_alive: "30m"` (ollama's default 5-minute idle unload otherwise forces
  a 3-10s cold reload on the next call); when the estimated prompt tokens
  (`len(prompt+system)/4`) plus 1024 headroom exceed ollama's 4096-token
  default, `Send` also sets `options.num_ctx` to the estimate plus 2048 so
  large prompts aren't silently truncated.
- `decorator.go` — `WithProgress` narrates each Send as a progress stage;
  skipped for interactive sends (spinner would fight the child for the TTY).
  `WithTimeout` gives non-interactive sends a deadline while leaving
  interactive handoffs unbounded.
- `gemini` is a registered alias for agy (v0.1 routing-rule compat).

## Routing (internal/router, internal/signals, internal/config/routing.go)

`routing.toml` (`~/.config/styx/`) parses into `config.Routing{Budget, Rules,
Models, Brain, Conductor, Watch, Tiers}`. Rules match on `verb` + required `signals`; **first match
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
ship-risk confirmation for `dispatch(risk=ship)` and `pipeline_run auto`;
`route_gate`: block | audit | off, default block, controlling the host-hook
enforcement of dispatch-over-inline routing — see the `styx hook` section below; and
`max_background_tasks`, the concurrent background-dispatch cap for the task
registry, default 4, seeded in `default_routing.go` and injected into
pre-B1 configs by `config.EnsureConductorTaskCap` — idempotent, respects an
already-customized `max_background_tasks` at any value, and appends a whole
`[conductor]` section when none exists);
`Tiers` maps brain tier names to claude CLI model aliases; `fable` maps to `fable`
again (the top tier, callable since mid-2026 after the 2026-06-12 suspension —
`config.EnsureFableTier` migrates suspension-era configs that still pin the seeded
`fable = "opus"`, leaving user-customized mappings alone).

`[watch]` (`config.WatchCap{StallThresholdSeconds, IntervalSeconds,
OllamaEnabled}`) configures live dispatch observability for `styx watch`
(`internal/activity`, see below): `StallThreshold()` returns the idle
duration past which an agent is flagged stalled (default 90s when
`StallThresholdSeconds <= 0`), `Interval()` returns the ollama-watcher poll
cadence (default 15s when `IntervalSeconds <= 0`), and `OllamaEnabled` gates
whether the watcher starts at all. Seeded into new installs by
`default_routing.go` (`stall_threshold_seconds = 90`, `interval_seconds =
15`, `ollama_enabled = true`) and injected into pre-C5 configs by
`config.EnsureWatchSection` — idempotent, appends the whole `[watch]` block
only when no `[watch]` section exists yet, leaving any existing section
(default or user-customized) untouched.

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
through `seedV5` constants — `seedV3` is the pre-async-dispatch seed, shipped
2026-07-07, kept verbatim from the live `Seed` at the moment background
dispatch/collect/rate_dispatch were added; `seedV4` is the pre-route-gate seed,
kept verbatim from before the "## Gated tools" section; `seedV5` is the
route-gate-era seed, kept verbatim from before the learning-loop nudges below)
is recognized as unmodified and transparently upgraded to the current `Seed`
on load. `Load(projectPath string)` returns the global guidance with an
optional per-repo override appended from `<repo>/styx/guidance.md` if it
exists. The `Seed` constant contains the default shipped guidance: a
dispatch-by-default rule (substantive work — implementation, research,
review, large summarization — goes through `dispatch`/`pipeline_run` rather
than the host's built-in Agent/Task subagents, which burn the interactive
session's Claude quota invisibly to styx's budget ledger; built-ins are
reserved for work too small to brief), a gated-tools section (WebSearch,
WebFetch, Task subagents, and external-fetch Bash/MCP tools are blocked by
route-gate design, not a bug — redirect to dispatch/pipeline_run), a
research-task mapping (`pipeline_run research` for brief-producing research,
`dispatch cli=claude` for repo-focused investigation, agy when very large),
channel best purposes (codex as primary implementer for well-scoped work,
claude for ambiguous/architectural/refactor work, agy for large-file explains,
ollama for trivial one-shots), model tier guidance, a background dispatch
section (fire independent multi-minute work with `background: true` for an
immediate `task_id` and keep working; call `collect` before synthesizing
results or on user status requests; same-thread/same-project edit-risk tasks
queue rather than parallelize; `risk=ship` never backgrounds; orphaned tasks
are reported if the mcp session ends), a rating-outcomes section (call
`rate_dispatch` with a thread/task id and one-line note on notably good or bad
outcomes, feeding styx's learning loop), working style conventions (plan
before dispatch, reuse threads, consult memory, check budget, and — new in
the learning-loop task — two explicit save nudges: memory_save an explicit
durable statement of the user's ("remember I prefer X") as kind=user-
preference immediately, no digest needed; and memory_save a 2-line what-
worked/what-didn't retrospective as kind=retrospective at natural session
endpoints, which is digest fuel for `styx learn` and is never injected into
guidance directly), and ship policy (confirmation token handoff).

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
structured-output format. `chat()` sends `keep_alive: "30m"` on every brain
call (avoids the 3-10s cold reload after ollama's 5-minute idle unload) and
sets `options.num_ctx` to `(len(system)+len(user))/4 + 2048` whenever that
estimate plus 1024 headroom would exceed ollama's 4096-token default — the
~40-exemplar preamble routinely sits near/over that default (measured ~3.3k
tokens for a minimal turn in `TestBrainPromptFitsDefaultContextOrSetsNumCtx`).
Capability cards describe claude, codex, agy, and
ollama on every brain turn; `styx doctor` uses the same cards as drift probes
for expected CLI flags and resume support. Drift probing checks each card's
`ExpectedFlags` against the CLI's `--help`; a non-dashed entry (e.g. `exec`) is
treated as a subcommand, so doctor also runs `<bin> <sub> --help` and searches
the union — this is how `codex exec --json` verifies even though `--json` never
appears in top-level `codex --help`. Cards without subcommand entries (claude,
agy) trigger no extra help invocation. `BuildPrompt` combines those cards
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
headless `stream-json` mode with native session resume (`--resume`), verbose
JSON output, and pre-granted permissions matching the existing execute path.
`ClaudeAdapter.ContextWindow()` defaults to the real 1M-token window that
Opus/Sonnet/Fable run on the API and Max plans, honoring Claude Code's own
`CLAUDE_CODE_DISABLE_1M_CONTEXT=1` opt-out (200k when set); the adapter's
`Window` field still overrides both for tests. `CodexAdapter` is a
resume-capable stream adapter: it drives `codex exec --json` with native
session resume (`codex exec resume <thread_id>`), captures exact per-turn token
usage from `turn.completed` events (never len/4 estimates), and defaults to a
400k-token window (`Window` overrides for tests). Edit-risk turns add
`--sandbox workspace-write` (codex exec is read-only by default); read-risk
turns keep the default. Agy remains a plain adapter with no native
resume/stream support: it runs `agy -p --dangerously-skip-permissions`, and
continuity is maintained by styx summaries (agy exposes `--continue`/
`--conversation <id>` but never surfaces conversation IDs in `--print` output,
so headless resume stays impossible).

The package defines the shared event shape and parses both Claude's and Codex's
stream protocols. Events carry a `Type` (`EventInit`/`EventText`/`EventTool`/
`EventResult`) plus a `Tool` field set only for `EventTool`. For claude:
`system/init` captures session IDs, assistant text chunks stream intermediate
output, `assistant` messages whose content is a `tool_use` block surface as
`EventTool` (`Tool` = the tool name, e.g. `Bash`/`Read`; `Text` = a best-effort
target pulled from the tool's `input` — command first line, file path, path,
URL, or search pattern, via `claudeToolTarget`/`firstLine`), and final `result`
events carry the answer plus real usage (normal input, cache creation input,
and cache-read input tokens). For codex (`ParseCodexEvent`): `thread.started`
captures the resumable `thread_id`, `item.completed` `agent_message` items
stream assistant text, any other non-empty `item.completed` type (e.g.
`command_execution`, `file_change`, `mcp_tool_call`) surfaces as `EventTool`
(`Tool` = the item type; `Text` = the item's `command` field, first line only,
when present), `turn.completed` carries exact usage (`input_tokens` +
`cached_input_tokens`, and `output_tokens`) but no text, and `turn.failed`
surfaces an error result. Context size is metered against each adapter's real
context window rather than rough character estimates. Hook and malformed
stream lines are still ignored.

Each project has a JSON thread store under
`~/.config/styx/state/threads/<id>.json`, keyed by the stable project ID rather
than the mutable registry name. Threads are named durable
conversations with a CLI, optional Claude session ID, rolling summary for
non-resume CLIs, last distillation checkpoint, context-token meter, turn count,
and update timestamp. Stores are created lazily and saved with tmp+rename.

`Runner` executes one turn by spawning the adapter's CLI with an optional
timeout and working directory. For stream-capable adapters it scans stdout
line-by-line, emits parsed events to the caller, captures session IDs, and
records real input/output token counts from the final result. Because codex's
`turn.completed` carries usage but no text (the text arrived in a prior
`item.completed`), the runner remembers the last streamed `EventText` and uses
it as the result text when the final event's text is empty. For plain adapters
(agy) it treats full stdout as the result and falls back to len/4 token
estimates until those CLIs expose structured usage. Every successful turn
updates the thread's context meter, turn count, and timestamp in memory; callers
persist the store after lifecycle decisions. `Runner.Board *activity.Board` and
`Runner.Label string` (both optional; `Label == ""` disables recording) let the
streaming loop write every parsed event to the shared liveness board via
`summarize(ev)` — a small switch rendering `EventInit` as "session started",
`EventTool` as `"<tool>: <target>"` (or just the tool name if there's no
target), `EventText` as "thinking", and `EventResult` as "finishing". This call
sits right after the existing `OnEvent` callback in the loop but does not
depend on it: `OnEvent` is nil on background dispatch (the conductor's async
path), so recording liveness in the Runner rather than in callers' `OnEvent`
handlers is what makes both sync and background dispatch observable through
one code path. `testdata/fakeagent` is an
executable stream-json fixture for runner and manager lifecycle tests, including
resume argument assertions and dead-session simulation; its `FAKEAGENT_SLEEP`
knob (seconds) sleeps once, before either protocol block emits, so tests can
hold a dispatch "running" long enough to observe it mid-flight (e.g. the B1
background-dispatch roundtrip in `cmd/styx/mcp_conductor_test.go`).

`Thread` and `ThreadStore` guard against the concurrency B1 background
dispatch introduces: a background task's goroutine can call `Manager.Dispatch`
on a thread while a synchronous `thread_status` call, or a second background
dispatch on a *different* thread of the same project, concurrently reads or
persists the same `*Manager`/`*ThreadStore` (they share one cached instance
per project — see "Conductor MCP tools" `managed`/`managerFor`; the
registry's ordering rules (`conflictLocked`) only serialize same-thread
tasks, so two different-thread dispatches on one project can run at once).
`Thread.mu` guards its own mutable fields (every
read/write outside construction takes it, including a `MarshalJSON` override
so `ThreadStore.Save` never reads a torn thread) and `ThreadStore.mu` guards
the `Threads` map structure (`Get`, `Save`, `StatusLines`, `Handoff`).
`ThreadStore.Save` holds `ts.mu` across its entire body — the marshal, the
`os.WriteFile` to its fixed `path+".tmp"`, and the `os.Rename` — so that
concurrent background-dispatch `Save` calls on one project's store serialize
their disk writes instead of interleaving on the same tmp path (which could
corrupt the tmp file or race the final rename). Locks are always released
before an external CLI subprocess call (`Runner.Send`) — never held across
it; the file write in `Save` is a local, millisecond-scale operation, not a
subprocess call, so holding `ts.mu` across it does not violate that rule.

`Manager` owns a project's thread lifecycle. `DispatchSpec` carries an
`ExtraRoots []string` field (absolute repo roots for cross-repo dispatch);
`Manager.Dispatch` renders them via `addDirArgs` into `--add-dir <root>` pairs,
merges them once into the `extra` slice, and passes that same merged slice at
both the first-attempt and crash-recovery `run.Send` sites. `CodexAdapter.
BuildArgs` places the merged `--add-dir` flags (and any brain-supplied extras)
after `exec [resume <id>] --json [--sandbox workspace-write]` and before the
message — the same arg-order rule as the channel layer (the installed Codex CLI
exposes `exec`, `resume`, `--json`, `--sandbox`, `--model`, and `--add-dir`).
`Dispatch` resolves the adapter,
creates the thread on first use, seeds fresh/restarted sessions with a project
role line or last distillation, runs the turn, records real token usage and the
routed model to the budget log under verb `thread`, maintains rolling summaries
for plain adapters, and saves the thread store. `Manager.Board *activity.Board`
(nil ok) is threaded into the per-dispatch `Runner` as `Board: m.Board, Label:
BoardLabel(m.ProjectID, name)` so every turn's events land on the shared
liveness board regardless of whether the caller passed an `onEvent` callback.
`BoardLabel(projectID, thread)` (exported) namespaces the board key as
`"<projectID>/<thread>"` so the single board a conductor server or REPL session
shares across every bound project never cross-attributes two projects' like-named
threads (e.g. both running a `codex` task); renderers strip the `"<projectID>/"`
prefix for display (`activity.Render`). `Dispatch` stamps `start :=
time.Now()` on entry and, immediately after `m.record(...)` and before the
`err != nil` branch, calls `m.Board.Done(label, time.Since(start))` — one
placement that covers both the error and success return paths, so a failed
turn still clears the agent's "in flight" state on the board. A codex thread that predates
native resume (rolling `Summary`, no `SessionID`) seeds that summary into its
first resume-capable turn as a one-time transition. If a resume-capable CLI
reports a dead session, the manager clears the session ID and retries once using
the last distillation as the handoff seed. When a resume-capable thread crosses its
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

`internal/budget/outcome.go` adds a second append-only table, `outcomes`, in
the same `usage.db` — the learning substrate shared with the planned
self-improvement digest (`styx learn`, B1 async-dispatch spec). Its schema
(`id`, `ts`, `project`, `thread`, `task_id`, `cli`, `model`, `signals`,
`risk`, `duration_s`, `tokens_in`, `tokens_out`, `error_kind`, `background`,
`rating`, `note`) records one dispatch-completion row per call, independent
of the token-counting `usage` table. `RecordOutcome(ctx, Outcome)` appends a
row. `RateOutcome(ctx, ref, ok, note)` is the single sanctioned mutation: it
stamps `rating`/`note` onto the most-recent row whose `thread` or `task_id`
matches `ref` (`ORDER BY id DESC LIMIT 1`), erroring loudly on no match —
callers rate by handle, not by row id, and get last-dispatch semantics for
free. `OutcomesSince(ctx, since)` reads rows with `ts >= since.Unix()` newest
first (`time.Time{}` returns the full table, since its zero-value Unix
timestamp is far in the past). The `outcomesSchema` DDL runs idempotently in
`New`, right after the existing `usage`/`cooldown` schema exec and before the
v0.3/v0.4 `ALTER TABLE` migrations — additive, so it never touches existing
`usage.db` files' prior tables.

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
prefix -> existing-path -> error listing candidates. The existing-path step is
existence-gated: only an alias that resolves to a real on-disk path (a directory
via its git root, or a file/subpath under a registered project's tree) matches;
a non-existent alias errors rather than resolving via lexical containment. This
matters for `styx mcp` launched inside a registered project, where a typo'd
relative alias would otherwise silently resolve to the cwd project. It never
silently falls back to the cwd when an explicit target was given and failed.
`cmd/styx` wraps
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

## Graph (`internal/graph/`)

Keeps per-project **graphify** knowledge graphs fresh. Wraps the external
`graphify` CLI (tree-sitter code knowledge graph; `uv tool install graphifyy`)
— styx never parses code itself. Active iff `graphify` is on PATH; disable
with `STYX_GRAPHIFY=off`. No routing.toml surface.

- **Artifacts:** in-repo `graphify-out/graph.json` (graphify's only output
  location, and where graphify's own Claude Code skill/hook expect it).
- **State:** `~/.config/styx/state/graph/<project-id>/` — `meta.json`
  (schema_version, built_at, git_head; atomic write), `build.log`,
  `build.lock` (O_EXCL; expired after BuildTimeout=10m and reclaimed).
- **Staleness:** HEAD-exact — stale iff meta or artifact missing, or current
  git HEAD != recorded head. No age/commit-count tolerance: `graphify update .`
  is an incremental SHA256-cached pass, so rebuilds are cheap (unlike intel's
  LLM-priced builds). Empty-ID projects (unregistered plain dirs) are never
  stale.
- **Build:** `graphify update .` in the repo, ctx-bounded, output to
  build.log; this command bootstraps the initial build via pure AST parsing
  (no LLM/API key needed) and runs incremental updates on subsequent calls.
  graph.json must parse with ≥1 node before meta is recorded, so a failed
  build re-triggers on the next check.
- **Entry points:** `styx graphify <target> [--force]` / `styx graphify ls`
  (cmd/styx/graphify.go, synchronous); conductor launch auto-build
  (cmd/styx/launch.go `ensureGraphsFresh`, background goroutine, silent after
  the host owns the TTY — build output goes only to build.log). A build killed
  by session exit leaves stale meta and retries next launch.
  Graph artifacts land in the target repository's working tree (`graphify-out/`);
  users should add this directory to the repo's `.gitignore` or their global git excludes.

## Memory (internal/memory)

Long-term memory is stored in SQLite databases under
`~/.config/styx/state/memory/`: `<id>.db` for per-project memory and
`global.db` for shared cross-project memory. Each store has a `memory` table of
typed items (`decision`, `todo`, `distillation`, `brief`, `fact`,
`routing-preference`, `user-preference`, or `retrospective`) with source
metadata, provenance columns (`project`, `scope`, `confidence`, `last_used_at`),
creation time, a `consumed_at` timestamp, and a float32 embedding packed as a
little-endian blob. `user-preference` captures how the user likes to work
(fed by `styx learn`); `retrospective` holds raw session notes that are
digest fuel only — never injected into recall or prompts, they exist purely
for a later summarization pass to consume. Old memory DBs are migrated
additively on open (including `consumed_at`, which defaults to `0` — zero
value = unconsumed — so pre-existing rows on upgrade read as never-consumed);
unset confidence defaults to `1`, while one-off routing preferences enter at
lower confidence and may carry a scope hint such as `reviews`. The store API
supports open, close, insert, and newest-first full scans (`Add`, `All`), plus
learning-loop methods: `TopByKind(kind, k)` ranks same-kind items by the
recall decay curve with similarity fixed at 1 (`confidence * 0.5^(age/90d)`),
so the top preferences are the newest, most confident ones and drift resolves
itself; `UnconsumedByKind(kind)` returns items with a zero `ConsumedAt`,
oldest first, for a digest to work through in order; `MarkConsumed(ids)`
stamps `consumed_at` on a batch (empty slice is a no-op); `Delete(id)` and
`UpdateEvidence(id, text)` both error loudly on an unknown id (Delete backs
`styx learn --forget`'s honesty; UpdateEvidence is the digest's dedupe path —
a re-learned memory gets fresher text and `created_at` instead of a duplicate
row); `MostSimilar(kind, vec)` returns the same-kind item with the highest
cosine similarity (zero `Item`, similarity `0` when the store holds no items
of that kind) — the seam a caller uses to decide "update existing evidence"
vs. "add a new memory" before writing. `Recall` embeds a query and ranks
items across one or more stores by brute-force cosine similarity weighted by
confidence and recency (`confidence * 0.5^(age/90d)`), so stale or
low-confidence corrections fade at personal scale. In a multi-project REPL
session, recall spans every bound repo's store plus the global store, giving
cross-repo recall without an explicit scope hint. `Embedder` abstracts text to
float32 vectors; the production `OllamaEmbedder` posts to `/api/embed` with a
30s HTTP client timeout and caller-provided context, carrying
`keep_alive: "30m"` on every request so the embed model isn't evicted between
calls. Every `Embed` call site embeds a single text per user action (recall
query, `memory_save`, distillation, brief indexing, routing-preference
correction, session-end summary) — no call site batches, so `EmbedBatch` stays
unimplemented pending bulk-embedding intel indexing.

## Learning (internal/learn)

**Application**: launch-time guidance injection (`cmd/styx/launch.go`,
documented under "Launcher" below) is the entire application mechanism for
what this package writes. No other code path reads `routing-preference` or
`user-preference` memories — the digest below and `styx learn` only produce
and manage them; the conductor's system prompt is where they take effect,
folded in as two sections (`## Routing preferences (learned)` and `##
User preferences (learned)`) each time the conductor launches.

Deterministic scorecard aggregation layer over dispatch outcomes — no LLM
involvement, read-only on routing.toml and code. `Scorecard` groups
`budget.Outcome` rows (seeded by real dispatch history) into `Cell` structs
(one per cli × signal pair), computing tallies (attempts, clean rate), medians
(duration, tokens), and rating counts. A row with N signals contributes to N
cells; a row with no signals lands in a `"(none)"` cell. Clean = no
classified error *and* not rated bad. The scorecard is the mechanical evidence
ground truth for digest citations (`scorecard:<cli>/<signal>`) and feeds both
the learning digest (Task 5-6: ollama-backed summarization of scorecard +
retrospectives into preference memories) and styx learn --scorecard human
inspection.

**Digest client** (`internal/learn/digest.go`): The `Digester` type wraps a
local ollama `/api/chat` endpoint and uses structured output to propose
candidate memories from a scorecard, unconsumed retrospectives, and rating
notes. Its chat shape (`Model`, `Stream`, `Think`, `Format`, `KeepAlive`,
`Options`, `Messages`) closely mirrors `internal/brain`'s shape to leverage
the same ollama tuning (30-minute keep-alive, dynamic `num_ctx` sizing based
on system + user prompt length, temperature 0 for deterministic classification)
and the same local brain model (default `qwen2.5-coder:7b`). Unlike the brain
which is REPL-coupled and ships with the conductor, the digest client is
fully self-contained and invoked on demand by `styx learn` without requiring
a REPL or brain session. `Propose(ctx, scorecard, retros, ratingNotes)` fails
loudly when ollama is unreachable — never returns empty silently; any upstream
failure is surfaced wrapped with context.

**Candidate schema**: `Candidate{Kind, Text, Confidence, Evidence}`. `Kind`
is an enum (`routing-preference` or `user-preference`); `Text` is a standalone
plain sentence for injection into future guidance; `Confidence` is a float in
(0,1] indicating certainty; `Evidence` is exactly one citation — either
`scorecard:<cli>/<signal>` naming a real scorecard cell or `retro:<id>` naming
a gathered retrospective id shown to the model. The schema enforces these
constraints at the ollama structured-output layer.

**Evidence guard** (`FilterByEvidence`): Mechanical hallucination filter that
rejects candidates before any write, dropping those with:
- kind not in the whitelist (`routing-preference`, `user-preference`)
- empty or whitespace-only text
- confidence outside (0,1]
- evidence citations that do not name a real scorecard cell (parsed as
  `scorecard:<cli>/<signal>`, verified against `Scorecard.HasCell`)
  or a real retrospective id (parsed as `retro:<id>`, verified against the
  provided `[]RetroNote`)
- evidence in any other format (neither scorecard nor retro prefix)

Survivors are capped at `maxCandidates = 5` (a hallucination bound: at worst 5
bad sentences, each still evidence-checked and printed). Drop reasons are
human-readable, one per dropped candidate.

**Verb surface** (`cmd/styx/learn.go`, see above): `styx learn --scorecard`
renders the table above with no memory store touched; `styx learn --list`
and `styx learn --forget <id>` inspect/reverse the learned
`routing-preference`/`user-preference` memories the digest writes; bare
`styx learn` (with an optional `--dry-run`) now runs the full digest —
implemented as of Task 6. Manual only, by design: no daemon runs the digest
automatically.

**The digest pass** (`runLearnDigest` in `cmd/styx/learn.go`, called by the
thin production wrapper `runLearn` which wires a real `memory.NewOllamaEmbedder`
against `a.routing.Brain.EmbedModel` and a `learn.Digester` against
`a.routing.Brain.Model`, both pointed at `http://localhost:11434` — the same
literal every other ollama caller in `cmd/styx` uses; there is no shared
base-URL constant/helper in this codebase). One digest pass is six steps:

1. **Scorecard** — `learn.Build` over `a.tracker.OutcomesSince(now -
   scorecardWindow)`, same as `--scorecard`.
2. **Gather** — `store.UnconsumedByKind(KindRetrospective)` plus rated
   dispatch notes (`Rating`+`Note` on outcome rows) from the same window.
3. **Propose** — `dig.Propose(ctx, scorecard.Render(), retros, notes)`
   against the local ollama brain model; an unreachable/erroring ollama
   returns here and the whole pass aborts with a wrapped error — nothing
   downstream runs.
4. **Evidence guard** — `learn.FilterByEvidence` drops any candidate whose
   citation doesn't name a real scorecard cell or gathered retrospective;
   every drop is printed (`dropped: "<text>" — <reason>`), never silent.
5. **Dedupe** — see below.
6. **Write + consume** — see below.

**Plan-before-write (partial-failure safety)**: after the evidence guard,
*every* surviving candidate is embedded (`emb.Embed`) and dedupe-checked
(`store.MostSimilar`) into an in-memory `plannedWrite` list *before any
store write happens*. If ollama's embed call fails on any candidate midway
through planning, `runLearnDigest` returns a wrapped error immediately —
nothing has been written yet and no retrospective has been marked consumed,
so a flaky/ollama-down run never leaves partial state. This is the same
plan-then-commit discipline as `execute`'s apply step, applied to memory
writes instead of file writes.

**Dedupe**: for each planned candidate, `store.MostSimilar(ctx, candidate.Kind,
vec)` finds the closest same-kind existing memory. Cosine similarity ≥
`dedupeSimilarity` (`0.9`, `cmd/styx/learn.go`) means "refresh, don't
duplicate": the plan records the existing row's id instead of a new write.
Below threshold, the candidate becomes a new memory row.

**Write phase**: runs only after planning completes successfully (and is
skipped entirely in `--dry-run`, see below). For each planned write:
- **Dedupe hit** (`dupeID > 0`): `store.UpdateEvidence(ctx, dupeID, text)`
  overwrites the existing row's text with the refreshed provenance string —
  same row id, no new memory. Narrated as `refreshed <id> [<kind>]: <text>`.
- **New row**: `store.Add` with `Source: "styx-learn"`, `Scope: "global"`,
  `Confidence` carried verbatim from the candidate, and the embedding vector
  computed during planning. Narrated as `learned <id> [<kind>]: <text>`.

Written/refreshed text always has the provenance suffix appended to the
candidate's raw sentence: `<sentence> [learned-by styx-learn <YYYY-MM-DD>;
evidence: <citation>]` — e.g. `codex handles complex specced work well
[learned-by styx-learn 2026-07-07; evidence: scorecard:codex/complex]`. The
date is `time.Now()` at write time; the citation is the candidate's raw
`scorecard:<cli>/<signal>` or `retro:<id>` evidence string, preserved
verbatim so `--list` and future digest passes can trace it back.

**Consumed-marking timing**: retrospectives gathered in step 2 are marked
consumed (`store.MarkConsumed`) only *after* every planned write in the write
phase has succeeded — the very last step of a live (non-dry-run) pass. A
retrospective is never marked consumed by a dry run, and never marked
consumed if any write in the phase fails partway (the function returns the
wrapped error before reaching `MarkConsumed`).

**`--dry-run`**: stops immediately after the plan phase, before the write
phase. Narrates each planned action without doing it — `would learn [<kind>,
conf X.XX]: <text>` for new rows, `would refresh memory <id> [<kind>, conf
X.XX]: <text>` for dedupe hits — followed by `dry run: nothing written,
retrospectives left unconsumed`. Embeds and `MostSimilar` dedupe checks still
run during planning (so a dry run still requires ollama to be reachable and
still surfaces the same loud failure if it isn't), but the store is never
mutated and retrospectives stay unconsumed for the next real pass.

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

## Route gate — `styx hook` (cmd/styx/hook.go)

Shipgate's sibling for the *routing* decision. The problem: the conductor
(Claude Code) keeps doing substantive/research work **inline** with its own
built-in tools (WebSearch, WebFetch, Task subagents, Bash-curl) instead of
dispatching, silently burning claude quota and forfeiting cross-CLI arbitrage.
The MCP server cannot gate this — it only sees tool calls the host routes to
it, and inline self-handling never crosses the MCP boundary (this is why
`ship`, a styx tool call, *can* be gated but inline research cannot from the
MCP side). The one seam that observes the host's native tools is Claude Code's
hook system, and since styx launches Claude Code, the launcher installs
`styx hook` as a shell-free exec-form hook (`command` is the styx binary path;
`args` is `["hook", "<event>"]`) scoped to conductor sessions only — the
settings file lives in styx's state dir, never the user's `~/.claude`.

`styx hook <event>` is dispatched **before `loadApp()`** (no SQLite/config load)
so it stays fast on the per-tool-call hot path. It reads Claude Code's hook JSON
from stdin and is **fail-open**: anything not explicitly denied is allowed, so a
hook bug or malformed payload can never brick a session (which always has
`dispatch` as the recorded escape hatch). Two events:

- **`pretooluse`** — denies the crisp "substantive work I'm doing myself"
  markers with a redirect to `pipeline_run research` / `dispatch`: `WebSearch`,
  `WebFetch`, `Task`; `Bash` **only** when it shells out to fetch a remote
  http(s) host (curl/wget to non-localhost — a chain like `curl URL | sed`);
  and `mcp__*` tools whose name matches `(web|search|fetch|research|scrape|crawl)`
  (catches `mcp__exa__web_search_exa` while preserving Gmail/Calendar/Drive/
  context7). Emits `{permissionDecision:"deny", permissionDecisionReason:...}`.
- **`posttooluse`** — appends one JSONL record (`ts, session_id, tool, cwd`) to
  `<stateDir>/inline-activity.jsonl` so the previously-invisible inline burn is
  auditable by the self-improvement loop, plus a soft `additionalContext` nudge
  for high-signal tools. Deliberately **not** the budget ledger: one ledger row
  == one subscription *message* against the 5h/weekly windows, and a tool call
  is not a message.

Controlled by `[conductor] route_gate` (block | audit | off, default block):
`block` installs both hooks; `audit` installs only the PostToolUse recorder
(never blocks); `off` writes no settings file. The launcher's
`buildConductorSettings` is fail-closed on an unrecognized mode — anything but
`audit`/`off` installs the full block gate. The gate flips the model's default
(inline now costs more than dispatch for high-signal cases) but cannot make a
determined `Read`+`curl`+`Grep` chain impossible — the `Bash` matcher narrows
the curl case, the audit tier and guidance prose cover the fuzzy remainder.

## Launcher (internal/launcher)

The conductor front door: opens a frontier-brain host session (Claude Code
first) with styx attached as an MCP toolbelt. `Host` (`Name() string`,
`Launch(ctx, Opts) error`) is the seam for future hosts; `ClaudeHost{Bin
string}` (empty `Bin` means `"claude"` on `PATH`) is the only host-specific
code in the conductor — everything else downstream is portable MCP surface
(`internal/mcpserver` + the conductor tools). `Opts{ProjectPath, StyxBin,
Guidance, RouteGate, ExtraRepos, ExtraArgs}` is everything a host needs.
`ClaudeHost.Launch`:
1. resolves `paths.StateDir()` and `paths.EnsureDir`s it;
2. writes `{"mcpServers": {"styx": {"command": StyxBin, "args": ["mcp"]}}}`
   to `<stateDir>/conductor-mcp.json` via atomic tmp+rename;
2b. unless `RouteGate == "off"`, writes `<stateDir>/conductor-settings.json`
   (the routing-gate hooks — see the `styx hook` section) via atomic tmp+rename
   and passes it as `--settings`. Each command hook uses Claude Code's exec form:
   `command` is the styx binary path and `args` is `["hook", "<event>"]`, with
   no shell or quoting on any platform. Native-Windows shell-form hooks run
   under Git Bash with a PowerShell fallback, whose incompatible quoting rules
   make one portable command string impossible. We deliberately do NOT pass
   `--strict-mcp-config`: the user's other MCP servers stay available and the
   hook's matcher catches MCP web tools by name instead;
3. execs `claude --mcp-config <path> [--settings <path>] --append-system-prompt <Guidance>`
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
then assembles the final guidance via the pure `conductorGuidance(base,
focusName, extraNote, prefs, userPrefs string) string`: it appends a "This
session's project" section naming the focus project's registry alias (so the
brain knows what to pass as `project` on `dispatch`/`thread_status`/
`memory_save`; an empty `project` also resolves to this repo — see Task 4
above), then a note about any extra repos, then two learned-preference
sections — `## Routing preferences (learned)` from `recallRoutingPrefs()`
and, after it, `## User preferences (learned)` from `recallUserPrefs()` —
each omitted entirely when empty. Both helpers call the shared
`topLearnedPrefs(kind memory.Kind) string`, which opens `global.db` and
renders `Store.TopByKind(ctx, kind, 5)` (confidence × recency ranking, no
embedding involved) as a bullet list. This replaced an earlier
embedding-based `recallRoutingPrefs(a *app)` that built an
`OllamaEmbedder` and called `memory.Recall` with the literal query string
"routing preference" — a similarity search that could cross-match
`user-preference` memory text into the routing section (and vice versa) and
depended on ollama being reachable at launch time. The kind-exact
`TopByKind` switch fixes both: it is exact per-kind (no cross-contamination
between the two sections) and embedder-free, so guidance injection — the
entire application mechanism for `styx learn`'s output, see "Learning"
above — still works with ollama down; any store-open/read failure is
best-effort, narrated via `logStatus`, and yields `""` rather than blocking
the launch. It resolves the running binary via
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
routing brain and the conductor dispatch surface as fourteen tools for MCP
hosts like OpenClaw or Claude Code: `route`, `budget_status`, `record_usage`,
`channel_health`, `get_intel`, `refresh_intel`, `recall`, `dispatch`,
`dispatch_parallel`, `thread_status`, `memory_save`, `pipeline_run`,
`rate_dispatch`, and `collect`. Pure stdlib, no provider SDK; stdout carries
the protocol, status stays on stderr. `cmd/styx/mcp.go` adapts tool args onto
`internal/router`, `internal/budget`, `internal/intel`, and `internal/memory`.

**Cancellable root context (no daemons).** `cmdMCP` opens `ctx, cancel :=
context.WithCancel(context.Background())` and `defer cancel()` before doing
anything else, then builds `d := newConductorDeps(a, ctx)` — this is the same
`ctx` `srv.Serve(ctx, os.Stdin, os.Stdout)` runs on. `Serve` blocks until the
host closes stdin (EOF); the deferred `cancel()` then fires, which is what
tears down every background dispatch goroutine spawned off `d.reg` (see
"Background task registry" below) — there is no separate supervisor process,
matching the project's "no daemons" rule. Background work's context is
therefore always this one root, never a per-call context from the tool
invocation that spawned it.

**Startup task-state wiring.** Right after constructing `d`, `cmdMCP` resolves
`paths.TasksDir()`, `paths.EnsureDir`s it, and — narrating any failure via
`logStatus` and continuing without persistence rather than failing startup —
sets `d.reg.dir = dir` so `Spawn`/completion/`Claim` start mirroring task
state to disk. It then calls `adoptOrphanedTaskFiles(dir, 7*24*time.Hour)`
(see "Startup orphan adoption" below) and, when it finds unclaimed files from
a previous `styx mcp` lifetime, feeds them to `d.reg.adoptOrphans(orphans)`
and narrates the count via `logStatus` — a crashed or killed prior process's
in-flight/finished-but-uncollected background tasks resurface as `o1`, `o2`,
… entries in this session's `collect`/status line instead of vanishing
silently. Finally `cmdMCP` builds the tool set as `append(mcpTools(a),
conductorTools(d)...)`. Before `srv.Serve`, `cmdMCP` fires
`go preloadOllamaModels(a)`: a fire-and-forget, 20s-timeout best-effort call
to `/api/generate` with `keep_alive: "30m"` for `a.routing.Brain.Model` and
`a.routing.Brain.EmbedModel`, so the first real dispatch/recall doesn't pay a
cold model load while it overlaps with the host handshake. Failures are
narrated via `logStatus` (stderr) and never fatal — ollama may simply be down.

**Progress notifications (`_meta.progressToken`).** `Server` carries a
mutex-guarded `enc *json.Encoder` (`s.write(v any) error`, `s.mu`-serialized)
so every stdout write — protocol responses and out-of-band notifications
alike — goes through one lock; `Serve` sets `s.enc` before its read loop and
both former direct `enc.Encode` call sites now call `s.write`. `callTool`
reads `params._meta.progressToken` off the new `callParams.Meta` field; when
present (and not the literal `"null"`), it installs a progress-emitter
closure into the handler's `context.Context` under an unexported
`progressKey{}` before invoking `tool.Handler`. Handlers read it back via
`mcpserver.ProgressFn(ctx) (func(progress float64, message string), bool)` —
the bool is `false` when the client sent no token, so a handler can no-op
without a nil check. Calling the emitter writes a
`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken","progress","message"}}`
line via `s.write`, interleaved with (but never colliding with) the eventual
`tools/call` response — stdout carries JSON-RPC exclusively, so this is the
only channel for mid-call narration; `logStatus` stays on stderr.

**Concurrent `tools/call`, cancellation, and the Serial lane.**
`tools/call` requests run concurrently, one goroutine per call, tracked by
request id; `notifications/cancelled` cancels the matching call's context
(this is how a host-side Esc reaches a long-running handler). `initialize`
and `tools/list` answer inline. On EOF the server cancels and drains every
in-flight call before returning. Tools whose handlers are not audited for
concurrent use set `Tool.Serial` and share a single lane (`pipeline_run`,
`refresh_intel`).

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
cross-cutting contract for every project-scoped v2 tool. This differs
deliberately from the conductor tools' `managerFor` (see "Conductor MCP
tools" below): there, an empty `project` resolves to the server's launch-
directory project instead of erroring, because the conductor case matches
`pipeline_run`'s cwd-anchored model rather than the routing-brain tools'
always-explicit one.

## Conductor MCP tools (cmd/styx/mcp_conductor.go)

`dispatch` and `thread_status` give a frontier-brain MCP consumer (Claude
Code, per the conductor spec) a dispatch surface onto the same
`internal/agent` thread machinery the REPL uses, without going through the
REPL loop.

- `conductorDeps` (`a *app`, `gate *shipgate.Gate`, `emb memory.Embedder`,
  `reg *taskRegistry` — the background dispatch registry, nil-safe on every
  read path — `board *activity.Board` — one shared liveness board fed by every
  managed `Manager` (`Board: d.board`), so sync and background dispatches alike
  record activity — `mirror func() error` (Task 9) — a debounced disk-mirror
  writer, nil if the state dir couldn't be prepared — a mutex-guarded
  `managers map[string]*managed` cache keyed by project ID, and a
  mutex-guarded `gmem *memory.Store` — lazy handle on the shared `global.db`,
  opened on first use via `globalMem()` and cached for the life of the
  process) is built once per `styx mcp` invocation via
  `newConductorDeps(a, rootCtx)`. The `rootCtx` parameter is `cmdMCP`'s
  cancellable root context (see "MCP server" above); it flows straight into
  `newTaskRegistry(rootCtx, a.routing.Conductor.MaxBackgroundTasks, board)` so
  every background task this server ever spawns is rooted on the server's own
  lifetime, not any single tool call's, and the registry can read each running
  task's last board action for its piggyback line. `newConductorDeps` also
  starts an `activity.Watcher` goroutine off `rootCtx` gated on
  `routing.Watch.OllamaEnabled` (dies with the server; best-effort), and wires
  `d.mirror` via `activity.MirrorThrottle(board, <StateDir>/watch/<projectID>.json,
  2*time.Second)`, keyed by `resolveGlobalTarget("")`'s project ID — the same
  cwd-based resolution `managerFor("")` and a `styx watch` process invoked
  alongside the server both use, so the mirror path always agrees with what
  `styx watch` reads (see the Activity section's disk-mirror write-up for the
  full path-consistency argument). `d.mirrorNow()` narrates (never swallows)
  a write failure via `logStatus` and is called from every dispatch's `runFn`
  closure — background and awaited spawns alike now route through the same
  `taskRegistry`, so both bracket `m.mgr.Dispatch` with `d.mirrorNow()` before
  and after; the mechanical pulse (`conductorDeps.pulse`, see the Activity
  section) covers mid-run freshness, so these brackets are just the
  start/end frames.
  `conductorTools(d)` returns seven tools: `dispatch`, `dispatch_parallel`,
  `thread_status`, `memory_save`, `pipeline_run`, `rate_dispatch`, and
  `collect`. (Deviation
  from the Task 8 brief, which put the board on the per-project `managed`
  struct: the board must exist at `newConductorDeps` time to thread into the
  registry, which is created before any `managed` — so it lives on
  `conductorDeps` and is shared into every Manager, mirroring how the REPL
  session owns one board across bound projects.)
- `conductorDeps.managerFor(alias)` lazily binds a project exactly the way
  `replSession.bind` does (`cmd/styx/repl.go`): opens `<memDir>/<projectID>.db`
  via `memory.Open`, loads `agent.LoadThreads(projectID)`, wires the
  claude/codex/agy adapters (`agent.NewClaudeAdapter/NewCodexAdapter/
  NewAgyAdapter`), the shared budget tracker, an ollama-backed `Summarize`
  closure for distill-and-restart, and the `[budget.claude].timeout_minutes`
  subprocess timeout (default 10m). Resolution: an **empty alias resolves to
  the server's cwd project** via `resolveGlobalTarget("")` — the launcher
  starts `styx mcp` in the launch directory, so cwd IS the caller's project
  for the conductor, matching `pipeline_run`'s own cwd resolution. A
  **named alias resolves strictly**, no fallback, via
  `target.Resolve(target.Spec{Alias: alias})`. Either way, a resolution
  failure is wrapped as `resolve project %q: %w (registered projects: %s)`
  via the `registeredProjectNames()` helper (`config.LoadProjects()` joined
  by name, or `"none"`) — unlike a CLI invocation, an MCP consumer can't
  "pass --dir" or "cd into a repo", so the error lists the registry instead
  of suggesting shell remedies.
- `dispatch(project?, thread?, cli, message, model?, risk, extra_roots?,
  confirm_token?, background?)` — `cli` is one of `claude|codex|agy|ollama`;
  `risk` is `read|edit|ship`. Validation (unknown cli, empty message, invalid
  risk) runs first and returns a plain error. For `risk: ship`, the
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
  like every other channel. Its result gains `model` (the resolved model
  string) and `duration_s` (wall-clock seconds since a `start := time.Now()`
  taken right after arg validation, rounded via
  `math.Round(time.Since(start).Seconds()*10)/10` to one decimal) alongside
  `{cli, text}`.

  Otherwise the call routes through `managerFor` and builds one shared
  `spec := agent.DispatchSpec{Thread, CLI, Model, Message, ExtraRoots,
  ReadOnly}` consumed by both forks below.

- **Awaited by default — spawn + observe (spec: awaited-dispatch, superpowers
  2026-07-08).** This is the governing behavior: `background` absent/`false`
  no longer calls `agent.Manager.Dispatch` directly. Instead the handler
  spawns an ordinary `taskRegistry` task — `runFn(bctx, id)` sets
  `ameta.TaskID = id`, brackets `m.mgr.Dispatch(bctx, spec, nil)` with
  `d.mirrorNow()`, and finishes through the shared `d.finishDispatch(bctx,
  ameta, res, derr)`, exactly like the background fork below — via
  `d.reg.Spawn(taskSpec{...}, runFn)`, then observes it with
  `(*conductorDeps).awaitTasks(ctx, []string{id}, notify)` (see "Awaiter"
  below) until it's terminal. Because the spawn goes through the same
  registry as `background: true`, the ordering rules apply uniformly: a
  thread or project-write collision **queues** the new dispatch behind the
  blocker instead of erroring — the old synchronous `Busy` guard is gone;
  queueing replaces it entirely. While the await runs,
  `mcpserver.ProgressFn(ctx)` (when the client sent a progress token)
  streams `awaitTasks`'s board-derived progress line (done count, per-task
  heartbeat, notices for unrelated completions) in place of the removed
  `onEvent` "streaming (N events)" chatter. On completion the claimed task's
  `collectOne` shape returns inline — `{task_id, status: "done", thread,
  cli, text, tokens_in, tokens_out, model, duration_s}`, a superset of the
  old sync result that now always carries `task_id` — or, on a task error,
  `finishDispatch`'s wrapped `dispatch <cli>: %w` string surfaces as the
  tool error. **Cancellation detaches.** If the call's `ctx` is cancelled
  mid-await (host Esc → `notifications/cancelled`, per-call cancellation
  wired in Task 1; or the server's EOF drain), `awaitTasks` returns
  `{Detached: true}` without claiming anything, and the handler returns
  `{detached: true, task_id, note}` — the task keeps running unclaimed on
  the registry's root context and stays fetchable via `collect`; the work
  itself is never lost, only the in-call observation of it. A nil `d.reg`
  (registry unavailable) is rejected loudly rather than silently falling
  back to a direct `Dispatch` call.
  **Every dispatch completion — the ollama one-shot branch, the awaited
  fork, and the background fork alike — appends one `budget.Outcome` row**
  via `(*Tracker).RecordOutcome` (see "Budget tracker" `outcomes` table):
  CLI, resolved model, risk, wall-clock duration, real token counts, and
  `Signals` (routing signals extracted from the raw message via
  `dispatchSignals` → `signals.Extract("dispatch", []string{message},
  config.Project{})`, comma-joined — recorded for learning, not routing,
  since the conductor picks the cli explicitly). `ErrorKind` is `""` on
  success, else the channel's `channel.ClassifiedError.Kind` when the
  dispatch error wraps one, else `"other"` (`outcomeErrKind`). As with the
  budget-event record above, an outcome-record failure is narrated via
  `logStatus` and never fails an otherwise-completed dispatch — this is
  the plan's one sanctioned soften of "never swallow errors". Awaited
  outcome rows now carry a non-empty `TaskID` with `Background: false`
  (distinguishing them from `background: true` rows, which carry both
  `TaskID` and `Background: true`) — before this task only background rows
  ever set `TaskID`. Post-dispatch bookkeeping (append the outcome row,
  wrap a dispatch error as `dispatch %s: %w`, or shape the success result
  map) is the shared `(*conductorDeps).finishDispatch(ctx, dispatchMeta, res
  agent.TurnResult, dispatchErr error) (map[string]any, error)` method —
  built from a `dispatchMeta{ProjectID, Thread, CLI, Model, Risk, Signals,
  TaskID, Background, Start}` struct assembled before either fork's
  `Manager.Dispatch` runs. `finishDispatch` is the one function every task
  completion (awaited or background) shares — reused verbatim, not
  reimplemented.
- **`background: bool`** — `dispatch` takes an optional `background`
  argument. `true` spawns the same kind of `taskRegistry` task as the
  awaited path above (same `spec`, same `d.reg.Spawn` call) but skips the
  observer entirely, returning `{task_id, thread, cli, status}` immediately
  instead of waiting for the turn to finish; `collect` (Task 8) later
  fetches the result. Spawn-time work runs in this fixed order, deliberately
  front-loaded so failures are synchronous and cheap — nothing here touches
  the (potentially expensive) project/thread resolution that follows:
  1. The same arg validation as the awaited path (unknown cli, empty
     message, invalid risk).
  2. **Ship/ollama rejection**: `risk: ship` background dispatches are
     rejected — the confirm-token handshake is interactive and cannot
     survive a tool call returning immediately — as are `cli: ollama`
     background dispatches, since one-shots are already local and fast
     enough to run synchronously. Both are plain errors naming `"background"`
     / `"ollama"` respectively.
  3. A nil `d.reg` (registry unavailable) is rejected loudly rather than
     silently falling back to a direct dispatch.
  4. **`(*conductorDeps).spawnBudgetCheck(ctx, cli) error`** — refuses the
     spawn when `tracker.ShouldCircuitBreak(ctx, cli, budget.BreakerThreshold,
     budget.BreakerWindow)` reports the channel's circuit open, or when
     `tracker.UsedPct(ctx, cli)` is at or over that channel's `cap_pct`
     (`capPctFor(routing, cli)`, reading `Budget.{Claude,Codex,Agy}.CapPct`;
     ollama never reaches here). A background task that would immediately
     fail on budget/circuit grounds is refused at spawn time instead of
     burning a registry slot only to fail invisibly later.
  5. Only past all four checks does project/thread resolution happen, after
     which (right after `meta := dispatchMeta{...}` and `spec := agent.
     DispatchSpec{...}` are assembled) the background fork's `runFn(bctx,
     id)` closure — `bctx` is the registry's root context, not this call's
     `ctx` — sets `bmeta.Background = true; bmeta.TaskID = id`, brackets
     `m.mgr.Dispatch(bctx, spec, nil)` (no progress callback: this tool
     call's JSON-RPC exchange is long gone by the time it completes) with
     `d.mirrorNow()`, and finishes through the same `d.finishDispatch(bctx,
     bmeta, res, derr)` as the awaited fork. `d.reg.Spawn(taskSpec{...},
     runFn)` registers it and returns immediately with `{task_id, thread,
     cli, status}` (`status` is `"running"` or `"queued"` per the registry's
     cap/ordering rules — see "Background task registry" below).
- `thread_status(project?)` — resolves the project via the same
  `managerFor` and returns `{threads: []string, tasks: []string}`. `threads`
  comes from `agent.Manager.StatusLines()` (name, CLI, turn count,
  context-window percent per thread); `StatusLines()` guarantees a non-nil
  `[]string{}` when a project has no threads, so the JSON shape is always
  `{"threads": []}`, never `{"threads": null}` — MCP consumers can rely on
  the key always being an array. `tasks` is built fresh each call from
  `d.reg.Snapshot()` (not project-scoped — it lists every background task
  this server knows about): one `taskLine(tk)` entry per task that is
  `taskQueued`/`taskRunning` OR terminal-and-unclaimed, so a caller sees
  live and not-yet-collected work without a separate call. Always
  initialized to `[]string{}`, never nil, so `tasks` is likewise always a
  JSON array (`[]`, never `null`) even when nothing is outstanding.
- `memory_save(project?, kind, text, scope?)` — validates `kind` against
  `memory.KindFact/KindDecision/KindTodo` plus the three user/session kinds
  `KindRoutingPreference` ("routing-preference"), `KindUserPreference`
  ("user-preference"), and `KindRetrospective` ("retrospective") (any other
  value errors loudly) and requires non-empty `text`, then embeds via
  `d.emb.Embed`. Routing forks on kind: the three user/session kinds describe
  the user/session, not one repo, so they write through `d.globalMem()` — a
  lazy, mutex-guarded, cached handle on a shared `global.db` under
  `paths.MemoryDir()` (opened once per
  `conductorDeps`, no project resolution needed) — with `Project: ""` and
  `scope` defaulting to `"global"`; the launch-time guidance injection
  (Task 7) reads this same store. All other kinds keep writing through
  `managerFor(project).mem.Add` with `scope` defaulting to `"project"`.
  Every write uses `Source: "conductor"`, `Confidence: 0.9`. Returns
  `{saved, id}`.
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
  `memory_save`) now match this: their shared `managerFor(alias)` resolves
  an empty alias to the same cwd project via `resolveGlobalTarget("")`,
  while a named alias still resolves strictly (no fallback) via
  `target.Resolve`; `managerForProject` binds an already-resolved project
  for the research indexing step. On success returns `{pipeline, done:
  true, note}` pointing at `styx/research/` and `styx/plans/` for
  artifacts.
- `rate_dispatch(thread_or_task, ok, note?)` — stamps a rating onto the
  **most recent matching outcome row** (by thread name or background task
  id) via `(*budget.Tracker).RateOutcome`, feeding the future `styx learn`
  loop. Rejects an empty `thread_or_task` before calling the tracker (a
  plain error, not a tracker round-trip); `RateOutcome` itself errors loudly
  on an unknown ref too, so there's no silent no-op. `ok: true` writes
  `Rating: "good"`, `false` writes `"bad"`; `note` is freeform and optional.
  Guidance baked into the tool description: rate only notable outcomes, not
  every dispatch. Returns `{rated: true, outcome_id, target}`.
- `collect(task_id?)` — the read side of async dispatch (Task 8), backed by
  the shared `collectOne(reg *taskRegistry, tk bgTask) map[string]any`
  helper. Two call shapes:
  - **With `task_id`**: `d.reg.Get(task_id)` first — an unknown id is a loud
    `fmt.Errorf`, never a silent empty result. Live tasks (`taskQueued`/
    `taskRunning`) return `{task_id, status, elapsed_s}` (`elapsed_s` since
    `Created`, rounded to 0.1s) plus `queued_behind` when the task names a
    specific blocker; nothing is claimed. Terminal tasks are **claimed as a
    side effect of being collected**: `taskDone` returns a fresh
    `map[string]any` seeded with `task_id`/`status: "done"` and every key
    from `tk.Result` copied in (`for k, v := range tk.Result { out[k] = v
    }` — a read of the registry-shared `Result` map, never a mutation of
    it) before calling `reg.Claim(tk.ID)`; `taskError`/`taskOrphaned`
    return `{task_id, status, error: tk.Err, thread, cli}` (the orphan
    payload's `error` is the "lost when styx mcp exited" message set at
    adoption time) and likewise claim. Once claimed, a task stops appearing
    in `thread_status.tasks` and in a no-`task_id` `collect` call — but
    remains fetchable by id from `Snapshot`/`Get` for the process lifetime.
  - **Without `task_id`**: iterates `d.reg.Snapshot()` once. Live tasks are
    summarized via `taskLine(tk)` into `pending` (no claim — a live task
    can't be claimed); every unclaimed terminal task is passed through
    `collectOne` into `results` (claiming each). Returns `{results:
    []map[string]any, pending: []string}`, both initialized to empty
    slices (never nil) so the JSON shape is always `{"results": [],
    "pending": []}` at minimum — repeat calls after everything is claimed
    return empty `results` rather than repeating prior payloads.
  - `taskLine(t bgTask)` (`cmd/styx/mcp_tasks.go`) is the one-line renderer
    shared by `collect`'s `pending` list and `thread_status.tasks`:
    `taskRunning` → `"<id> running (<cli>, thread <thread>, <elapsed>)"`;
    `taskQueued` → `"<id> queued <behind X|at cap> (<cli>, thread
    <thread>, <elapsed>)"`; terminal states → `"<id> <state>[ unclaimed]
    (<cli>, thread <thread>)"`.
- `dispatch_parallel` — awaited N-agent fan-out: an array of {cli, message,
  risk, thread?, project?, model?, extra_roots?} specs, each spawned as a
  registry task (read risk runs concurrently; ordering rules queue
  collisions), observed by the same awaiter as `dispatch`. Per-task failures
  (validation, budget, agent error) are per-entry results — the call errors
  only on malformed arguments. Cancellation detaches all spawned tasks.
  read|edit only; ship and ollama stay single-dispatch.
- **Piggyback (Task 9)** — `withBackgroundStatus(tools []mcpserver.Tool, reg
  *taskRegistry) []mcpserver.Tool` (`cmd/styx/mcp_tasks.go`) is the single
  decoration point that keeps background work from being forgotten: it wraps
  every conductor tool's `Handler`, runs the inner handler unchanged, and on
  success — only when `reg.StatusLine()` is non-empty (live or unclaimed
  tasks exist) AND the result is a `map[string]any` — sets `m["bg"]` to that
  status line before returning. `StatusLine`'s running-task entries are
  enriched (Task 8) with the task's last board action: when `reg.board` is set
  (nil-safe), it matches `board.Snapshot()` state by the project-qualified key
  `agent.BoardLabel(Spec.ProjectID, Spec.Thread)` (NOT the bare thread — the
  board is shared across projects, so matching on thread alone would
  cross-attribute two projects' like-named tasks) and appends `— <last>` so the
  piggyback line carries what each agent is *doing*,
  not just its elapsed clock. Errors pass straight through untouched
  (never decorated); non-map results (e.g. the raw `shipgate.Result` token
  relay from a denied `risk: ship`/`pipeline_run` gate) also pass through
  untouched, since there's no map to add a key to. An idle registry
  (`StatusLine() == ""`) adds no `bg` field at all — the common case, where
  nothing background is outstanding, is byte-for-byte what the tool would
  have returned anyway. `cmdMCP` wires it as `append(mcpTools(a),
  withBackgroundStatus(conductorTools(d), d.reg)...)` — only the conductor
  tools get the decoration; the base `mcpTools(a)` (route, budget_status,
  etc.) are not wrapped, since they have no registry context and aren't part
  of the async-dispatch surface. The JSON-RPC transport itself is untouched:
  this only augments the map-shaped result payload a handler already
  returns, it never emits a notification or writes to stdout directly.

### Background task registry (cmd/styx/mcp_tasks.go)

`taskRegistry` is the in-memory, mutex-guarded heart of async dispatch (B1):
it owns every background task of one `styx mcp` process and enforces the cap
and ordering rules that keep background work from racing a project's own
stateful sessions. Task 5 lands the registry itself (cap, ordering,
collect/claim); Task 6 wires the state-file mirror (`writeTaskFile`) and
startup orphan adoption; Task 7 wires the `dispatch(background: true)` tool
surface on top (see "Conductor MCP tools" above) and `cmdMCP`'s startup
sequencing — constructing the registry on the server's cancellable root
context, registering `r.dir`, and calling
`adoptOrphanedTaskFiles`/`adoptOrphans` before serving (see "MCP server"
above); `collect` (Task 8) is the read side that surfaces `Get`/`Claim`
results to the caller.

- **States** — `taskQueued` ("queued"), `taskRunning` ("running"),
  `taskDone` ("done"), `taskError` ("error"), `taskOrphaned` ("orphaned").
  Queued/running are "live"; done/error/orphaned are terminal and stay
  visible (in `Snapshot` and `StatusLine`) until claimed.
- **Monotonic ids** — `newTaskRegistry(rootCtx, limit)` builds an empty
  registry; `Spawn` assigns ids `t1`, `t2`, … in a mutex-guarded `r.seq`
  counter, monotonic within one server lifetime. Orphans adopted from a
  prior (crashed or exited) `styx mcp` lifetime get `o1`, `o2`, … ids instead
  (assigned by adoption order, not `r.seq`) — see the mirror/orphan-adoption
  paragraph below. `Spawn` returns `(id, state)` immediately — `state` is
  `taskRunning` if the task started inline or `taskQueued` if
  capacity/ordering deferred it.
- **Global concurrency cap** — `limit` (defaulting to 4 if `<= 0`), sourced
  from `[conductor] max_background_tasks` (`internal/config/routing.go`
  `Conductor.MaxBackgroundTasks`, default 4, seeded by
  `EnsureConductorTaskCap`). `startEligibleLocked` counts running tasks and
  promotes queued tasks in creation order while `running < limit` and no
  ordering rule blocks them.
- **Ordering rules** — both enforced by `conflictLocked`, checked in
  creation order against every currently-running task:
  1. **Per-thread serialization**: a task on the same `(ProjectID, Thread)`
     as a running task never runs concurrently with it — session resume is
     stateful, so two turns on one thread would corrupt each other's state.
  2. **Per-project write queue**: an edit-risk task (`Spec.Risk != "read"`)
     waits for any running edit-risk task on the same `ProjectID`; read-risk
     tasks run freely in parallel with each other and with edit-risk tasks
     on other threads. A queued task's `QueuedBehind` field names the
     blocking task id, or `""` when it is waiting on capacity alone (all
     slots full, no specific blocker).
- **Root-context lifetime (no daemons)** — every task's `run` goroutine
  (`go r.execute(t)`, spawned from `startEligibleLocked`) is invoked with
  `r.rootCtx`, the context passed into `newTaskRegistry` at server startup —
  never a per-call context from the tool invocation that spawned it. This
  means background tasks survive the `dispatch` tool call returning (the
  whole point of async dispatch) but are canceled the instant the `styx mcp`
  process's root context ends; there is no separate daemon or supervisor
  process, matching the project's "no daemons" rule.
- **Claim semantics** — a finished task (done/error/orphaned) stays
  unclaimed until `Claim(id)` sets `Claimed = true`; `StatusLine` only lists
  unclaimed terminal tasks (as `"<id> <state> unclaimed — call collect"`),
  so once the caller has read a result via `collect` (Task 8) it stops
  resurfacing on every status line. `run` errors are never swallowed: a
  failed task's `Err` field carries `err.Error()`, surfaced through `Get`/
  `Snapshot`/`StatusLine`, not dropped.
- **No sync bypass — every dispatch goes through the registry.** The
  `Busy(projectID, thread, risk)` guard that used to let a *synchronous*
  `dispatch` call check for a colliding live background task and error
  loudly (`thread %q is busy with background task %s`) is gone (removed by
  the awaited-dispatch task, superpowers 2026-07-08): since `dispatch`
  awaits by default via `Spawn` + `awaitTasks` (see "Conductor MCP tools"
  above), a thread or project-write collision now hits the same
  `conflictLocked` ordering rules a `background: true` collision always
  did, and **queues** rather than erroring. There is no longer a
  synchronous caller that bypasses the registry to check.
- **Nil-safety** — `Get`, `Claim`, `Snapshot`, and `StatusLine` are all safe
  to call on a nil `*taskRegistry` (zero-value+false / no-op / nil slice /
  `""` respectively), so callers don't need a separate "is async enabled"
  check.
- **State-file mirror (crash honesty, never resumption)** —
  `persistLocked` mirrors task state to `r.dir` via `writeTaskFile` on every
  `Spawn`/completion/`Claim` when `r.dir != ""` (`""` in most unit tests, so
  persistence is skipped there). `writeTaskFile` writes
  `<dir>/<run-id>.json` atomically (tmp + rename under `os.Rename`) and is
  best-effort: a marshal/write/rename failure is narrated via `logStatus`
  and never fails the task — the in-memory task is always authoritative
  during a live process. Results themselves are never persisted to disk,
  only status metadata; a finished-but-uncollected task surviving a crash is
  a reported loss, not something to resume.
- **Startup orphan adoption** — `adoptOrphanedTaskFiles(dir, maxClaimedAge)`
  scans `dir` once at `styx mcp` startup (wired in Task 7's `cmdMCP`, using
  `paths.TasksDir()`). Every UNCLAIMED file, regardless of the state it
  recorded (`queued`/`running`/`done`/`error`/already-`orphaned`), is a loss:
  the process that owned it is gone and its result — if any — lived only in
  that process's memory. Each such file is flipped to `taskOrphaned` on disk
  (best-effort tmp+rename, narrated via `logStatus` on failure — the orphan
  is still returned in-memory even if the on-disk flip can't be persisted,
  so the next scan simply retries the flip) and returned to the caller.
  `(*taskRegistry).adoptOrphans(files)` inserts the returned orphans into a
  fresh registry as `o1`, `o2`, … entries so `collect` (Task 8) and the
  status-line piggyback report them; an orphan's `Err` explains the loss
  (`"lost when styx mcp exited (state was ...); no result — re-dispatch if
  still needed"`) so the caller knows to re-run the work, never that it will
  silently resume. An unclaimed orphan file keeps resurfacing on every scan
  until something calls `Claim` on it; claiming re-persists the file so it
  stops. Claimed files whose `Finished` timestamp is older than
  `maxClaimedAge` (7 days, wired in Task 7) are pruned (`os.Remove`) during
  the same scan — claimed files are never orphans, only prune candidates.

**Awaiter (`cmd/styx/mcp_await.go`).** Awaited dispatches are observed
background tasks: `awaitTasks` polls the registry every second until every
awaited id is terminal, streaming one compact progress line per change
(per-task heartbeats from the activity board in Render vocabulary — ▸ / ⚠ /
✓ — plus ✗ for an awaited task that finished in `taskError` or
`taskOrphaned`, one-time "tN done — collect" notices for unrelated
completions, the ollama watcher note when present) through the call's MCP
progress emitter. Terminal awaited tasks are claimed — their results return
inline. Context cancellation
(host Esc → notifications/cancelled, or server EOF drain) detaches: nothing
is claimed and the tasks keep running as collectible background work.

## Activity (internal/activity)

- **Activity** (`internal/activity/`): live dispatch-observability board —
  per-agent heartbeat, stall detection, ollama watcher, and the cross-process
  disk mirror behind `styx watch`.

`Board` is the in-process, thread-safe (single `sync.Mutex`) liveness
substrate every later surface (TUI renderer, ollama watcher, disk mirror)
reads from. It holds only strings and timestamps — never `agent.Event` — so
`internal/activity` imports nothing from `internal/agent`; the dependency
runs one way, agent → activity. `NewBoard()` starts empty on the wall clock
(`SetClock` overrides it for tests). `Record(label, summary)` stamps a
one-line activity string for an agent label, marking it live and appending to
a per-agent ring buffer capped at `recentCap` (20) entries so the ollama
watcher's prompt stays small. `Done(label, elapsed)` marks an agent finished
with its total elapsed time. `Snapshot()` returns an `[]AgentState` (`Label`,
`Last`, `LastAt`, `Done`, `Elapsed`, `Recent`) in first-seen order; `Recent
(label)` returns just that agent's ring buffer. Labels are opaque keys: the
`agent.Manager` writes project-namespaced ones (`agent.BoardLabel` →
`"<projectID>/<thread>"`) so the single board shared across projects never
collides; `activity.Render` strips the `"<projectID>/"` prefix (via
`displayLabel`) so `/watch` and the live renderer show the bare thread name. `SetWatcherNote`/
`WatcherNote` hold the ollama watcher's latest health read as a single
string. **Wiring (Task 8):** both the REPL session and the conductor own one
`Board`, injected into every `agent.Manager` (`Manager.Board`) so every turn
records liveness; the REPL exposes it via `/watch` and an inline parallel-
dispatch heartbeat, the conductor via the piggyback bg line and its own
watcher goroutine (see the `repl.go` and "Conductor MCP tools" sections).

**Ollama watcher** (`Watcher`): a best-effort background goroutine that
periodically feeds cross-agent activity to local ollama `/api/chat` and
writes health observations back to the board via `SetWatcherNote`.
`Watcher{BaseURL, Model, Board, Interval}` configures the endpoint, chat
model, target board, and poll cadence (0 defaults to 15s). `Run(ctx)` fires
a goroutine that polls until context cancellation; poll errors are
deliberately swallowed — a down ollama must not spam or crash the session.
`pollOnce(ctx)` runs one watch cycle: it snapshots live (non-`Done`) agents,
builds a structured prompt listing each agent's recent activity lines,
sends it to ollama with a system prompt tuned for agent-health assessment,
and stores the parsed response on success. On failure (unreachable ollama,
parse error, etc.) it returns the error and leaves the existing note
untouched — callers can log the error for debugging but the session
continues unaffected. This graceful degradation is tested via `httptest`
mocks for ollama success and failure paths.

**Live renderer** (`LiveRenderer`): a TTY-aware refresh loop that repaints
the board in place on a ticker. `NewLiveRenderer(w io.Writer, b *Board, stall
time.Duration)` builds a renderer targeting writer `w`, reading from board
`b` with stall timeout `stall`. On a TTY it uses ANSI cursor movement to
clear previous frames in place (`\033[<n>A` to move up n lines, `\r\033[K` to
clear each line); on a non-TTY (e.g., a buffer) it appends frames (quiet
cadence for logging). `Start()` begins a repainting goroutine that calls
`paint()` every second until `Stop()`. `paint()` is the single testable frame
method: it snapshots the board, renders it via `Render`, acquires a mutex to
serialize writes, and updates a counter tracking lines painted for next-frame
cursor repositioning. `Stop()` closes the internal stop channel, waits for
the goroutine to exit, and paints a final frame; a `sync.Once` (`stopOnce`)
guards the close so a second `Stop()` call — or a `Stop()` before `Start()`,
where `l.stop` is still nil — is a safe no-op instead of a
close-of-closed-channel panic (`TestLiveRendererStopTwiceDoesNotPanic`).
Tests set `lr.now` to a fixed clock for deterministic rendering and call
`paint()` directly against a buffer (non-TTY, so no ANSI codes), asserting
the rendered output contains expected content.

**Disk mirror + `styx watch` (Task 9, `mirror.go`, `cmd/styx/watch.go`):** a
throttled on-disk copy of the board so a *second, separate* `styx` process
can render the same live activity without sharing memory with the session
that's dispatching. `WriteMirror(path, states, note)` marshals a
`[]AgentState` + note to JSON and writes it atomically (tmp file + rename) so
a concurrent reader never observes a partial write. `ReadMirror(path)`
reverses this; when `path` doesn't exist it wraps `os.ReadFile`'s error with
`%w`, so callers detect absence with `errors.Is(err, fs.ErrNotExist)` (never a
bespoke sentinel). `MirrorThrottle(b *Board, path string, min time.Duration)`
returns a debounced writer closure: the first call always writes (leading
edge), later calls within `min` of the last write are no-ops, and every write
failure is returned to the caller — a mirror write is a best-effort side
channel, so it is narrated (`logStatus`), never fails the dispatch it rides
along with.

Mirror files live at `~/.config/styx/state/watch/<projectID>.json`
(`paths.StateDir()/watch/`, created with `paths.EnsureDir`), throttled to
roughly one write per 2s. **Path-consistency is the whole design constraint:**
the writer (a REPL session or the `styx mcp` conductor) and the reader (a
`styx watch` process in another terminal) must independently compute the same
path, with no message passed between them. `newREPLSession(a, repos...)` has
*two* seed-resolution paths and both must be matched:

- With an explicit repo arg (`styx repl otherRepo`), `seed` resolves via
  `target.Resolve(target.Spec{Alias: repos[0]})` — by registered name/prefix/
  path, **not** the process's cwd — and the mirror is keyed on `seed.ID`.
- With no repo arg (bare `styx repl`), `seed` resolves via
  `resolveGlobalTarget("")` (the process's cwd), matching `newConductorDeps`'s
  identical `resolveGlobalTarget("")` call (the server's launch-directory
  project, per `managerFor`'s existing cwd convention).

Either way `seed` (not the mutable `s.focus`, which `/focus` can move to a
different bound repo mid-session) is resolved once, before the session
struct exists, so the mirror path never drifts under a running session.

`cmdWatch` (`cmd/styx/watch.go`) mirrors both branches via a small helper,
`watchProjectID(args []string)`: a non-empty first positional arg resolves
through the identical `target.Resolve(target.Spec{Alias: args[0]})` call the
REPL's explicit-repo path uses, so `styx watch otherRepo` agrees with a
`styx repl otherRepo` session launched from *any* directory; with no args it
falls back to the same `resolveGlobalTarget("")` cwd resolution as the bare
REPL and the conductor. `watchMirrorPath(args)` joins that ID onto
`paths.StateDir()/watch/<id>.json`. (An earlier version of `cmdWatch` always
called `resolveGlobalTarget("")` regardless of arguments — silently
mismatching a `styx watch otherRepo` invocation against an explicit-repo REPL
session; `watchProjectID` closes that gap and is covered by
`TestWatchProjectIDMatchesREPLAliasResolution` /
`TestWatchProjectIDNoArgsMatchesCwdResolution` in `cmd/styx/watch_test.go`.)

Writers call the throttle from the dispatch event path: the REPL's
`printEvent` (every streamed event) and, for the quiet parallel fan-out
(where `onEvent` is `nil` and `printEvent` never fires), a dedicated
one-second ticker (`startMirrorTicker`) bracketing the `LiveRenderer` span.
The conductor calls it from every `dispatch` task's `runFn` closure —
awaited and background spawns alike now route through the registry, and
each brackets its `Dispatch` call with explicit `d.mirrorNow()` calls before
and after. A mechanical pulse goroutine
in the conductor (`conductorDeps.pulse`, 1s tick) refreshes the throttled
mirror whenever any agent or task is live and writes one unthrottled final
frame on the live→idle transition — `styx watch` is live mid-run for
background and awaited dispatches alike, with no ollama dependency. (This
closes the Task-9 deferred limitation from the 2026-07-07 observability
plan.) `cmdWatch` is a read-only loop:
resolve the project via `watchMirrorPath(args)`, `ReadMirror` it, clear the
screen, render via `activity.Render(states, note, stall, time.Now())`, sleep
~1s, repeat; a missing mirror (`errors.Is(err, fs.ErrNotExist)`) prints `(no
live activity …)` and returns nil rather than erroring. `stall` comes from
`watchStallThreshold()`, a pure `config.LoadRouting()` TOML parse (no
`loadApp()`/sqlite wiring) that returns `routing.Watch.StallThreshold()`, so
a cross-process `styx watch` flags stalls at the same configured threshold
as the in-process REPL `/watch` and inline `LiveRenderer` (both of which read
`routing.Watch.StallThreshold()` too) instead of always using
`activity.DefaultStall` (90s); a missing or unparsable routing.toml falls
back to `activity.DefaultStall` silently, since `watch` is a read-only viewer
that must keep working without a config. Because `cmdWatch` never calls
`loadApp()` (no routing/budget/sqlite wiring beyond that one pure TOML
parse), `watch` is registered in `cmd/styx/dispatch.go`'s pre-`loadApp`
switch, alongside `runs`.

**Mirror cleanup on session end:** both writers remove their mirror file when
they stop, so a later `styx watch` shows the `(no live activity …)` nudge
instead of replaying the last session's final frame. The REPL's `cleanup()`
(`cmd/styx/repl.go`, run via `defer` in both the interactive and one-shot
dispatch paths) does `os.Remove(mirrorPath)` after closing bound bundles,
using the exact path captured when `s.mirror` was built; `fs.ErrNotExist` is
ignored (nothing to clean up), any other error is narrated via `logStatus`
and never fails cleanup. The conductor mirrors this: `conductorDeps` carries
`mirrorPath` alongside `mirror`, and `cmdMCP` (`cmd/styx/mcp.go`) calls
`d.removeMirror()` right after `srv.Serve(...)` returns, before propagating
its error — so the file is gone whether the server exits cleanly or the host
closes stdin.

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
~/.config/styx/state/tasks/<run-id>.json    background-task state mirrors (crash honesty)
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

`e2e/` (build tag `e2e`, run via `make e2e`) is the regression net: it builds
`./bin/styx`, then drives a real `styx mcp` subprocess over stdio JSON-RPC
exactly as a conductor would — `initialize`, `tools/list`, `tools/call` for
`route`/`budget_status`/`dispatch`/`thread_status` — with `testdata/fakeagent`
installed as both `claude` and `codex` on an isolated `PATH`, and `HOME`/
`XDG_CONFIG_HOME` pointed at a fresh `t.TempDir()` so no real config or
subscription quota is touched. The launch project is a throwaway git repo
created per test run. `startServer(t, extraEnv...)` is variadic so tests can
layer extra env (e.g. `FAKEAGENT_SLEEP=2`) onto the subprocess without
touching existing callers. `TestBackgroundDispatchRoundtrip` exercises the
full background-dispatch stack over the same real JSON-RPC subprocess: a
`dispatch` call with `background:true` returns an immediate `{task_id,
status:"running"}` handle; a subsequent `thread_status` call's `bg` piggyback
line names the live task; polling `collect({task_id})` observes the task
transition to `status:"done"` with the fakeagent's text once its
`FAKEAGENT_SLEEP` elapses; and a final `collect({})` shows nothing pending
and no `bg` line once the result is claimed. This is hermetic by default: no
Docker (a plain subprocess + fake-CLIs-on-PATH gives the same isolation for a
single-binary CLI without the image/build overhead), no network beyond a
possibly-absent local ollama, and no real AI-CLI calls. `TestLearnScorecardSeesDispatchOutcomes` closes the
learning loop across two separate processes sharing one isolated config: a
`dispatch` call over the `styx mcp` subprocess writes an outcome row to the
isolated `usage.db`, then a second, independently-spawned `styx learn
--scorecard` process — with `HOME`/`XDG_CONFIG_HOME`/`PATH` reconstructed from
`filepath.Dir(proj)` to match the server's env — reads that same db and
renders the aggregated `claude × trivial: 1/1 clean` cell (a message ≤50
chars tags the `trivial` signal); no ollama is involved since `--scorecard`
is the deterministic aggregation layer. `TestLiveSmoke` is
skipped unless `STYX_E2E_LIVE=1`, in which case it runs `styx doctor` and a
live ollama one-shot dispatch through the real routing brain model — meant to
be run manually and rarely, since it consumes real quota/local resources.

## Planned work

Checkpoint B dogfooding and later safety/provenance/trust hardening are tracked
in `docs/superpowers/plans/2026-06-12-styx-repl-orchestrator.md`.
