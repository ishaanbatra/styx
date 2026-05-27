package main

import "fmt"

const helpText = `styx — multi-model dev orchestration

USAGE
  styx <verb> [args]

VERBS
  research <query>          Gemini draft + Codex critique -> brief
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
  route --explain <verb> "..." Show routing decision for a hypothetical request
  project ls|add|rm|rename  Manage project registry
  migrate-secrets           One-time: move env-var secrets to macOS Keychain
  intel <p> [--force]       Build/refresh the codebase intel index
  intel ls                  List cached indexes + freshness state
  help                      Show this menu

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
