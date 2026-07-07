# Styx

Personal multi-model dev orchestration CLI. Routes work between Claude, Codex,
Antigravity (agy / Gemini), and Ollama via a hand-curated rules table.

## Install (one shot)

    ./install.sh        # builds + drops binary at ~/bin/styx (backs up any existing one)

Then migrate any plaintext secrets out of your shell rc:

    styx migrate-secrets

If you're upgrading from v0.1 with `gemini:*` references in routing.toml, styx
auto-rewrites them to `agy:default` on first v0.2 startup (with a backup).

## Build manually

    make build       # produces ./bin/styx
    make test        # runs all tests
    make e2e         # hermetic JSON-RPC regression net against `styx mcp` (fake CLIs, no quota)
    make install     # same as install.sh

## Verbs

### Global flags

| Flag | What it does |
|---|---|
| `--project <alias>` | Run the verb against a registered project, from anywhere (exact name or unique prefix) |
| `--dir <path>` | Run the verb against the repo at `<path>`, from anywhere |

Without either flag, styx uses the current directory's repo. An explicit target
that can't be resolved is a clear error, never a silent fallback.

### Conversational

| Verb | What it does |
|---|---|
| `styx` | Launch the Claude Code conductor with the styx MCP toolbelt — from any directory, git repo or not (repl for the classic v0.2 REPL) |
| `styx <repo...>` | Launch the conductor bound to one or more named repos (first is focus) |
| `styx launch [repo...]` | Same as the two rows above, explicit verb form |
| `styx resume [sessionID]` | Relaunch the conductor resuming a Claude Code session — most recent for the cwd without an ID (`--continue`), a specific one with (`--resume <id>`); the styx toolbelt and guidance are rewired either way |
| `styx repl [repo...]` | Open the classic v0.2 REPL, kept until the conductor reaches parity |
| `styx "<anything>"` | Run one brain-routed turn, then exit |

`styx`/`styx launch` writes an MCP config binding a `styx` server (`styx mcp`)
and execs `claude --mcp-config <file> --append-system-prompt <guidance>` in
the project directory, handing control to Claude Code with styx's routing
brain, budget, memory, and dispatch surface attached as tools. Guidance comes
from `internal/guidance` plus any recalled routing preferences; extra repos
beyond the focus are added to the Claude Code session directly (`--add-dir`
per repo) and noted in that guidance so the brain also passes them as the
`dispatch` tool's `extra_roots`, giving dispatched agent threads the same
access.

Within a multi-repo classic-REPL session, `/repos` lists all bound repos
(focus-marked), `/focus <name>` switches to a different bound repo (binding it
lazily if not yet open), and naming a repo mid-conversation binds it without
restarting the session.

### Deep research + planning

| Verb | What it does |
|---|---|
| `research <q>` | Convergence loop: agy drafts, codex critiques, agy revises, until critic returns no BLOCKING/IMPORTANT (max 6 rounds) |
| `research --deep <q>` | Same loop, then chase every cited URL and summarize into a Sources Appendix |
| `plan <desc>` | Auto-refresh intel index, write `.claude/context.md`, draft a detailed plan via Claude |
| `build [target]` | Auto-refresh intel, write `.claude/context.md`, launch Claude interactively |
| `review` | Parallel Claude + Codex diff review, synthesized |

### Autonomy

| Verb | What it does |
|---|---|
| `auto <goal>` | Full pipeline: research -> intel -> plan -> execute -> test -> review -> ship. Plans on Claude, implements on Codex (Claude for `complex` goals) |
| `auto --deep <goal>` | Pipeline with citation chasing in research stage |
| `auto --no-pr <goal>` | Stop at push (don't open PR) |
| `auto --no-push <goal>` | Stop at commit (don't push) |
| `auto --resume <run-id>` | Resume an interrupted pipeline |
| `execute <plan-file>` | Apply a plan non-interactively via the `implement` route (Codex for well-scoped plans, Claude for `complex` ones) |

### Context + inspection

| Verb | What it does |
|---|---|
| `intel <project>` | Build/refresh the codebase intel index |
| `intel ls` | List cached intel indexes + freshness state |
| `context show` | Print rendered `.claude/context.md` for the current project |
| `runs ls` | List pipeline runs for the current project |
| `runs show <run-id>` | Show JSON state of a specific run |

### One-shots + admin

| Verb | What it does |
|---|---|
| `grunt <prompt>` | Local Ollama pass-through |
| `think <prompt>` | Local Ollama reasoning mode (`deep:` prefix -> Claude) |
| `explain <files...>` | Explain code files |
| `summarize <files...>` | Summarize a set of files |
| `critique <text>` | Devil's-advocate critique (Codex) |
| `check` | Dashboard: git status, latest briefs/plans |
| `budget` | Per-channel usage summary |
| `doctor [--fix]` | Preflight CLIs, capability-card drift, callable Claude tiers, and required Ollama models |
| `route --explain <verb> "..."` | Why did styx pick that channel? |
| `project ls/add/rm/rename/scan` | Manage project registry |
| `project scan [root] [--depth N]` | Walk down from `root` (default `~`), find git repos, bulk-register them (prunes node_modules/vendor; depth default 4) |
| `mcp` | Run styx as an MCP stdio server (JSON-RPC 2.0) exposing thirteen tools to OpenClaw, Claude Code, and any MCP host (see [`docs/openclaw-integration.md`](docs/openclaw-integration.md)): `route` — pick a channel for a task (budget-aware, capability-floor-aware, with fallback chain); `budget_status` — per-channel usage/limits/cooldowns; `record_usage` — log usage a consumer ran outside styx; `channel_health` — circuit-breaker state, recent failures, error-kind buckets, cooldown; `get_intel` — read the per-project codebase intel index (or one section), with staleness; `refresh_intel` — rebuild that index; `recall` — semantic top-k recall over project + global long-term memory; plus the conductor dispatch surface: `dispatch` — send work to a persistent agent thread (claude/codex/agy) or a one-shot local ollama task, synchronously or (with `background: true`) as a task you `collect` later; `thread_status` — list this project's persistent agent threads with turn counts and context usage, plus live/unclaimed background tasks; `collect` — fetch background dispatch results by `task_id` or everything finished (claims them); `memory_save` — persist a durable fact, decision, todo, or routing preference to styx memory; `pipeline_run` — run a styx pipeline (research/review/intel/auto), with a confirm-token handshake for `auto`'s ship step; `rate_dispatch` — rate a recent dispatch outcome good/bad to feed styx's learning loop |
| `hook <event>` | Internal plumbing — the route-gate hook the launcher installs into conductor sessions (Claude Code invokes it, not you); denies inline WebSearch/WebFetch/Task/external-curl + MCP web tools and redirects to dispatch/pipeline_run, per `[conductor] route_gate` |
| `migrate-secrets` | Move env-var secrets to macOS Keychain |
| `upgrade` | Re-run routing migrations manually (v0.1->v0.2 gemini->agy; v0.3 adds the `implement` verb) |
| `version` / `styx --version` | Print the styx version and exit |

## Configuration

- `~/.config/styx/routing.toml` — routing rules + budget caps (you edit this)
- `~/.config/styx/projects.toml` — registered projects (auto-managed)
- `~/.config/styx/state/usage.db` — append-only sqlite usage log
- `~/.config/styx/state/memory/<id>.db` — per-project memory, keyed by stable project id
- `~/.config/styx/state/audit/<id>/` — REPL audit logs, keyed by stable project id
- `~/.config/styx/state/threads/<id>.json` — agent thread state, keyed by stable project id
- `~/.config/styx/state/intel/<id>/index.json` — per-project codebase intel, keyed by stable project id
- `<project>/.claude/context.md` — rendered intel (Claude Code auto-loads this)
- `<project>/.styx/runs/<run-id>/` — pipeline state per run
- Secrets live in macOS Keychain under service `styx`.

## Deps

- `claude` CLI (Anthropic)
- `codex` CLI (OpenAI, signed in via ChatGPT Plus)
- `agy` CLI (Antigravity, replaces gemini-cli): `curl -fsSL https://antigravity.google/cli/install.sh | bash`
- `ollama` (local)
- `gh` (optional, for PR creation in `auto`)
