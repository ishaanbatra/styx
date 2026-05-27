# Styx

Personal multi-model dev orchestration CLI. Routes work between Claude, Codex,
Gemini-CLI, and Ollama based on a hand-curated rules table.

See `docs/superpowers/specs/2026-05-26-styx-v2-design.md` for design.

## Build

    make build       # produces ./bin/styx
    make test        # runs all tests
    make install     # installs to ~/bin/styx (backs up any existing one)
