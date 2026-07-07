---
topic: self-improving styx — learning loop over dispatch outcomes, retrospectives, and user preferences
date: 2026-07-07
status: approved-design, plan pending
depends_on: 2026-07-07-styx-async-dispatch-design.md (the `outcomes` table)
---

# Styx self-improvement (learning loop)

**Goal:** styx gets better at routing and at working the way its user works,
by learning from three feeds: mechanical dispatch outcomes, conductor
session retrospectives, and explicitly stated user preferences. Learning is
inspectable, reversible, and additive.

**The prime constraint:** all learning lands as **plain-text memories with
provenance** — never code changes, never edits to `routing.toml`. The
transparent, user-edited routing table stays absolute (core styx decision).
A bad learned memory is at worst a bad sentence in a prompt: visible via
`--list`, removable via `--forget`.

**Non-goals (explicitly out of scope):**
- Self-updating capability cards / adapters (doctor-triggered research) —
  not selected.
- Styx modifying its own codebase (dogfooding loop) — not selected.
- Auto-tuning router (learned outcomes rewriting routing rules) — rejected;
  violates the transparent-table decision.
- Any background/daemon learning — the digest runs only when the user runs
  `styx learn`.

## Decisions (from design review, 2026-07-07)

1. Signal source: **mechanical + optional rating** — always-on outcome rows
   (errors, retries, duration, tokens) plus the conductor's optional
   `rate_dispatch` when an outcome was notably good or bad.
2. Learning cadence: **`styx learn` verb + session nudge** — digestion is a
   manual verb using the local ollama brain (free, private, no daemons);
   launch guidance nudges the conductor to save short retrospectives.
3. Scope selected by user: learn routing from outcomes, guidance/prompt
   evolution via retrospectives, and user preferences. (Also see non-goals.)

## Design

### Memory kinds

- `routing-preference` (existing, end-to-end: memory_save accepts it, launch
  injects top-5 into guidance).
- `user-preference` (new): how the user likes to work — e.g. "prefers
  table-driven tests", "wants concise summaries".
- `retrospective` (new, input-only): raw 2-line session notes. Fuel for the
  digest; **never injected into guidance directly**. Marked consumed once
  digested; consumed retrospectives are skipped by future runs.

Every memory written by the digest carries provenance:
`learned-by: styx-learn`, date, and a one-line evidence citation
("14/15 clean codex implements, 30d").

### Input feed

- The `outcomes` table + ratings (built by the async-dispatch spec; sync
  dispatches feed it too, so learning works even before/without background
  use).
- Retrospectives: launch guidance nudges the conductor to `memory_save` a
  2-line retrospective (what worked / what didn't) at natural session
  endpoints.
- Explicit preferences: when the user states one ("remember I prefer X"),
  the conductor saves it as `user-preference` — no digest needed; explicit
  statements are trusted as-is.

### Scorecard (deterministic layer)

Pure-Go aggregation over the trailing 30 days of outcomes, grouped by
cli × routing signal: attempts, clean-success rate (no classified error and
not rated bad), median duration_s, median tokens, rating tallies.
`styx learn --scorecard` prints it; the digest consumes it as ground truth.
No LLM involvement; independently useful.

### `styx learn` (the digest)

Manual verb. Steps:
1. Render the scorecard.
2. Gather unconsumed retrospectives + rating notes since the last run.
3. Prompt the **local ollama brain** for at most 5 candidate memories
   (`routing-preference` / `user-preference`), each with a confidence score
   and a scorecard/retrospective citation.
4. **Evidence guard (mechanical):** candidates whose citation does not match
   a real scorecard line or retrospective are dropped before writing.
5. **Dedupe** against existing memories via the embedding store:
   near-duplicates update the existing memory's evidence + date instead of
   multiplying entries.
6. Write survivors; print exactly what was learned. Mark digested
   retrospectives consumed.

Flags: `--dry-run` (show candidates, write nothing), `--scorecard` (stats
only), `--list` (current learned set with provenance), `--forget <id>`
(hard delete one memory).

### Application (closing the loop)

Launch-time guidance injection is the entire application mechanism:
- existing "Routing preferences (learned)" section: top-5
  `routing-preference` by recency + confidence;
- new "User preferences (learned)" section: top-5 `user-preference`, same
  ranking.
Nothing else consumes learned state.

### Failure modes

- Ollama down → `styx learn` fails loudly; nothing partial is written.
- Brain hallucination → evidence guard + dedupe + 5-candidate cap + the
  printed output is reviewed by the user who just ran the verb.
- Preference drift → newer memories outrank older; `--forget` for hard
  removal.
- Retrospective bloat → consumed-marking keeps each note digested at most
  once; the store holds notes, not transcripts.

## Build order

The async-dispatch spec lands first (it creates the `outcomes` table +
`rate_dispatch`). This spec is buildable immediately after, with only that
table as a dependency.

## Testing

- Scorecard: table-driven unit tests over a seeded sqlite fixture (mixed
  successes, classified errors, ratings; verifies grouping + medians).
- Digest: httptest ollama returning scripted candidates — tests the evidence
  guard, dedupe path, cap, dry-run, consumed-marking, and loud failure when
  ollama is unreachable.
- Guidance injection: extend the existing `conductorGuidance` tests with the
  user-preference section.
- E2E: one assertion in the existing harness that outcome rows exist after
  the harness's dispatches.
- Drift contract: ARCHITECTURE.md (memory kinds, new verb, budget-db
  outcomes table), README verb table gains `learn`.
