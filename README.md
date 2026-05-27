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
    make install     # same as install.sh

## Verbs

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
| `auto <goal>` | Full pipeline: research -> intel -> plan -> execute -> test -> review -> ship |
| `auto --deep <goal>` | Pipeline with citation chasing in research stage |
| `auto --no-pr <goal>` | Stop at push (don't open PR) |
| `auto --no-push <goal>` | Stop at commit (don't push) |
| `auto --resume <run-id>` | Resume an interrupted pipeline |
| `execute <plan-file>` | Apply a plan via Claude non-interactively |

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
| `route --explain <verb> "..."` | Why did styx pick that channel? |
| `project ls/add/rm/rename` | Manage project registry |
| `migrate-secrets` | Move env-var secrets to macOS Keychain |
| `upgrade` | Re-run the v0.1->v0.2 routing rewrite manually |

## Configuration

- `~/.config/styx/routing.toml` — routing rules + budget caps (you edit this)
- `~/.config/styx/projects.toml` — registered projects (auto-managed)
- `~/.config/styx/state/usage.db` — append-only sqlite usage log
- `~/.config/styx/state/intel/<project>/index.json` — per-project codebase intel
- `<project>/.claude/context.md` — rendered intel (Claude Code auto-loads this)
- `<project>/.styx/runs/<run-id>/` — pipeline state per run
- Secrets live in macOS Keychain under service `styx`.

## Deps

- `claude` CLI (Anthropic)
- `codex` CLI (OpenAI, signed in via ChatGPT Plus)
- `agy` CLI (Antigravity, replaces gemini-cli): `curl -fsSL https://antigravity.google/cli/install.sh | bash`
- `ollama` (local)
- `gh` (optional, for PR creation in `auto`)
