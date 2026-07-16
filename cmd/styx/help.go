package main

import "fmt"

const helpText = `styx — multi-model dev orchestration

USAGE
  styx [--quiet|--verbose] [--host claude|codex] <verb> [args]
  styx [--quiet|--verbose] [--host claude|codex]
  styx [--quiet|--verbose] [--host claude|codex] "<anything>"

GLOBAL FLAGS
  --quiet      Suppress progress narration (only final results print)
  --verbose    Show extra detail (prompt sizes, model names) during long ops
  --host       Override [conductor] host for this launch (claude or codex)

VERBS
  styx [repo...]            Launch the configured conductor host with the styx MCP
                             toolbelt, bound to named repos if given (first = focus)
  launch [repo...]          Same as above, explicit verb form
  resume [sessionID]        Resume a conductor host session (most recent for
                             the cwd if no ID),
                             rewiring the styx toolbelt + guidance
  repl [repo...]            Open the classic v0.2 REPL, kept until the
                             conductor reaches parity
  styx "<anything>"         One brain-routed turn, then exit
  research <query>          Gemini draft + Codex critique -> brief
  debug <bug>               ultraFerdDebug: agy sweep + independent Codex/Claude
                             review -> diagnosis report (no code edits)
  debug --log <file...> [-- <failure description>]
                             Cluster log/test failures by root cause, trace them
                             to code, then run one Codex review (no code edits)
  debug --review-only <brief> [bug]
                             Re-run only the cheap reviews for an existing brief
  deep-research <query>     Open Gemini + ChatGPT in browser; synthesis template
  plan <description>        Draft an implementation plan using the latest brief
  build [target]            Interactive Claude/Codex session in the project dir
  review                    Parallel multi-channel review of git diff main...HEAD
  grunt <prompt>            Quick Ollama pass-through (code gen)
  think <prompt>            Ollama reasoning mode, no code (prefix with "deep:" for Claude)
  explain <file...>         Explain code in given files
  summarize <file...>       Summarize a set of files
  critique <text|file>      Devil's-advocate critique (Codex)
  check                     Dashboard: git status, ollama, latest briefs/plans
  budget                    Per-channel usage summary
  learn [--scorecard|--dry-run|--list|--forget <id>]
                            Digest dispatch outcomes + retrospectives into
                            learned preferences (local ollama; reversible)
  doctor [--fix]            Preflight CLIs, capability cards, ollama models
  route --explain <verb> "..." Show routing decision for a hypothetical request
  project ls|add|rm|rename  Manage project registry
  migrate-secrets           One-time: move env-var secrets to macOS Keychain
  intel <p> [--force]       Build/refresh the codebase intel index
  intel ls                  List cached indexes + freshness state
  graphify <p> [--force]    Build/refresh the graphify knowledge graph (needs graphify CLI)
  graphify ls               List graph freshness per registered project
  context show              Print rendered .claude/context.md for current project
  auto <goal>               Full autonomous pipeline (research -> ship)
  auto --resume <run-id>    Resume an interrupted pipeline
  execute <plan-file>       Non-interactive code execution from a plan markdown
  runs ls                   List pipeline runs for current project
  runs show <run-id>        Show JSON state of a specific run
  runs unlock               Force-release a stale pipeline lock (after a crash)
  update                    Update styx to the latest verified release
  help                      Show this menu
  version                   Print the styx version

REPL
  Slash commands: /status /budget /threads /why /audit /quit

CONFIG
  ~/.config/styx/routing.toml      routes (you edit)
  ~/.config/styx/projects.toml     registry (auto-managed)
  ~/.config/styx/state/usage.db    usage log

SECRETS
  Stored in macOS Keychain under service "styx". Migrate from env vars with:
    styx migrate-secrets
`

func printHelp() {
	fmt.Print(helpText)
}
