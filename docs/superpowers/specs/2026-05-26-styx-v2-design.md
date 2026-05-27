# Styx v2 — Design Spec

| Field | Value |
|---|---|
| Date | 2026-05-26 |
| Author | Ishaan Batra (brainstormed with Claude) |
| Status | Approved for planning |
| Supersedes | `~/bin/styx` (387-line bash script, Hoot-scoped) |
| Scope | Slice A + B + C of the larger Styx-as-Jarvis vision |

---

## 1. Summary

A Go-based personal CLI that orchestrates four AI channels — **Claude** (via `claude` CLI, $100/mo plan), **Codex** (via `codex` CLI, $20/mo ChatGPT plan), **Gemini** (via `gemini-cli` + free dev API), and **Ollama** (local `qwen2.5-coder` 7b/14b) — across **any** of the user's Git repositories. Verb-first CLI surface (`styx research`, `styx plan`, `styx build`, etc.) with an editable, hand-curated rules table that selects the right channel per request based on verb, signals, project context, and remaining budget. Fallback chains keep work moving when a channel is capped or unreachable.

Replaces the existing bash `styx` in-place. No phased migration.

---

## 2. Goals

1. **Global, not Hoot-scoped.** Works in every git repo without configuration; auto-registers new projects on first use.
2. **Leverage every available channel** rather than burning Claude on tasks better suited to other models.
3. **Curate-by-rules-table.** The routing decision is transparent, editable, and debuggable (`styx route --explain`).
4. **Never block on a capped channel.** Every rule has a fallback chain; budget-aware degradation kicks in before the cap, not after.
5. **Zero setup in new projects.** `cd` into a repo, run any verb — Styx self-configures.
6. **Preserve Hoot's existing workflow.** Briefs/plans for `ai-ta-backend` continue to land in `docs/research`/`docs/plans` exactly as they do today.

## 3. Non-goals (this slice)

- Natural-language entrypoint (`styx ask "<text>"`). Verb-first only.
- Persistent memory / brief retrieval / semantic search. Index schema reserved; population deferred.
- Pipeline orchestration (`styx auto <goal>`). Per-verb routing first; chaining later.
- Rich usage dashboard. Single-line `styx budget` only.
- Daemon / proactive surface. Architecture supports it (stateless channels, files-on-disk state) but no daemon code in v1.
- Voice input. Out of scope.
- Cross-machine state sync. Out of scope.
- Non-macOS support. Mac-only for v1 (Keychain dependency).
- Web UI. Out of scope, indefinitely.

## 4. Decisions log

| # | Decision | Rationale |
|---|---|---|
| 1 | Rewrite, don't extend, the bash script | 387 lines of bash, no tests, no plugin surface; every new feature compounds maintenance |
| 2 | **Go** | HTTP-orchestration CLI with a path to daemon mode; ~5ms cold start; mature LLM SDKs; faster iteration loop than Rust even with AI writing |
| 3 | **Verb-first CLI** (vs NL entrypoint) | Predictable, debuggable, fails closed. NL layer can be added later as a wrapper. |
| 4 | **Hand-curated rules table** (vs LLM-as-router) | Transparent, zero-token routing, encodes user's domain knowledge of which model is best at what |
| 5 | Wire **all four channels** (Claude, Codex, Gemini-CLI, Ollama) | Match user's actual subscription set; Codex CLI + Gemini-CLI unlock the $20+$20/mo that's currently dead weight |
| 6 | **In-place replacement**, no phased migration | User wants a clean restart, not coexistence |
| 7 | **XDG config layout** (`~/.config/styx/`) | Standard, predictable, separates config/state/cache cleanly |
| 8 | **macOS Keychain for secrets** | Replaces plaintext `GEMINI_API_KEY` in `~/.zshrc`; one-time `styx migrate-secrets` flow |
| 9 | **Project auto-registration** on first use | Walks pwd up for `.git`; sniffs language; writes to `projects.toml` with sensible defaults |
| 10 | **Per-repo `.styx.toml` overrides** | Lets sensitive repos block cloud channels without affecting global config |

---

## 5. Architecture

```
                    ┌──────────────────┐
                    │   styx CLI       │  one Go binary, ~5ms cold start
                    └────────┬─────────┘
                             │
        ┌────────────────────┼────────────────────┐
        │                    │                    │
   ┌────▼─────┐       ┌──────▼──────┐      ┌──────▼─────┐
   │  Verbs   │       │   Router    │      │  Context   │
   │ (cmd/*)  │──────▶│ (internal/  │◀─────│ (project/  │
   │          │       │   router)   │      │   pwd/git) │
   └──────────┘       └──────┬──────┘      └────────────┘
                             │
              ┌──────────────┼──────────────┬─────────────┐
              │              │              │             │
        ┌─────▼────┐   ┌─────▼────┐   ┌─────▼─────┐ ┌────▼────┐
        │ channel/ │   │ channel/ │   │ channel/  │ │ channel/│
        │ claude   │   │ codex    │   │ gemini    │ │ ollama  │
        └──────────┘   └──────────┘   └───────────┘ └─────────┘
                             │
                      ┌──────▼──────┐
                      │   Budget    │  per-channel usage tracker
                      │  Tracker    │  ~/.config/styx/state/usage.db
                      └─────────────┘
```

### Package layout

```
github.com/ishaanbatra/styx/
├── cmd/
│   └── styx/                  # main binary; one file per verb
│       ├── main.go            # dispatcher
│       ├── research.go
│       ├── deep_research.go
│       ├── plan.go
│       ├── build.go
│       ├── review.go
│       ├── grunt.go
│       ├── think.go
│       ├── explain.go         # NEW
│       ├── summarize.go       # NEW
│       ├── critique.go        # NEW
│       ├── check.go
│       ├── budget.go          # NEW: `styx budget`
│       ├── route.go           # NEW: `styx route --explain`
│       ├── project.go         # NEW: `styx project {ls,rm,rename}`
│       └── migrate_secrets.go # NEW: one-time secret migration
├── internal/
│   ├── router/                # rule evaluation + decision
│   ├── channel/               # Channel interface + 4 impls
│   │   ├── claude/
│   │   ├── codex/
│   │   ├── gemini/
│   │   └── ollama/
│   ├── project/               # discovery + registry
│   ├── budget/                # usage tracking (sqlite)
│   ├── brief/                 # research/plan file I/O
│   ├── config/                # routing.toml, projects.toml, Keychain
│   └── signals/               # extract size/complexity/lang signals
├── docs/
│   └── superpowers/specs/     # this doc
├── testdata/
│   └── scenarios/             # e2e golden tests
└── go.mod
```

## 6. Components

### 6.1 `cmd/styx` (verb dispatcher)

Each verb is a thin file. No business logic. Responsibilities:

1. Parse args
2. Call `project.Current()` for context
3. Call `signals.Extract(verb, args, project)` for routing signals
4. Call `router.Route(req)` for a decision
5. Call the chosen channel
6. Persist outputs (brief, plan, log)
7. Print result to user

**Simple verbs** (`plan`, `build`, `grunt`, `think`, etc.) make one router call and one channel call.

**Compound verbs** (`research`, `review`) orchestrate multiple router calls, each with a different verb key:
- `research <q>` issues two router calls in sequence: `Route(verb="research")` for the draft (typically Gemini), then `Route(verb="research.critic")` for the critique (typically Codex). The dotted suffix is a routing-key convention so each sub-call can target a different channel without the user having to think about it.
- `review` uses a different mechanism: a single rule with `parallel = [...]` lists multiple channels that run concurrently and a `synthesize_with` channel that merges the outputs.

Both patterns are documented inline in `routing.toml` examples (Section 7.2).

### 6.2 `internal/router`

```go
type Request struct {
    Verb    string
    Args    []string
    Project Project
    Signals []string  // e.g. ["large_context", "complex", "trivial"]
}

type Decision struct {
    Channel  string         // "claude"
    Model    string         // "sonnet-4-6"
    Fallback []ChannelModel // ordered fallback chain
    Rule     int            // index into routing.toml for explainability
}

func Route(req Request) (Decision, error)
func Explain(req Request) string  // human-readable trace
```

Reads `~/.config/styx/routing.toml` on every call (cheap, hot file, ~1ms). Rules evaluated top-to-bottom; first match wins. Each rule may specify `use` (single channel) OR `parallel + synthesize_with` (multi-channel review pattern).

### 6.3 `internal/channel/*`

All channels implement:

```go
type Channel interface {
    Name() string
    Send(ctx context.Context, req ChannelRequest) (ChannelResponse, error)
    BudgetState(ctx context.Context) (BudgetState, error)
}

type ChannelRequest struct {
    Model       string
    System      string
    Prompt      string
    Attachments []Attachment   // file refs for context
    Interactive bool           // for `build` verb
}

type ChannelResponse struct {
    Text         string
    EstTokensIn  int
    EstTokensOut int
}
```

Per-impl notes:

- **claude/** — shells out to `claude` CLI. For interactive verbs (`build`), execs `claude` directly with `cd` to project dir. For one-shot (`plan`, `review`), uses `claude -p "<prompt>"`. Reads `claude --usage` (or equivalent) for `BudgetState`.
- **codex/** — shells out to `codex` CLI. Same shape as Claude. BudgetState reads from `codex usage` output if available, else falls back to local request-count estimate.
- **gemini/** — preferred path: shell out to `gemini-cli` (uses $20 sub). Fallback path: direct HTTP to `generativelanguage.googleapis.com` with the Keychain-stored API key (free dev tier).
- **ollama/** — HTTP POST to `localhost:11434/api/chat`. Auto-launches the Ollama app if `/api/tags` is unreachable (`open -a Ollama`, then poll up to 20s). BudgetState always returns "unlimited."

### 6.4 `internal/project`

```go
type Project struct {
    Name         string   // friendly alias, e.g. "hoot-backend"
    Path         string   // absolute repo root
    Language     string   // "python" | "typescript" | "go" | "rust" | "mixed" | "unknown"
    ResearchDir  string   // relative to Path, default "styx/research"
    PlansDir     string   // relative to Path, default "styx/plans"
    DefaultVerbs []string // optional, shown in `styx help` when inside this project
}

func Current() (Project, error)            // pwd-based, auto-registers if new
func Resolve(alias string) (Project, error)
func List() ([]Project, error)
func Register(p Project) error
func Forget(alias string) error
```

Discovery algorithm:
1. Walk pwd up looking for `.git/`. That dir is the candidate root.
2. Look up `candidate_root` in `projects.toml`. If found, return.
3. If not found, sniff language: look for `pyproject.toml`/`setup.py` → python, `package.json` → typescript/javascript, `Cargo.toml` → rust, `go.mod` → go. Multiple → "mixed".
4. Default friendly name = basename(candidate_root), lowercased, hyphenated. If collision, append `-2`, `-3`, etc.
5. Auto-register with default research/plans dirs and write to `projects.toml`.
6. Print a one-line notice: `[styx] registered new project: hoot-backend (python) at /Users/.../ai-ta-backend`.

### 6.5 `internal/budget`

```go
type Usage struct {
    Channel  string
    Window   time.Duration  // monthly for paid, daily for free tiers
    UsedPct  float64
    LimitHit bool
    CooldownUntil time.Time
}

func Record(ctx, channel, verb string, tokensIn, tokensOut int, ok bool) error
func State(channel string) (Usage, error)
func MarkCooldown(channel string, until time.Time) error
```

Storage: append-only sqlite at `~/.config/styx/state/usage.db`. Single table:

```sql
CREATE TABLE usage (
    ts          INTEGER NOT NULL,    -- unix epoch
    channel     TEXT    NOT NULL,
    verb        TEXT    NOT NULL,
    tokens_in   INTEGER NOT NULL,
    tokens_out  INTEGER NOT NULL,
    success     INTEGER NOT NULL,    -- 0/1
    error_kind  TEXT                 -- null | "timeout" | "429" | "5xx" | "other"
);
CREATE INDEX usage_channel_ts ON usage (channel, ts DESC);
```

Caps and windows live in `routing.toml [budget]` section. UsedPct is computed at read time from the sqlite log + caps. No background process needed.

### 6.6 `internal/brief`

Preserves current Styx's research/plan output format. Each project's brief and plan dirs are configurable via `projects.toml`; default is `styx/research` and `styx/plans` inside the repo. Hoot's special paths (`docs/research`, `docs/plans`) become a regular `projects.toml` entry, not a hard-coded default.

### 6.7 `internal/config`

```go
type Routing struct {
    Budget BudgetCaps
    Rules  []Rule
}

type Rule struct {
    Verb           string
    Signals        []string
    Use            string         // "claude:sonnet-4-6"
    Parallel       []string       // for review-style verbs
    SynthesizeWith string
    Fallback       []string
}

func LoadRouting() (Routing, error)
func LoadProjects() ([]Project, error)
func SaveProjects([]Project) error      // atomic write via tmpfile + rename
func Secret(name string) (string, error) // reads from Keychain
```

Keychain access uses `security` CLI: `security find-generic-password -s styx -a <name> -w`. Stored via `security add-generic-password -U -s styx -a <name> -w <value>`.

### 6.8 `internal/signals`

Pure function: `(verb, args, project) -> []string`. Examples of signals it emits:

- `trivial` — args < 50 chars and verb is `grunt`
- `complex` — args contain "architecture"/"refactor"/"migrate"/"redesign" OR file count in args > 20
- `large_context` — total estimated tokens of referenced files > 50k
- `lang:python`, `lang:typescript`, etc. — from project metadata
- `interactive` — verb is `build`

Signals are matched against rule `signals = [...]` arrays (AND semantics: all listed signals must be present).

---

## 7. Configuration

### 7.1 File layout

```
~/.config/styx/
├── routing.toml        # rules table + budget caps (user-edited)
├── projects.toml       # registered projects (mostly auto-managed)
├── styx.toml           # general settings (log level, default editor, etc.)
└── state/
    ├── usage.db        # sqlite append-only usage log
    └── briefs.idx      # local FTS index of briefs/plans (reserved; populated later)

~/.local/share/styx/logs/    # rolling log files

~/.cache/styx/               # discardable caches
```

### 7.2 `routing.toml` (the file the user actively edits)

```toml
# Rules evaluated top-to-bottom, first match wins.
# Use `styx route --explain <verb> "<text>"` to see which rule fired.
#
# Dotted verb keys (e.g. "research.critic") are issued by compound verbs that
# make multiple router calls under the hood. See spec Section 6.1.

[budget]
claude.cap_pct       = 80    # auto-degrade above 80% of monthly plan
codex.cap_pct        = 80
gemini_free.cap_pct  = 70    # free dev API daily cap
gemini_paid.cap_pct  = 80    # gemini-cli authenticated path

# ── research ──
[[rule]]
verb = "research"
use  = "gemini:flash"
fallback = ["gemini:pro", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "research.critic"
use  = "codex:gpt-5"
fallback = ["ollama:qwen2.5-coder:14b"]

# ── plan ──
[[rule]]
verb = "plan"
signals = ["complex"]
use  = "claude:opus-4-7"
fallback = ["claude:sonnet-4-6", "codex:gpt-5"]

[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
fallback = ["codex:gpt-5", "ollama:qwen2.5-coder:14b"]

# ── build (interactive) ──
[[rule]]
verb = "build"
use  = "claude:interactive"
fallback = ["codex:interactive"]

# ── review (parallel two-channel) ──
[[rule]]
verb = "review"
parallel = ["claude:sonnet-4-6", "codex:gpt-5"]
synthesize_with = "claude:sonnet-4-6"

# ── grunt / think ──
[[rule]]
verb = "grunt"
signals = ["trivial"]
use  = "ollama:qwen2.5-coder:7b"

[[rule]]
verb = "grunt"
use  = "ollama:qwen2.5-coder:14b"

[[rule]]
verb = "think"
signals = ["deep"]
use  = "claude:sonnet-4-6"

[[rule]]
verb = "think"
use  = "ollama:qwen2.5-coder:14b"

# ── explain / summarize / critique ──
[[rule]]
verb = "explain"
signals = ["large_context"]
use  = "gemini:pro"
fallback = ["claude:sonnet-4-6"]

[[rule]]
verb = "explain"
use  = "ollama:qwen2.5-coder:14b"

[[rule]]
verb = "summarize"
use  = "gemini:pro"
fallback = ["claude:sonnet-4-6", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "critique"
use  = "codex:gpt-5"
fallback = ["claude:sonnet-4-6", "ollama:qwen2.5-coder:14b"]
```

### 7.3 `projects.toml` (auto-managed, user-editable)

```toml
[[project]]
name = "hoot-backend"
path = "/Users/ishaanbatra/Documents/GitHub/ai-ta-backend"
language = "python"
research_dir = "docs/research"
plans_dir    = "docs/plans"
default_verbs = ["plan", "build", "review"]

[[project]]
name = "hoot-student-ui"
path = "/Users/ishaanbatra/Documents/GitHub/ai-ta-student-ui"
language = "typescript"

[[project]]
name = "hoot-teacher-ui"
path = "/Users/ishaanbatra/Documents/GitHub/ai-ta-teacher-ui"
language = "typescript"

[[project]]
name = "voiceresumebot"
path = "/Users/ishaanbatra/Documents/GitHub/VoiceResumeBot"
language = "python"
```

(Other projects auto-register on first use.)

### 7.4 Per-repo `.styx.toml` override

```toml
[overrides]
default_channel_allowlist = ["ollama:*"]   # block cloud channels for this repo
```

Loaded when `project.Current()` runs. Overrides anything in global `routing.toml`.

---

## 8. Secrets

One-time migration via `styx migrate-secrets`:

1. Scans `~/.zshrc`, `~/.bashrc`, `~/.bash_profile`, `~/.zprofile` for known secret-shaped env vars (`*_API_KEY`, `*_TOKEN`).
2. For each match, prompts: "Move to Keychain? [Y/n]"
3. If yes: writes to Keychain (`security add-generic-password -U -s styx -a <name> -w <value>`), comments out the line in the shell rc (preserving for rollback).
4. Reports what moved.

Channels at runtime fetch via `config.Secret(name)`. No env vars touched during normal operation.

---

## 9. Data flow (worked example)

`$ styx plan "add streaming to /chat endpoint"` from `~/Documents/GitHub/ai-ta-backend`:

```
1. cmd/styx/plan.go            parses verb=plan, args=...

2. project.Current()           walks up → finds .git → matches "hoot-backend" (python)

3. signals.Extract(req)        no "architecture" keyword → no "complex" signal
                               args len = 36 → not "trivial"
                               → signals = ["lang:python"]

4. router.Route(req)           consults routing.toml
                               first rule "verb=plan, signals=[complex]" does NOT match
                               second rule "verb=plan" matches
                               budget.State("claude") → 47% used, under 80% cap
                               returns Decision{
                                 channel: "claude",
                                 model:   "sonnet-4-6",
                                 fallback: ["codex:gpt-5", "ollama:qwen:14b"],
                                 rule: 4,
                               }

5. brief.LoadLatest(project)   reads latest *-brief.md from
                               /…/ai-ta-backend/docs/research/

6. channel/claude.Send(...)    shells out: claude -p "<plan prompt + brief>"
                               captures stdout
                               estimates tokens (chars / 4)

7. budget.Record(...)          appends row to usage.db

8. brief.WritePlan(...)        writes /…/ai-ta-backend/docs/plans/<ts>-plan.md

9. stdout to user:
     ✓ Plan saved: docs/plans/20260526-153012-plan.md
       Channel: claude:sonnet-4-6 (rule #4)
       Budget:  claude 47% → 48% of monthly plan
```

---

## 10. Error handling

| Failure mode | Behavior |
|---|---|
| Channel returns transport error (network, 5xx, CLI crash, timeout) | Walk fallback chain in order, log each attempt, surface final error with full chain in user-facing message |
| Channel returns 429 / explicit budget exhausted | Mark channel cooled-down (N min, default 15) in `usage.db`. Walk fallback. Don't retry capped channel until cooldown expires. |
| Budget cap pre-check fails (e.g. `claude > 80%`) | Skip primary, log "auto-degraded due to budget cap", use first fallback directly. Zero wasted tokens. |
| No matching rule | Default to `ollama:qwen2.5-coder:14b`. Print one-line warning suggesting a routing.toml addition. |
| Channel binary missing on PATH (e.g. `gemini-cli` not installed) | One-time warning at first use: "gemini-cli not found. Install with: `npm install -g @google/gemini-cli`. Falling back to API key." |
| Project auto-registration write fails | Run uses in-memory project, prints warning, continues. Manual `styx project add` available. |

**Global circuit breaker:** any channel returning ≥5 errors in 60s gets marked unhealthy for 5 min. Prevents thrashing the same broken channel.

---

## 11. Testing

Three layers, all alongside the code in `_test.go`:

1. **Unit tests** — pure functions: router rule evaluation, signal extraction, project sniffing, budget math. Table-driven. No mocks.

2. **Channel contract tests** — each `Channel` impl runs through a shared suite verifying:
   - Honors `ctx` cancellation
   - Wraps errors with channel name
   - Times out at configured limit
   - Reports budget state without erroring (even when channel itself is unreachable)
   Real network calls gated behind `-tags=integration`.

3. **End-to-end golden tests** — `testdata/scenarios/*.txt` describe `(project state, verb, args, fake channel responses)` and assert routing decision + final output. Fast, no network, easy to extend.

**Coverage targets:**
- `internal/router`: 80%+
- `internal/budget`: 80%+
- `internal/project`, `internal/signals`, `internal/config`: 70%+
- Channels: 60%+ (lower because integration paths)
- `cmd/styx`: 50%+ (mostly glue)

---

## 12. Migration from current Styx

Single step, no phasing:

1. Build new binary: `go build -o ~/bin/styx ./cmd/styx`
2. Install command moves old script: `mv ~/bin/styx ~/bin/styx.old.bak` (one-time safety; user can `rm` whenever)
3. Drop new binary into `~/bin/styx`
4. Run `styx migrate-secrets` once to move `GEMINI_API_KEY` from `~/.zshrc` to Keychain
5. Run `styx project add` for the 3 Hoot repos (or just `cd` into each one and run any verb — auto-registers)

Existing briefs and plans in `ai-ta-backend/docs/research` and `docs/plans` stay where they are; the `projects.toml` entry preserves those paths.

---

## 13. Open questions / risks

1. **Codex CLI auth lifecycle.** Need to verify the `codex` CLI's sign-in flow actually persists and that BudgetState is meaningfully readable. If not, fall back to local request-count estimate.
2. **Gemini-CLI installation friction.** If `gemini-cli` requires npm and the user doesn't have node/npm globally, the install step is more than one command. Plan acknowledges this; spec doesn't block on it.
3. **Token-count estimation accuracy.** Char/4 is a rough proxy. If budget tracking matters precisely, swap in a real tokenizer (tiktoken/equivalent) per channel later. v1 uses the rough estimate; conservative caps absorb the error.
4. **Interactive verbs and parallel channels.** `build` is interactive — fallback to `codex:interactive` works, but switching channels mid-session isn't viable. Treat interactive fallback as "next session uses fallback," not "live failover."
5. **`.styx.toml` repo override + version control.** Should the override file be gitignored by default? Probably yes; document it.

---

## 14. Deferred slices (out of scope for v1)

| Slice | Why deferred | Architectural readiness |
|---|---|---|
| **D. Persistent memory + brief retrieval** | Needs FTS/embedding infrastructure; layer on top of brief writer | `briefs.idx` slot reserved in state dir |
| **E. Pipeline orchestration** (`styx auto <goal>`) | Trust per-verb routing first before chaining | Channels are stateless; verb dispatch is reentrant |
| **F. Rich usage dashboard** | v1 ships `styx budget` (one-line); rich view later | `usage.db` schema supports it |
| **G. Daemon + proactive surface** | Biggest UX/scope expansion; defer until brain is solid | All state on disk; channels stateless; no global mutables — drop-in compatible |
| **NL entrypoint** (`styx ask "<text>"`) | Verb-first first; NL is a wrapper | Decoupled from router internals |
