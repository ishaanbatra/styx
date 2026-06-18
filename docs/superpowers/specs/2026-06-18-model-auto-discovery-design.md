# Styx Model Auto-Discovery — Design

**Date:** 2026-06-18
**Status:** Approved (pending final user review)

## Problem

Styx hard-codes concrete model ids in two places — the routing table
(`routing.toml` / the seeded `cmd/styx/default_routing.go`) and the brain
capability cards (`internal/brain/cards.go`). The provider CLIs (claude, codex,
agy) ship new models continuously and retire old ids. When a pinned id is
retired, the routed call fails outright.

This was caught by dogfooding: `styx research` routed `research.critic` to
`codex:gpt-5`, but the user's Codex CLI is authenticated against a ChatGPT
account that rejects that id:

```
ERROR: invalid_request_error: The 'gpt-5' model is not supported when using
Codex with a ChatGPT account.
```

The critic failed every round and aborted the whole research run — even though
the agy drafter had produced a usable report. Patching `gpt-5` → `gpt-5.5` only
resets the clock until the next codex release. The durable fix is for styx to
discover the current valid model per channel instead of pinning versions by
hand.

A quick-patch (bump `codex:gpt-5` → `codex:gpt-5.5` in the live `routing.toml`
and seeded `default_routing.go`) has already landed to unblock today's usage.
This spec covers the durable feature.

## Goal & non-goals

**Goal (MVP): never break on stale model ids.** Styx self-heals: it
proactively discovers the model each channel currently accepts and rewrites the
routing table to match, before tasks run.

**Decisions locked during brainstorming:**

- **Timing — proactive only.** Discovery runs *ahead* of tasks, not inline
  mid-task. If a model still breaks between refreshes the run fails and the next
  refresh repairs it; there is no inline retry/recovery path.
- **Cadence — on `styx doctor` + a staleness check in `loadApp()` (~24h).**
  No daemon; consistent with styx's "state is files on disk, no daemons." The
  check lives in `loadApp()` (not just REPL session start) so it covers every
  entry point — verb dispatch (`styx research`, `styx auto`), one-shot brain
  turns, and the REPL alike. The original failure came through the one-shot
  brain path, so a REPL-only hook would have missed it.
- **Config writes — auto-rewrite + record correction.** The refresh rewrites
  the routing table in place and records a routing-correction memory.

**Non-goals (explicitly deferred):**

- Routing by codex *reasoning effort* (`high`/`medium`) — the "best model per
  tier" ambition. Styx keeps using codex's own configured effort default.
- Inline mid-task model recovery.
- Refreshing the brain capability cards in `cards.go`. They name models at the
  *tier* level (`opus`/`sonnet`/`haiku`, `qwen2.5-coder:*`), not version pins,
  so they do not rot the same way. Revisit later.

## Key insight: the four channels name models differently

Discovery is feasible for all four channels, but via four different native
mechanisms. The staleness pain is concentrated in version-pinned ids (codex,
and the pinned claude versions); alias-based and enumerable channels barely rot.

| Channel | Enumerates? | Stale risk | Native mechanism | `Current` |
|---------|-------------|------------|------------------|-----------|
| codex   | no list cmd | **high**   | read `~/.codex/config.toml` → `model` | that value (e.g. `gpt-5.5`) |
| agy     | `agy models` ("List available models") | low | parse the list | configured `default`, validated |
| ollama  | `GET /api/tags` (doctor already parses) | low | enumerate installed | n/a — validate pinned ids exist |
| claude  | none | none | stable aliases (`opus`/`sonnet`/`haiku`/`fable`) | the alias |

The design stance for the two pinned channels is **stop pinning**: codex defers
to its own config (styx broke precisely by *overriding* it with `--model`);
claude defers to aliases (the `[tiers]` block already maps tiers to aliases, but
the rules pin `claude:opus-4-7` / `claude:sonnet-4-6`). agy and ollama get
genuine enumeration because they offer it.

## Architecture

A new focused package **`internal/modelsync`** owns discovery, the staleness
cache, and the routing rewrite. It does **not** extend the `channel.Channel`
interface (that abstraction is about sending requests, not introspection).
Instead it defines its own `Discoverer` abstraction and a registry keyed by
channel name. `cmd/styx/doctor.go` and the REPL session-start path call into it.

### Components & data flow

```
doctor (always) / loadApp when cache stale
        │
        ▼
 modelsync.Refresh(ctx, cfg)
        │  for each registered Discoverer (best-effort, per-channel timeout):
        ├─► codexDiscoverer   reads ~/.codex/config.toml
        ├─► agyDiscoverer     runs `agy models`
        ├─► ollamaDiscoverer  GET /api/tags
        └─► claudeDiscoverer  returns stable aliases
        │
        ▼
 diff Results against routing.toml tokens
        │
        ├─► surgical rewrite of stale ids (atomic tmp+rename, comments preserved)
        ├─► record routing-correction memory (global scope, provenance)
        └─► write models.json cache (Results + refreshed_at)
```

### Discovery interface

```go
// Discoverer reports the model ids a channel currently accepts.
type Discoverer interface {
    Channel() string                              // "codex", "claude", ...
    Discover(ctx context.Context) (Result, error)
}

// Result is one channel's discovered model state.
type Result struct {
    Current   string   // id styx should prefer now (e.g. "gpt-5.5"); "" if alias-only
    Available []string // all valid ids when enumerable (agy, ollama); else nil
    Source    string   // "codex-config" | "agy-models" | "ollama-tags" | "claude-alias"
}
```

Each discoverer is tiny and independently testable. Per-channel behavior:

- **codex** — read `model` (and note `model_reasoning_effort`, unused for
  routing in MVP) from `~/.codex/config.toml`. `Current` = that model.
  `Source = "codex-config"`. Missing file → error (channel skipped, warned).
- **agy** — run `agy models`, parse ids into `Available`; `Current` = the
  configured `default` if present in the list, else the first listed.
  `Source = "agy-models"`.
- **ollama** — `GET /api/tags`, parse installed model names into `Available`
  (reuse doctor's existing tags parsing). No single `Current`; used only to
  validate pinned ids. `Source = "ollama-tags"`.
- **claude** — pure: return `Available = [opus, sonnet, haiku, fable]`
  (the stable aliases). `Source = "claude-alias"`.

## Staleness cache & trigger

`~/.config/styx/models.json` (atomic tmp+rename via `paths` helpers):

```json
{
  "refreshed_at": "2026-06-18T12:00:00Z",
  "channels": {
    "codex":  {"current": "gpt-5.5", "source": "codex-config"},
    "agy":    {"available": ["default", "..."], "source": "agy-models"},
    "ollama": {"available": ["qwen2.5-coder:7b", "qwen2.5-coder:14b"], "source": "ollama-tags"},
    "claude": {"available": ["opus","sonnet","haiku","fable"], "source": "claude-alias"}
  }
}
```

- **`styx doctor`** always runs a full `Refresh`.
- **`loadApp()`** reads `refreshed_at`; if older than the threshold it runs
  `Refresh` before wiring channels, so verb dispatch, one-shot turns, and the
  REPL all get fresh routing. Best-effort: a refresh failure here warns and
  proceeds with existing routing.
- Threshold: new `[models]` block in `routing.toml`, `refresh_interval_hours = 24`
  (configurable; seeded into `default_routing.go`).
- Refresh is **synchronous, best-effort, with a short per-channel timeout**.
  codex/claude are instant, ollama is a fast local GET, `agy models` ~1s. A
  discoverer that exceeds its timeout keeps its cached value and is skipped.

## Routing rewrite & record

- **Diff:** scan `routing.toml` for `channel:model` tokens in `use`,
  `fallback`, `parallel`, and `synthesize_with` values. For each, compare the
  model against the channel's discovery `Result`.
- **Surgical text rewrite (not TOML re-marshal):** replace only the stale id
  token in the raw file text so hand-written comments and layout survive.
  Atomic tmp+rename. Rules:
  - **codex:** any `codex:<x>` where `<x> != Current` → `codex:<Current>`.
  - **claude:** de-pin a pinned version to its alias by tier-prefix match
    (`claude:opus-4-7` → `claude:opus`, `claude:sonnet-4-6` → `claude:sonnet`).
    One-time; afterward claude tokens are aliases and never rewrite again.
    `claude:interactive` is left untouched.
  - **agy / ollama:** if the pinned id is absent from `Available` and a single
    unambiguous replacement exists, swap it; otherwise **leave it and warn**
    (never invent a model).
- **Record:** one memory item per applied change, global scope, high
  confidence, via the provenance system (Task 19.2). Example text:
  `"routing: codex model gpt-5 → gpt-5.5 (source: codex-config), 2026-06-18"`.
- **Transparency:** print each change to stderr via `logStatus` (respect
  `--quiet`).
- The `models.json` cache is rewritten on every refresh regardless of whether a
  routing change was applied, so the staleness timer resets.

## Error handling

Every discoverer is best-effort and isolated. A missing `~/.codex/config.toml`,
a failed `agy models`, or ollama being unreachable warns for *that channel* and
skips it — it never aborts the refresh and never bricks session start. The
rewrite proceeds only for channels that discovered cleanly. A routing-write
failure surfaces loudly in `doctor` but is swallowed-to-warning in `loadApp()`
(a refresh hiccup must not block a verb, a one-shot turn, or the REPL). All subprocess/HTTP calls run under a
context with timeout, per styx convention.

## Testing

Table-driven, fakes over mocks, matching styx conventions:

- **Per discoverer:** temp-dir fake `~/.codex/config.toml` (inject the codex
  home path); scripted `agy models` output via a `testdata` fake binary (like
  `testdata/fakeagent`); `httptest` for ollama `/api/tags`; claude alias path is
  pure.
- **Rewrite:** fixture `routing.toml` + a discovery `Result` → assert new ids
  present, comments preserved, untargeted lines byte-identical, and the write is
  atomic. Cover codex bump, claude de-pin, agy/ollama ambiguous-leave-and-warn.
- **Staleness:** inject a clock (no inline `time.Now()`); assert refresh fires
  when the cache is stale and is skipped when fresh.
- **Record:** assert the routing-correction memory is written with correct
  provenance (global scope, high confidence).
- **doctor integration:** extend `cmd/styx/doctor_test.go` to assert a full
  refresh runs and reports per-channel results.

## Drift-contract impact

- `internal/modelsync/` is new — gets a package doc comment and a mention in
  `docs/ARCHITECTURE.md` (owner of `internal/**`), with `last_verified` bumped.
- New `[models]` block in `default_routing.go` and its `routing.toml` upgrade
  path.
- No verbs added/removed → `README.md` verb table unchanged.

## Rollout

1. Quick-patch (done): `codex:gpt-5` → `codex:gpt-5.5` in live `routing.toml`
   and seeded `default_routing.go`.
2. `internal/modelsync` package: `Discoverer`, four discoverers, cache.
3. Routing rewrite + record-correction.
4. Wire into `doctor` (always) and session start (staleness).
5. De-pin claude in the seeded defaults as part of the first real refresh.
