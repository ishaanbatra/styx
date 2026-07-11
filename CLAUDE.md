# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

# Styx

Go CLI that orchestrates dev work across four AI channels — claude, codex, agy
(Antigravity), and ollama — via a hand-curated routing table with budget-aware
fallback. Verb-first today (`styx research`, `styx auto`, …); evolving into a
conversational REPL orchestrator (spec + plan in `docs/superpowers/`).

## Doc tree — navigate docs first, code second

Each doc declares `owns:` globs in its frontmatter and is the authority on
those files:

- `docs/ARCHITECTURE.md` — owns `cmd/styx/**`, `internal/**`, `testdata/**`,
  `eval/**`, `e2e/**`: all subsystems (channels, routing, budget, intel,
  pipelines, research, execute, progress) and the on-disk layout
- `README.md` — user-facing verb table, install, configuration paths
- `docs/superpowers/specs/` — design specs (newest: REPL orchestrator)
- `docs/superpowers/plans/` — task-by-task implementation plans for those specs

**Drift contract (the most important rule in this file):** before editing a
source file, load its owner doc. After editing code, update the owner doc **in
the same commit** and bump its `last_verified` date. Adding/removing/renaming
a verb also means updating README.md's verb table. Stale docs are worse than
no docs. A PostToolUse hook in `.claude/settings.json` reminds you after every
`.go` edit — do not dismiss it.

## Architecture

- **Verb dispatch** (`cmd/styx/dispatch.go`): two-tier verb switch; `loadApp()`
  wires routing + budget tracker + router + progress-decorated channels
- **Channels** (`internal/channel/` + claude/codex/agy/ollama adapters): the
  provider abstraction; subprocess CLIs or ollama HTTP, classified errors
- **Router** (`internal/router/` + `internal/signals/`): first-match rules
  from `~/.config/styx/routing.toml`, signal tagging, budget degradation down
  fallback chains
- **Budget** (`internal/budget/`): append-only sqlite usage log, 5h/weekly
  message windows, cooldowns, circuit-breaker query
- **Intel** (`internal/intel/`): per-project codebase index rendered to
  `<project>/.claude/context.md`
- **Graph** (`internal/graph/`): per-project graphify knowledge-graph
  freshness — wraps the external `graphify` CLI, artifacts in-repo at
  `graphify-out/`, HEAD-drift staleness, auto-built on conductor launch
- **Pipeline** (`internal/pipeline/` + `cmd/styx/auto.go`): resumable 7-stage
  auto flow (research → intel → plan → execute → test → review → ship)
- **Research** (`internal/research/` + `internal/brief/`): drafter/critic
  convergence loop, deep-mode URL chasing, briefs to disk
- **Execute** (`internal/execute/`): headless claude plan application + ship
  (commit/push/PR)
- **Progress** (`internal/progress/`): TTY-aware `[styx]` stage narration

Read `docs/ARCHITECTURE.md` for data flow and per-package detail before
touching any of these.

## Key tech decisions

- Channels are CLIs/HTTP, never SDKs — styx rides the user's existing
  subscriptions (claude/codex/agy CLIs, local ollama). Do not add API-key SDK
  clients.
- Routing is a transparent, user-edited rules table (`routing.toml`), not an
  LLM router. The coming REPL brain is additive; the table stays.
- SQLite via `modernc.org/sqlite` (pure Go, no cgo). Don't switch drivers.
- macOS-first: Keychain for secrets, `open -a Ollama` auto-launch.
- State is files on disk (TOML/JSON/SQLite under `~/.config/styx/`), atomic
  tmp+rename writes. No daemons.

## Common commands

```bash
make build                              # go build -> ./bin/styx
make test                               # go test ./...
make install                            # build + copy to ~/bin/styx (backs up old)
go test ./internal/router/ -v           # one package
go test ./internal/agent/ -run TestRunner -v   # one test
go vet ./... && gofmt -w .              # before every commit
go run ./cmd/styx route --explain plan "refactor the loader"  # debug routing
```

## Coding standards

- Table-driven tests with `t.Run`; fakes over mocks (httptest for ollama,
  scripted `testdata/fakeagent` for CLI agents, in-memory stubs for interfaces)
- Wrap errors with context: `fmt.Errorf("load registry: %w", err)`; channel
  errors as `channel.ClassifiedError` so the router can label them
- Status/narration to stderr via `logStatus`/`progress` (respect `--quiet`);
  results to stdout
- New packages get a doc comment explaining their role in the orchestration
- File writes are atomic (tmp + rename); follow `paths` helpers for locations

## What NOT to do

- Never call provider HTTP APIs directly for claude/codex/agy — always shell
  out to their CLIs (subscription auth lives there)
- Never swallow errors (`x, _ :=`) — surface them through progress stages or
  wrapped returns; silent fallback caused real bugs here
- Never write secrets to disk or env — Keychain only (`internal/config/secrets.go`)
- Never break `styx auto --resume`: any pipeline change must keep old
  `state.json` files loadable
- Never let a subprocess run unbounded — every exec gets a context with
  timeout or an interruptible signal context
- Don't edit `~/.config/styx/routing.toml` defaults without updating
  `cmd/styx/default_routing.go` (the seeded copy) and its upgrade path
