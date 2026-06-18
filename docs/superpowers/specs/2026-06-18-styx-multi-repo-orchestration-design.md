# Styx Multi-Repo Orchestration + Run-From-Anywhere — Design

**Date:** 2026-06-18
**Status:** Approved (pending final user review)

## Problem

Styx is a per-CWD, single-project orchestrator. Every entry point resolves a
single project from the current working directory (`project.Current()`,
`internal/project/project.go:25` — the only `os.Getwd()` in the tree) and pins
all work to it: the REPL session binds one `proj` (`cmd/styx/repl.go:32`), the
agent manager binds one `Project` (`repl.go:446`), memory is one per-project db
(`repl.go:391`), audit is one per-project dir (`repl.go:416`). Most verbs hard
-error with `ErrNotInGitRepo` when run outside a repo.

This blocks the way the user actually works. A real request looks like:

> "examine why the embedding process in **ai-ta-backend** isn't working when
> uploaded through the frontend in **ai-ta-teacher-ui**, and test it by asking a
> question in **ai-ta-student-ui**"

That is **one coordinated task whose context spans three repos**, worked through
sequentially (inspect backend → trace the upload path in teacher-ui → verify in
student-ui). Today styx cannot see more than one repo at a time, and cannot be
driven from the user's home directory.

This design makes styx **conversational, brain-routed, multi-repo**: from any
directory, the user names repos in a request and the brain works across them in
one sequential task with **multi-root context** (a primary repo where edits land
plus supporting repos attached for reading/testing). It also makes every verb
runnable from anywhere via an explicit target.

## Goals / non-goals

**Goals**

- Run any styx verb from any directory by naming a target (`--project` / `--dir`),
  not by `cd`-ing into a repo.
- A conversational REPL whose brain resolves repos by the names the user mentions
  and binds them into one session.
- Multi-root context for a single dispatch: a primary repo (cwd, where edits and
  commits land) plus additional repos attached as read/test roots.
- Session-scoped memory and audit so a coordinated cross-repo task reasons
  coherently across all bound repos.
- Make the "run several styx processes in several terminals" pattern safe
  (concurrency-hardened shared state).
- Fix the latent path/resolver bugs this work sits on top of.

**Non-goals (deliberately out of scope)**

- **In-process parallel fleet.** Independent parallel work is "open 3 terminals";
  we make that *safe* rather than reimplementing OS concurrency inside one
  session.
- **Parallelism within a single task.** The motivating tasks are causally
  sequential; there is nothing to parallelize within them. (`runDispatches`
  already runs independent dispatches concurrently — that capability is retained,
  not extended.)
- **The brain silently editing `routing.toml`.** Routing stays a human-edited,
  transparent table (per CLAUDE.md).
- **The self-improvement evaluator / apply-back loop.** That is a separate spec
  (#2). This design only lays its data seam (run-id + per-event project tag).

## Core model

- **Target = a resolved project, not the CWD.** A single resolver turns
  `{--project alias, --dir path, cwd}` into a `Project`. Every verb and the brain
  use it. Outside a repo with no target is a *clear error*, never a silent
  fallback.
- **A session binds a set of repos.** The REPL session holds a set of bound
  projects rather than one. Repos enter the set lazily, the moment the user names
  one (resolved against the registry). An optional launch binding seeds the set.
- **One dispatch, multiple roots.** A brain dispatch carries a primary `project`
  plus `extra_roots`; the primary becomes the agent's cwd, the extras are attached
  via the provider CLI's `--add-dir`.
- **Sequential by nature, concurrent across processes.** Within a task the brain
  works repos in order; true parallelism is multiple styx processes, made safe by
  WAL-mode budget sqlite and existing per-repo locks.

## Architecture changes

```
internal/target     NEW  one resolver: {alias|dir|cwd} -> Project; no silent fallback
internal/project    EDIT stable ID (path hash); scan (walk-down); fix findGitRoot
internal/brain      EDIT Dispatch.Project + Dispatch.ExtraRoots; registry in prompt
internal/agent      EDIT DispatchSpec project/working-dir override + ExtraRoots
internal/channel    EDIT Request.ExtraRoots -> --add-dir (claude, codex; agy has it)
internal/budget     EDIT Record gains project + run tag; open db WAL + busy_timeout
internal/memory     ----  no API change: recall already variadic; db path is caller-side
internal/audit      EDIT Record gains a project field
cmd/styx            EDIT global --project/--dir flags; multi-project replSession;
                         memory db + audit dir re-keyed by ID; `project scan`;
                         all verbs route through internal/target
```

## 1. Foundations (shared layer)

### 1a. Unified target resolver — `internal/target`

A single resolver replaces the two divergent ones that exist today:

- `resolveTarget` (`cmd/styx/dispatch.go:341`) — **silently falls back to the CWD
  repo when an alias does not resolve** (`if err == nil { return p }; return
  project.Current()`). This is a real footgun and violates the repo's own "never
  swallow errors" rule.
- `resolveProjectArg` (`cmd/styx/intel.go:96`) — does prefix matching + on-the-fly
  registration, used by only a couple of verbs.

New API (sketch):

```go
// internal/target
type Spec struct {
    Alias string // from --project or a name the brain/user gave
    Dir   string // from --dir
    Cwd   string // fallback (usually os.Getwd())
}

// Resolve returns the project for the spec. Precedence: Alias -> Dir -> Cwd.
// Alias resolution: exact name -> unique prefix -> error. Never silently
// falls back to the CWD when an explicit target was given and failed.
func Resolve(spec Spec) (project.Project, error)
```

- Precedence: explicit `Alias` → explicit `Dir` → `Cwd` walk-up.
- Alias resolution: exact `Name` match → unique prefix match → **error** listing
  candidates. An ambiguous prefix is an error, not a guess.
- Both old resolvers are deleted; every caller moves to `target.Resolve`.

### 1b. Global `--project` / `--dir` flags

`parseGlobalFlags` (`cmd/styx/main.go`) currently knows only
`--quiet`/`--verbose`. Add `--project <alias>` and `--dir <path>`. Every verb
resolves its target through `target.Resolve` instead of calling
`project.Current()` directly. Result: `styx research/plan/auto/review/context/
runs` and the REPL all run from `~`. Outside a repo with no target → an error
that explains how to target (name a project, pass `--dir`, or `cd` in), replacing
the bare `ErrNotInGitRepo`.

### 1c. `styx project scan [root] [--depth N]`

Git detection today only walks **up** (`findGitRoot`). Add a bounded walk-**down**
that finds git roots under `root` (default: `~` or a configured code dir) and
bulk-registers them, pruning `node_modules`, `vendor`, `.git`, and not descending
into a repo once found. Default depth bounded (e.g. 4) to stay fast. Reuses
`autoRegister`. This populates the registry the brain resolves names against, so
"point styx at all my repos" is one command.

### 1d. Stable project ID + state re-key

On-disk state is keyed by `proj.Name` today (memory db `repl.go:391`, audit dir
`repl.go:416`, intel index). `project rename` orphans all of it, and two repos
slugging to the same base name collide. Once repos are addressed by name, this
matters.

- Add `ID string` to `config.Project` — a stable hash of the absolute path
  (e.g. first 12 hex of `sha256(Path)`). Names/aliases remain human-facing.
- Re-key the memory db filename, audit dir, and intel index dir from `Name` to
  `ID`.
- **Migration:** idempotent rename of existing `Name`-keyed dirs/files to
  `ID`-keyed, run lazily on open and/or as a `styx doctor` step. Backfill `ID`
  for existing `projects.toml` entries on load.

### 1e. Multi-terminal hardening (shared global state)

Three concurrent styx processes share the budget sqlite log and 5h/weekly
windows under `~/.config/styx`, plus the models cache.

- Open the budget sqlite with **WAL journal mode + a busy_timeout** so concurrent
  writers don't throw "database is locked".
- Confirm `projects.toml` and the models cache keep atomic tmp+rename writes.
- Per-repo `.lock` files (`internal/pipeline/lock.go`) already prevent two runs
  clobbering the *same* repo, so different repos in different terminals are safe;
  only the shared DB needs the WAL change.

### 1f. Run-id + project tag on budget events

`budget.Record` records `(channel, model, verb, tokens, success, error_kind)`
with no repo or run association (`Verb` is just `"plan"`/`"thread"`). Add:

- `project` (the resolved project ID) on every usage event.
- A `run` correlation id minted per REPL session and per verb invocation.

This makes per-repo usage attributable now, and is the exact seam the
self-improvement spec (#2) will read back. No evaluator or apply-back is built
here.

## 2. Brain & channel multi-root

### 2a. Brain `Dispatch`

`brain.Dispatch` (`internal/brain/action.go:32`) gains:

```go
Project    string   `json:"project,omitempty"`     // primary repo (name); cwd + commit target
ExtraRoots []string `json:"extra_roots,omitempty"` // additional repo names, attached via --add-dir
```

- Update `ActionSchema` (`action.go:128`) to include `project` and `extra_roots`,
  and `Valid()` accordingly (a dispatch with an unresolvable project name is
  invalid → brain retry → escalate to the user, never a silent guess).
- The brain **prompt** (`internal/brain/prompt.go`) gets the registry injected:
  the set of bound + known projects with one-line language/description, so the
  model can map "the ai-ta-backend repo" to a project name. Names it cannot map →
  `escalate`.
- `CLIOptions` already exists with a `// e.g. --add-dir` comment (`action.go:36`);
  structured `Project`/`ExtraRoots` supersede ad-hoc flag injection for repo
  targeting, leaving `CLIOptions` for genuinely extra flags.

### 2b. Agent `DispatchSpec`

`agent.DispatchSpec` gains a project/working-dir override plus resolved
`ExtraRoots []string` (absolute paths), so a dispatch can target a repo other
than the session's current focus. The session resolves `Dispatch.Project` /
`Dispatch.ExtraRoots` (names) to projects/paths before calling the manager, and
routes to (or lazily creates) that project's manager.

### 2c. Channel `Request`

`channel.Request` (`internal/channel/channel.go:18`) gains
`ExtraRoots []string`. Adapters translate it to the provider CLI's multi-root
flag: claude and codex gain `--add-dir` handling (agy already passes `--add-dir`,
`internal/channel/agy/agy.go:51`). This removes the single-`WorkingDir`
bottleneck (`channel.go:25`).

## 3. Session model (the heart)

`replSession` (`cmd/styx/repl.go:32`) becomes multi-project:

- **Bound set.** Holds a set of bound projects keyed by ID, each with a lazily
  created per-project `agent.Manager` + threads (cwd differs per repo). A repo is
  bound on first reference (resolved via `target.Resolve` against the registry).
- **Session-scoped recall.** `memory.Recall` already takes variadic stores
  (`repl.go:57`). Recall becomes
  `Recall(ctx, emb, utterance, 5, <each bound repo's db>..., glob)`, so a fact
  learned while looking at backend surfaces when focus shifts to student-ui. (The
  existing per-item `Scope` tag stays available as a finer in-db filter; the
  cross-repo behavior comes from passing multiple stores.)
- **One session audit stream.** A single audit log for the session; each record
  tagged with the `project` it touched (audit records gain a project field).
- **Current focus + per-dispatch override.** A current-focus pointer provides the
  default target; `Dispatch.Project` overrides per turn; `Dispatch.ExtraRoots`
  attaches supporting repos for a coordinated task.
- **Launch binding (optional).** `styx <repo...>` or `styx --workspace <name>`
  seeds the bound set; otherwise the set grows as the user names repos. The REPL
  header and `/threads` show per-repo state.

The single-repo case is just "bound set = {cwd repo}", so the existing UX is
unchanged when run inside one repo.

## 4. Backward compatibility

- Single-repo REPL, `styx "..."` one-shot, and all verbs behave identically when
  run inside a repo with no flags.
- The state re-key migration (1d) is idempotent and safe to re-run.
- `styx auto --resume` `state.json` stays loadable — no breaking field changes; if
  a run gains a project/run tag, it is additive with a `State.Version` bump per
  the resume contract.
- Memory/audit written under old `Name` keys are migrated, not abandoned.

## 5. Error handling & safety

- Resolver failures are loud (criticism: no silent CWD fallback). Ambiguous or
  unknown targets list candidates.
- An unresolved repo name from the brain → escalate / ask the user, never a
  silent guess.
- Ship-risk confirmation (`repl.go:84`) is unchanged; multi-root edits are still
  gated by the same risk model.
- Concurrency across terminals is safe via WAL + busy_timeout; same-repo
  collisions are blocked by existing locks.

## 6. Testing

- `internal/target`: table-driven resolver tests — exact alias, unique prefix,
  ambiguous prefix (error), `--dir`, cwd walk-up, unknown (error), no-silent
  -fallback assertion.
- `project scan`: run against a temp tree with nested + vendored dirs; assert the
  right roots register and pruning works.
- Brain: schema tests that `project` / `extra_roots` round-trip and that an
  unresolvable project name fails `Valid()`.
- Channel adapters: assert claude/codex emit `--add-dir` for each `ExtraRoot`.
- Session: extend `cmd/styx/repl_test.go` to bind two repos against the scripted
  `testdata/fakeagent` and assert a dispatch targets the right repo with the
  other attached, and that recall spans both.
- Migration: idempotency test (run twice, stable result; old keys migrated).
- Budget: a concurrent-writers test that WAL + busy_timeout prevents "database is
  locked".

## 7. Documentation (drift contract)

In the same commits as the code:

- `docs/ARCHITECTURE.md` — new `internal/target` package, multi-project session,
  `project scan`, run-id/project tagging, ID-keyed state; bump `last_verified`.
- `README.md` — verb table: `project scan`, global `--project` / `--dir` flags.

## 8. Scope boundary with spec #2 (self-improvement)

This spec lands the **data seam** self-improvement needs — run-id, per-event
project attribution, and richer multi-repo usage — but explicitly does **not**
build the evaluator, the usage↔outcome join, or any apply-back surface. Those,
plus the thesis-safe "suggested-rule with human gate" shape, are the subject of
the separate self-improvement spec. Building multi-repo first is deliberate: it
is lower-risk, has the clearer demo, and produces the richer dataset the learning
loop later consumes.

## Open questions

None blocking. Two deferred-by-choice items, revisitable during planning:

- Whether `--workspace` named groups ship in v1 or as a fast follow (lazy
  name-binding covers the core need without them).
- Final form of the migration trigger (lazy-on-open vs `doctor`-only vs both).
