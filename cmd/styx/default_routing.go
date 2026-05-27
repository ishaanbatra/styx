package main

const defaultRoutingTOML = `# Styx routing rules.  Edit freely; first match wins.
# Use 'styx route --explain <verb> "..."' to see why a route was chosen.

[budget]
claude.cap_pct       = 80
codex.cap_pct        = 80
agy.cap_pct          = 80
ollama.cap_pct       = 0    # local, unlimited

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
`
