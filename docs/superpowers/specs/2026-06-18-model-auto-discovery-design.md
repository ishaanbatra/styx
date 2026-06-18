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

**Goal: stop pinning model versions; control reasoning effort explicitly.**
The original failure conflated two orthogonal dimensions. The design separates
them:

1. **Model version → defer to "latest", never pinned.** styx stops naming
   concrete model versions. claude routes by alias (`opus`/`sonnet` already mean
   "latest of that class"); codex sends no `--model` at all, so codex uses its
   own current default. Nothing version-pinned ⇒ nothing to go stale ⇒ styx is
   always on the latest by construction. (agy already defers; ollama unaffected.)
2. **Reasoning effort → user-controlled, per routing rule.** A new optional
   `effort` field on each rule lets the user pick effort independent of version
   (e.g. codex `high` instead of its `medium` default; claude `ultracode`
   instead of `high`). Effort is a **pass-through string**, never validated
   against a styx enum, so new effort levels work without a styx change — the
   same anti-staleness stance applied to the effort axis.

**Decisions locked during brainstorming + refinement:**

- **Defer-to-latest, not rewrite-to-current.** Because versions are no longer
  pinned, `styx doctor` keeps you current passively. doctor's active job is
  (a) **validate** that aliases resolve, and (b) run a **one-time migration**
  that de-pins any legacy version-pinned tokens in an existing `routing.toml`
  (`codex:gpt-5.5` → `codex`, `claude:opus-4-7` → `claude:opus`), recording a
  correction. After migration the table has no version pins and the migration
  is idempotent (no further rewrites).
- **Timing — proactive only.** No inline mid-task recovery. Since nothing is
  pinned this is rarely needed; if a CLI default itself breaks, the run fails
  and the user fixes the CLI.
- **Cadence — on `styx doctor` + a staleness check in `loadApp()` (~24h).**
  No daemon; consistent with "state is files on disk, no daemons." The check
  lives in `loadApp()` (not just REPL start) so it covers verb dispatch, one-shot
  turns, and the REPL alike. (The migration only rewrites once; subsequent
  refreshes just validate + refresh the cache timestamp.)
- **Config writes — migrate + record correction.** The one-time migration
  rewrites the table in place (atomic) and records a routing-correction memory.

**Non-goals (explicitly deferred):**

- Inline mid-task model recovery.
- Refreshing the brain capability cards in `cards.go`. They name models at the
  *tier* level (`opus`/`sonnet`/`haiku`, `qwen2.5-coder:*`), not version pins,
  so they do not rot the same way. Revisit later.
- agy and ollama discoverers (agy ignores `--model`; ollama is doctor-covered).
- styx *validating* effort values — they are pass-through; an invalid effort is
  the CLI's error to report, surfaced via the existing `ClassifiedError` path.

## Key insight: two axes, handled per channel

The fix separates **version** (auto-latest) from **effort** (user-controlled).
How each axis is realized depends on the channel's CLI:

| Channel | Version → "latest" | Effort mechanism | Notes |
|---------|--------------------|------------------|-------|
| codex   | send **no** `--model` → codex's own default | `-c model_reasoning_effort=<effort>` | was the broken case; defer fixes it |
| claude  | `--model <alias>` (`opus`/`sonnet`/`haiku`/`fable`) = latest of class | `--effort <effort>` | de-pin `opus-4-7`→`opus` |
| agy     | already defers (Send ignores `req.Model`) | none | `agy:default` cosmetic; untouched |
| ollama  | explicit local model name (user-pulled) | none | doctor already validates presence |

So routing names **at most a model *class* (claude) or nothing (codex)** for the
version axis, plus an optional **effort** per rule. Migration de-pins the two
channels that currently carry version strings; agy/ollama are left alone.

**Scope (from adapter investigation):** agy never sends a model id and ollama
staleness is doctor-covered, so neither needs a discoverer. MVP ships **one
discoverer (claude, to enumerate valid aliases for migration) and a codex
config reader (to report the current default for the cache/transparency)**.
The `Discoverer` registry remains extensible.

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
        │  discover (best-effort, per-channel timeout):
        ├─► codexDiscoverer   reads ~/.codex/config.toml (current default)
        └─► claudeDiscoverer  returns stable aliases
        │
        ▼
 scan routing.toml for version-pinned tokens
        │
        ├─► one-time migration: codex:<ver>→codex, claude:<ver>→claude:<alias>
        │     (atomic tmp+rename, comments preserved; idempotent after first run)
        ├─► record routing-correction memory (global scope, provenance)
        └─► write models.json cache (Results + refreshed_at)
```

Separately, at **dispatch time** (not in Refresh), the router carries an
`Effort` from the matched rule into the channel `Request`; the codex and claude
adapters translate it to their flags. This axis is static config, not discovery.

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
    Source    string   // "codex-config" | "claude-alias"
}
```

Each discoverer is tiny and independently testable. MVP ships two:

- **codex** — read the top-level `model = "..."` line from `~/.codex/config.toml`
  (line scan, no TOML-schema coupling). `Current` = that model. `Source =
  "codex-config"`. Missing file / no model line → error (channel skipped, warned).
- **claude** — pure: return `Available = [opus, sonnet, haiku, fable]`
  (the stable aliases). `Source = "claude-alias"`. No `Current`.

## Staleness cache & trigger

`~/.config/styx/models.json` (atomic tmp+rename via `paths` helpers):

```json
{
  "refreshed_at": "2026-06-18T12:00:00Z",
  "channels": {
    "codex":  {"current": "gpt-5.5", "source": "codex-config"},
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

## Effort plumbing (dispatch time)

- **Config:** `config.Rule` gains `Effort string` (`toml:"effort"`), optional.
- **Router:** the `Decision` returned by `Route` carries `Effort` copied from
  the matched rule. (For `parallel`/`fallback` entries the rule-level `Effort`
  applies to all of them in MVP — per-entry effort is out of scope.)
- **Channel `Request`:** gains `Effort string`. Dispatch paths
  (`research.go`, the REPL `runOneDispatch`, etc.) set it from the decision.
- **Adapters:**
  - **codex:** stop sending `--model` (defer to codex's default). When
    `req.Effort != ""`, append `-c model_reasoning_effort=<effort>`.
  - **claude:** keep `--model <alias>`. When `req.Effort != ""`, append
    `--effort <effort>`.
  - **agy / ollama:** ignore `Effort`.
- Effort is **never validated** by styx; an unsupported value surfaces as the
  CLI's own `ClassifiedError`.

## One-time migration & record

The migration de-pins legacy version strings in an existing `routing.toml` so
old configs converge to the defer-to-latest form. New installs are already in
that form (see seeded defaults), so this is a no-op there.

- **Scan:** find `channel:model` tokens in `use`, `fallback`, `parallel`, and
  `synthesize_with` values.
- **Surgical text rewrite (not TOML re-marshal):** edit only the target token in
  the raw file text so comments and layout survive. Atomic tmp+rename. Rules:
  - **codex:** any `codex:<version>` → bare `codex` (drop the version so codex
    uses its own default). `codex:interactive` is left untouched.
  - **claude:** de-pin a pinned version to its class alias by prefix match
    against the claude discoverer's `Available` (`claude:opus-4-7` →
    `claude:opus`, `claude:sonnet-4-6` → `claude:sonnet`). Tokens already equal
    to an alias, and `claude:interactive`, are left untouched.
  - **agy / ollama:** never touched.
  - **Idempotent:** after the first run no version-pinned tokens remain, so
    subsequent scans make no change.
- **Record:** one memory item per applied change — `KindRoutingPreference`,
  `Project: ""` (global), high `Confidence`, via the provenance system
  (Task 19.2). Example: `"routing: de-pinned codex:gpt-5.5 → codex (defer to
  latest), 2026-06-18"`.
- **Transparency:** print each change to stderr via `logStatus` (respect
  `--quiet`).
- `models.json` is rewritten on every refresh regardless, so the staleness
  timer resets even when no migration was needed.

## Error handling

Every discoverer is best-effort and isolated. A missing `~/.codex/config.toml`
warns for codex and skips it — it never aborts the refresh and never bricks any
entry point. The claude migration proceeds only if the claude discoverer
returned its alias set. A routing-write failure surfaces loudly in `doctor` but
is swallowed-to-warning in `loadApp()` (a refresh hiccup must not block a verb,
a one-shot turn, or the REPL). All subprocess/HTTP calls run under a context
with timeout, per styx convention.

## Testing

Table-driven, fakes over mocks, matching styx conventions:

- **codex discoverer:** temp-dir fake `~/.codex/config.toml` (inject path)
  covering present / missing-file / no-`model`-line cases.
- **claude discoverer:** pure — asserts the alias set.
- **Migration:** fixture `routing.toml` → assert `codex:<ver>`→`codex`,
  `claude:opus-4-7`→`claude:opus`, `*:interactive` and agy/ollama untouched,
  comments preserved, untargeted lines byte-identical, write atomic, and a
  second run is a no-op (idempotent).
- **Effort plumbing:** `config` test that `effort = "high"` parses into
  `Rule.Effort`; router test that `Decision.Effort` is populated; codex adapter
  test asserting `-c model_reasoning_effort=high` is in argv and `--model` is
  **absent**; claude adapter test asserting `--effort ultracode` is in argv.
- **Staleness:** inject a clock (no inline `time.Now()`); assert refresh fires
  when the cache is stale and is skipped when fresh.
- **Record:** assert the routing-correction memory is written with correct
  provenance (`KindRoutingPreference`, global, high confidence).
- **doctor integration:** extend `cmd/styx/doctor_test.go` to assert a refresh
  runs and reports per-channel results.

## Drift-contract impact

- `internal/modelsync/` is new — package doc comment + a section in
  `docs/ARCHITECTURE.md` (owner of `internal/**`), `last_verified` bumped.
- `internal/channel/*` (codex drops `--model`, adds effort; claude adds effort;
  `Request.Effort`), `internal/router` (`Decision.Effort`), `internal/config`
  (`Rule.Effort`, `[models]` block) all owned by `ARCHITECTURE.md` — update it.
- Seeded `default_routing.go` changes (de-pinned tokens + `[models]` +
  `effort` examples) and its `routing.toml` upgrade path.
- No verbs added/removed → `README.md` verb table unchanged.

## Rollout

1. Quick-patch (done): `codex:gpt-5` → `codex:gpt-5.5` to unblock; superseded by
   step 5's de-pin.
2. Effort plumbing: `Rule.Effort` → `Decision.Effort` → `Request.Effort` →
   codex/claude adapter flags (codex also drops `--model`).
3. `internal/modelsync`: codex reader + claude discoverer + `models.json` cache.
4. One-time migration (de-pin scan + atomic rewrite) + record-correction.
5. Re-author seeded `default_routing.go` to the de-pinned form (`codex`,
   `claude:opus`/`claude:sonnet`) with `[models]` and example `effort` fields.
6. Wire `Refresh` into `doctor` (always) and `loadApp()` (staleness).
