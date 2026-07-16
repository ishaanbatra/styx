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
use  = "agy:Gemini 3.1 Pro (High)"
fallback = ["ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "research.critic"
use  = "codex"
# effort: low|medium|high|xhigh|max (claude); codex maps to model_reasoning_effort
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

# ── debug (ultraFerdDebug: agy sweep, codex+claude review) ──
[[rule]]
verb = "debug.sweep"
use  = "agy:Gemini 3.1 Pro (High)"
fallback = ["claude:sonnet"]

[[rule]]
verb = "debug.review.codex"
use  = "codex"
effort = "high"

[[rule]]
verb = "debug.review.claude"
use  = "claude:sonnet"

# ── dead-code (agy sweep, deterministic grep, codex spot-check) ──
[[rule]]
verb = "dead-code"
use  = "agy:Gemini 3.1 Pro (High)"
fallback = ["claude:sonnet", "codex"]

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
use  = "agy:Gemini 3.1 Pro (High)"
fallback = ["claude:sonnet"]

[[rule]]
verb = "explain"
use  = "ollama:qwen2.5-coder:14b"

[[rule]]
verb = "summarize"
use  = "agy:Gemini 3.1 Pro (High)"
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
fable_weekly_cap      = 80   # caps fable messages before the tier degrades to opus

# Tier -> claude CLI model alias. The brain emits tiers; the REPL maps them here.
# NOTE: the "fable" tier mapped to opus during the 2026-06-12 suspension; Fable 5
# is callable again since mid-2026 and now maps to "fable" (safety classifiers may
# transparently serve opus for flagged requests).
[tiers]
fable  = "fable"
opus   = "opus"
sonnet = "sonnet"
haiku  = "haiku"

# ── Conductor (frontier-brain launcher + MCP toolbelt) ──
[conductor]
# interactive host CLI: claude | codex
host = "claude"
# ship confirmation for dispatch(risk=ship) / pipeline_run auto:
# handshake (token relay, default) | tty (prompt on /dev/tty) | off
ship_gate = "handshake"
# host-hook enforcement of dispatch-over-inline routing in conductor sessions:
# block (deny inline WebSearch/WebFetch/Task/external-curl + MCP web tools,
# redirect to dispatch/pipeline_run; audit the fuzzy tail) | audit (record
# inline use, never block) | off (no hooks). Default block.
route_gate = "block"
# max concurrent background dispatches; over-cap tasks queue (collect shows position)
max_background_tasks = 4

[watch]
stall_threshold_seconds = 90
interval_seconds = 15
ollama_enabled = true
`
