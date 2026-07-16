# Styx

[![latest release](https://img.shields.io/github/v/release/ishaanbatra/styx)](https://github.com/ishaanbatra/styx/releases/latest)

Personal multi-model dev orchestration CLI. Routes work between Claude, Codex,
Antigravity (agy / Gemini), and Ollama via a hand-curated rules table.

## Prerequisites

- **Go 1.25.12+** — to build (no cgo; sqlite is pure Go).
- **`claude` CLI** ([Claude Code](https://claude.com/claude-code)) with an
  active subscription — the only hard requirement at runtime; the default
  `styx` verb launches it as the conductor.
- **Optional channels** — `codex` (OpenAI), `agy` (Antigravity), and `ollama`
  add routing targets; missing ones degrade gracefully down the routing
  table's fallback chains. `gh` enables PR creation in `auto`; `graphify`
  enables knowledge graphs. Run `styx doctor` to see what's wired up.

styx rides your existing CLI subscriptions — there are no API keys to
configure. Platform support is tiered: macOS is the primary target (secrets
in the Keychain, ollama auto-launched); Windows is supported natively
(secrets in the Windows Credential Manager; start ollama yourself); Linux
works minus a secret store (no `migrate-secrets`) and ollama auto-launch.

## Install

macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/ishaanbatra/styx/main/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/ishaanbatra/styx/main/install.ps1 | iex
```

With Go:

```sh
go install github.com/ishaanbatra/styx/cmd/styx@latest
```

Tagged releases are also available on the
[GitHub Releases page](https://github.com/ishaanbatra/styx/releases). Run
`styx update` to stay current when you installed a release directly.

### From source

Contributors can keep the local build-and-install workflow:

```sh
git clone https://github.com/ishaanbatra/styx.git
cd styx
./install.sh --from-source
```

Then migrate any plaintext secrets out of your shell rc:

    styx migrate-secrets

If you're upgrading from v0.1 with `gemini:*` references in routing.toml, styx
auto-rewrites them to `agy:default` on first v0.2 startup (with a backup).

## First run

On its first interactive terminal run, styx detects `claude`, `codex`, and
`agy` on `PATH` plus a reachable local Ollama server, then opens a short wizard.
Choose the subscriptions you have; styx writes a plain, hand-editable
`~/.config/styx/routing.toml` whose fallback chains avoid unavailable channels.
For a selected subscription whose CLI is missing, the wizard can run that
tool's official installer only after an explicit, default-off confirmation.
It never uses `sudo`.

The wizard runs only when stdin, stdout, and stderr are all terminals. CI,
pipes, redirected commands, and `STYX_NO_WIZARD=1` keep the previous behavior:
the standard routing file is seeded silently and unchanged. After setup, run
`styx doctor` to verify the selected tools and local models.

## Build manually

    make build       # produces ./bin/styx
    make test        # runs all tests
    make e2e         # hermetic JSON-RPC regression net against `styx mcp` (fake CLIs, no quota)
    make install     # builds and installs the local checkout to ~/bin/styx

## Verbs

### Global flags

| Flag | What it does |
|---|---|
| `--project <alias>` | Run the verb against a registered project, from anywhere (exact name or unique prefix) |
| `--dir <path>` | Run the verb against the repo at `<path>`, from anywhere |
| `--host claude\|codex` | Override `[conductor] host` for this conductor launch |

Without a project or directory flag, styx uses the current directory's repo.
An explicit target that can't be resolved is a clear error, never a silent
fallback.

### Conversational

| Verb | What it does |
|---|---|
| `styx` | Launch the configured Claude Code or Codex conductor with the styx MCP toolbelt — from any directory, git repo or not (`repl` opens the classic v0.2 REPL) |
| `styx <repo...>` | Launch the conductor bound to one or more named repos (first is focus) |
| `styx launch [repo...]` | Same as the two rows above, explicit verb form |
| `styx resume [sessionID]` | Relaunch the configured conductor and rewire the styx toolbelt/guidance: Claude uses `--continue` / `--resume <id>`; Codex uses `resume --last` / `resume <id>` |
| `styx repl [repo...]` | Open the classic v0.2 REPL, kept until the conductor reaches parity |
| `styx "<anything>"` | Run one brain-routed turn, then exit |

`styx`/`styx launch` starts `[conductor] host` (default `claude`, overridable
for one launch with `--host`). Claude Code receives a styx-owned MCP config and
appended system prompt; Codex receives equivalent per-invocation
`mcp_servers.styx.*` and TOML-encoded `developer_instructions` overrides, so
neither path mutates the host's user config. Both run in the project directory
with stdio passthrough. Extra repos are added directly with `--add-dir` and
noted in guidance so the brain also passes them as the `dispatch` tool's
`extra_roots`. Claude Code enforces non-`off` `route_gate` modes with hooks;
Codex currently degrades to guidance-only routing and narrates that limitation.

Within a multi-repo classic-REPL session, `/repos` lists all bound repos
(focus-marked), `/focus <name>` switches to a different bound repo (binding it
lazily if not yet open), and naming a repo mid-conversation binds it without
restarting the session.

### Deep research + planning

| Verb | What it does |
|---|---|
| `research <q>` | Convergence loop: agy on pinned Gemini 3.1 Pro (High) drafts, codex critiques, agy revises, until critic returns no BLOCKING/IMPORTANT (max 6 rounds) |
| `research --deep <q>` | Same loop, then chase every cited URL and summarize into a Sources Appendix |
| `plan <desc>` | Auto-refresh intel index, write `.claude/context.md`, draft a detailed plan via Claude |
| `build [target]` | Auto-refresh intel, write `.claude/context.md`, launch Claude interactively |
| `review` | Parallel Claude + Codex diff review, synthesized |

### Debugging

| Verb | What it does |
|---|---|
| `debug <bug>` | **ultraFerdDebug**: agy on pinned Gemini 3.1 Pro (High) sweeps the repository into a cited debug brief, then Codex and Claude independently review it and styx writes a deterministic diagnosis report. Diagnosis only; no code edits |
| `debug --log <file...> [-- <failure description>]` | Failure-triage entry mode: agy reads one or more log/test-output files by path, clusters failures by root cause, and traces each cluster to repository code; one Codex review checks the clusters and traces. Log contents are never embedded in the prompt |
| `debug --test <name> --file <hint> <bug>` | Add a failing-test name and repeatable starting-file hints to the normal diagnosis sweep |
| `debug --review-only <brief> [bug]` | Skip the expensive sweep and run only the two short reviews plus verdict against an existing brief |
| `dead-code [path]` | Agy on pinned Gemini 3.1 Pro (High) sweeps the repository for unused files, functions, and imports. Styx whole-word-checks every valid reported symbol outside its definition site, marks it CONFIRMED/REFUTED, then runs one Codex spot-check over up to five confirmed findings. Read-only; report saved under `styx/dead-code/` |
| `map-impact <symbol\|file\|diff-spec>` | Agy on pinned Gemini 3.1 Pro (High) traces direct dependents and transitive change impact across the repository from a symbol, file, or git diff/ref such as `HEAD~1`. Findings are structured dependency edges; one Codex turn spot-checks up to five claimed edges against the cited code. Read-only; report saved under `styx/map-impact/` |
| `cross-repo <root2> [root3...] [-- <question>]` | Agy traces machine-readable producer/consumer links across exactly the named git repository roots, then one Codex turn spot-checks up to five links. Every mounted tree is guarded; any mutation refuses success. Sensitive credential directories are never mounted. Report saved atomically under the primary repo's `styx/cross-repo/` |

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
| `graphify <project> [--force]` | Build/refresh the graphify knowledge graph for a repo (wraps the external `graphify` CLI; errors if not installed; the conductor launch path skips instead) |
| `graphify ls` | List knowledge-graph freshness per registered project |
| `context show` | Print rendered `.claude/context.md` for the current project |
| `runs ls` | List pipeline runs for the current project |
| `runs show <run-id>` | Show JSON state of a specific run |
| `styx watch [repo]` | Live dispatch board — per-agent heartbeat + stall flags, refreshed from a running session or `styx mcp`, in a second terminal. Defaults to the current-directory project; an explicit `repo` resolves by registered name/prefix/path the same way `styx repl [repo]` does, so `styx watch otherRepo` follows a `styx repl otherRepo` session launched from anywhere |

Knowledge graphs write artifacts into each repo's working tree (`graphify-out/`); recommend adding `graphify-out/` to each repository's `.gitignore` or your global git excludes.

### One-shots + admin

| Verb | What it does |
|---|---|
| `grunt <prompt>` | Local Ollama pass-through |
| `think <prompt>` | Local Ollama reasoning mode (`deep:` prefix -> Claude) |
| `explain <files...>` | Explain code files; large contexts route to pinned Gemini 3.1 Pro (High) on agy |
| `summarize <files...>` | Summarize a set of files with pinned Gemini 3.1 Pro (High) on agy |
| `critique <text>` | Devil's-advocate critique (Codex) |
| `check` | Dashboard: git status, latest briefs/plans |
| `budget` | Per-channel usage summary |
| `learn [--scorecard\|--dry-run\|--list\|--forget <id>]` | Digest dispatch outcomes + session retrospectives into learned routing/user preferences — plain-text memories with provenance, injected into conductor guidance; `--list` inspects, `--forget` reverses |
| `doctor [--fix]` | Preflight CLIs, capability-card drift, callable Claude tiers, and required Ollama models |
| `route --explain <verb> "..."` | Why did styx pick that channel? |
| `project ls/add/rm/rename/scan` | Manage project registry |
| `project scan [root] [--depth N]` | Walk down from `root` (default `~`), find git repos, bulk-register them (prunes node_modules/vendor; depth default 4) |
| `mcp` | Run styx as an MCP stdio server (JSON-RPC 2.0) exposing fourteen tools to OpenClaw, Claude Code, and any MCP host (see [`docs/openclaw-integration.md`](docs/openclaw-integration.md)): `route` — pick a channel for a task (budget-aware, capability-floor-aware, with fallback chain); `budget_status` — per-channel usage/limits/cooldowns; `record_usage` — log usage a consumer ran outside styx; `channel_health` — circuit-breaker state, recent failures, error-kind buckets, cooldown; `get_intel` — read the per-project codebase intel index (or one section), with staleness; `refresh_intel` — rebuild that index; `recall` — semantic top-k recall over project + global long-term memory; plus the conductor dispatch surface: `dispatch` — send work to a persistent agent thread (claude/codex/agy) or a one-shot local ollama task, awaited by default (or, with `background: true`, as a task you `collect` later); `dispatch_parallel` — fan out an array of dispatches at once, awaited together, per-task results in input order; `thread_status` — list this project's persistent agent threads with turn counts and context usage, plus live/unclaimed background tasks; `collect` — fetch results by `task_id` or sweep everything finished, or set `wait: true` (optionally `timeout_s`) to block with live heartbeats until one task or all currently-outstanding tasks finish; `memory_save` — persist a durable fact, decision, todo, or routing preference to styx memory; `pipeline_run` — run a styx pipeline (research/review/intel/auto/debug), with a confirm-token handshake only for `auto`'s ship step; `rate_dispatch` — rate a recent dispatch outcome good/bad to feed styx's learning loop |
| `hook <event>` | Internal plumbing — the route-gate hook the launcher installs into conductor sessions (Claude Code invokes it, not you); denies inline WebSearch/WebFetch/Task/external-curl + MCP web tools and redirects to dispatch/pipeline_run, per `[conductor] route_gate` |
| `migrate-secrets` | Move env-var secrets to the platform secret store (macOS Keychain / Windows Credential Manager) |
| `update` | Replace styx with the latest GitHub release after SHA-256 verification; Scoop/WinGet installs stay package-manager-owned |
| `upgrade` | Re-run routing migrations manually (including seeded conductor host/task settings) |
| `version` / `styx --version` | Print the styx version and exit |

Dispatches wait by default: while an agent works, live progress (tool-by-tool
activity plus the local-ollama watcher's narration) streams into the MCP
host's UI as progress notifications — no tokens, no polling — and the
findings return inline the moment the work finishes. `dispatch_parallel`
does the same for several agents at once. `background: true` detaches
instead; `styx watch` in a second terminal is live either way. Interrupting
an awaited call (Esc) detaches it — the agents keep working. When background
work is needed, call `collect` with `wait: true` instead of polling; it blocks,
streams the same heartbeats, and returns the result inline. A `timeout_s`
expiry or Esc detaches only the wait, leaving the tasks running and
collectible. For very long dispatches, raise your MCP client's tool-call
timeout (Claude Code: `MCP_TOOL_TIMEOUT`).

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
- `<project>/styx/debug/` — recoverable ultraFerdDebug briefs and final reports (overridable with the project's `debug_dir`)
- `<project>/styx/dead-code/` — atomic dead-code sweep, deterministic verification, and Codex spot-check reports
- `<project>/styx/map-impact/` — atomic impact maps with canonical dependency-edge JSON and a Codex spot-check
- `<project>/styx/cross-repo/` — atomic multi-root link reports with canonical findings and all-roots guard evidence
- Secrets live in the platform secret store (macOS Keychain service `styx` /
  Windows Credential Manager target prefix `styx/`).

The conductor host is configured in the routing file and defaults to Claude
Code. Existing configs receive the default key through the normal idempotent
routing upgrade without replacing a host already chosen by the user:

```toml
[conductor]
host = "claude" # claude | codex
```

The seeded routing table pins agy rules to `Gemini 3.1 Pro (High)` because the
subscription CLI otherwise reuses the user's last interactive model choice.
It uses `debug.sweep` for agy with `claude:sonnet` fallback,
`debug.review.codex` for the high-effort Codex pass, and
`debug.review.claude` for the Claude Sonnet pass. `styx upgrade` injects
missing debug rules and upgrades `agy:default` targets to the pin without
overwriting explicitly selected custom models. Log mode still routes its sweep
through `debug.sweep`, but intentionally stops after the single Codex review;
the full Codex+Claude review remains exclusive to the normal diagnosis mode.
The `dead-code` rule uses the same agy pin with `claude:sonnet` then `codex`
fallbacks. Existing routing files receive it idempotently during startup or
`styx upgrade`; an existing customized `dead-code` target is preserved.
The `map-impact` rule uses the same pin and fallback chain; its missing-rule
migration is likewise idempotent and preserves existing customized routing.
The `cross-repo` rule follows the same pattern; existing routing files receive
it idempotently without replacing a customized `cross-repo` target.

## Deps

- `claude` CLI (Anthropic) — required for Claude channels or conductor sessions
- `codex` CLI (OpenAI, signed in via ChatGPT Plus) — required for Codex channels or conductor sessions
- `agy` CLI (Antigravity, replaces gemini-cli): `curl -fsSL https://antigravity.google/cli/install.sh | bash`
- `ollama` (local)
- `gh` (for PR creation in `auto`)
