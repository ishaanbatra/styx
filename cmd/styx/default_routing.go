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
use  = "codex"
effort = "high"
fallback = ["ollama:qwen2.5-coder:14b"]

# ── plan ──
[[rule]]
verb = "plan"
signals = ["complex"]
use  = "claude:opus"
fallback = ["claude:sonnet", "codex"]

[[rule]]
verb = "plan"
use  = "claude:sonnet"
fallback = ["codex", "ollama:qwen2.5-coder:14b"]

# ── implement (autonomous code application from a plan) ──
# A detailed plan already exists by this point, so the work is well-scoped:
# codex is the primary implementer ("faster to a first diff"). Ambiguous /
# multi-file architectural work (the "complex" signal) stays on claude, which
# reasons more before acting. claude is always the fallback.
[[rule]]
verb = "implement"
signals = ["complex"]
use  = "claude:sonnet"
fallback = ["codex", "claude:opus"]

[[rule]]
verb = "implement"
use  = "codex"
fallback = ["claude:sonnet"]

# ── build (interactive) ──
[[rule]]
verb = "build"
use  = "claude:interactive"
fallback = ["codex:interactive"]

# ── review (parallel) ──
[[rule]]
verb = "review"
parallel = ["claude:sonnet", "codex"]
synthesize_with = "claude:sonnet"

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
use  = "claude:sonnet"

[[rule]]
verb = "think"
use  = "ollama:qwen2.5-coder:14b"

# ── explain / summarize / critique ──
[[rule]]
verb = "explain"
signals = ["large_context"]
use  = "agy:default"
fallback = ["claude:sonnet"]

[[rule]]
verb = "explain"
use  = "ollama:qwen2.5-coder:14b"

[[rule]]
verb = "summarize"
use  = "agy:default"
fallback = ["claude:sonnet", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "critique"
use  = "codex"
fallback = ["claude:sonnet", "ollama:qwen2.5-coder:14b"]

# ── model discovery ──
[models]
refresh_interval_hours = 24

# ── REPL brain ──
[brain]
model                 = "qwen2.5-coder:7b"
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
