# Styx

Personal multi-model dev orchestration CLI. Routes work between Claude, Codex,
Gemini-CLI, and Ollama based on a hand-curated rules table.

See `docs/superpowers/specs/2026-05-26-styx-v2-design.md` for design.

## Install (one shot)

    ./install.sh        # builds + drops binary at ~/bin/styx (backs up any existing one)

Then migrate any plaintext secrets out of your shell rc:

    styx migrate-secrets

## Build manually

    make build       # produces ./bin/styx
    make test        # runs all tests
    make install     # same as install.sh

## Verbs

| Verb           | Purpose                                                   |
|----------------|-----------------------------------------------------------|
| research <q>   | Gemini draft + Codex critique → brief in research_dir     |
| deep-research  | Open Gemini + ChatGPT in browser, write synthesis template |
| plan <desc>    | Use latest brief to draft a detailed implementation plan  |
| build [target] | Launch interactive Claude (or Codex on fallback) in repo  |
| review         | Parallel multi-channel review of `git diff main...HEAD`   |
| grunt <prompt> | Local Ollama pass-through                                 |
| think <prompt> | Local Ollama reasoning mode (`deep:` prefix → Claude)     |
| explain ...    | Explain code files                                        |
| summarize ...  | Summarize a set of files                                  |
| critique ...   | Devil's-advocate critique (Codex)                         |
| check          | Dashboard: git status, Ollama, latest briefs/plans        |
| budget         | Per-channel usage summary                                 |
| route --explain <verb> "..." | Why did styx pick that channel?             |
| project ls/add/rm/rename     | Manage project registry                      |
| migrate-secrets              | Move env-var secrets to macOS Keychain       |

## Configuration

- `~/.config/styx/routing.toml` — routing rules + budget caps (you edit this)
- `~/.config/styx/projects.toml` — registered projects (auto-managed)
- `~/.config/styx/state/usage.db` — append-only sqlite usage log
- Secrets live in macOS Keychain under service `styx`.
