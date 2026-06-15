package main

const defaultRoutingTOML = `# Styx routing rules.  Edit freely; first match wins.
# Use 'styx route --explain <verb> "..."' to see why a route was chosen.

[budget]
claude.cap_pct           = 80
claude.messages_per_5h   = 45
claude.messages_per_week = 225
codex.cap_pct            = 80
codex.messages_per_5h    = 50
codex.messages_per_week  = 250
agy.cap_pct              = 80
agy.messages_per_5h      = 100
agy.messages_per_week    = 500
ollama.cap_pct           = 0    # local, unlimited

# ── research ──
[[rule]]
verb = "research"
use  = "agy:default"
fallback = ["ollama:qwen2.5-coder:14b"]

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

# ── review (parallel) ──
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
use  = "agy:default"
fallback = ["claude:sonnet-4-6"]

[[rule]]
verb = "explain"
use  = "ollama:qwen2.5-coder:14b"

[[rule]]
verb = "summarize"
use  = "agy:default"
fallback = ["claude:sonnet-4-6", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "critique"
use  = "codex:gpt-5"
fallback = ["claude:sonnet-4-6", "ollama:qwen2.5-coder:14b"]

# ── REPL brain ──
[brain]
model                 = "llama3.2:3b"
embed_model           = "nomic-embed-text"
confidence_threshold  = 0.5
context_threshold_pct = 70
fable_weekly_cap      = 80   # vestigial while fable maps to opus (see [tiers] note); kept for easy restore

# Tier -> claude CLI model alias. The brain emits tiers; the REPL maps them here.
# NOTE (2026-06-12): Claude Fable 5 and Mythos 5 are suspended worldwide under a
# US export-control directive, so the "fable" tier maps to opus until access is
# restored. Opus 4.8 is the most capable model currently callable. Flip fable
# back to "fable" if/when Anthropic restores it.
[tiers]
fable  = "opus"
opus   = "opus"
sonnet = "sonnet"
haiku  = "haiku"
`
