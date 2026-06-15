# Routing-brain prompt iteration — results

**TL;DR:** the `llama3.2:3b` routing brain went from **84.8% → 96.0%** on the
canonical Go gate (`TestRoutingAccuracy`, 99 labeled utterances) by rewriting
`systemPreamble` only (no model change, no dataset change, no code-logic change).
The shipped prompt is `variants/v5.txt`, ported verbatim into
`internal/brain/prompt.go`. 96.0% is **stable across two canonical Go runs** with
identical misses, and matches the promptfoo prediction exactly.

## Numbers

| Variant | What changed | Accuracy (faithful promptfoo) |
|---------|--------------|-------------------------------|
| baseline | shipped prompt before this work | **84.8%** (84/99) |
| v1 | full surgical rewrite: pipeline carve-outs, handoff/remember/ollama/status/research/review boundaries, +13 examples | 94.9% (94/99) |
| v2 | v1 + "assess/critique/is-it-sound → claude" rule | 87.9% (87/99) — **rejected** |
| v3 | v1 + agy size-rule + review "the changes" + 2 examples | 91.9% (91/99) |
| v6 | v1 + the two rule lines only, **no new examples** | 92.9% (92/99) |
| **v5** | **v3's fixes + critic's de-overfit fixes + a `parallel_dispatch` anchor example** | **96.0% (95/99)** |

**Canonical Go gate on v5:** `95/99 = 96%`, run twice, identical misses.
The faithful harness and the Go gate agreed on the baseline miss set *and* the
v5 miss set, byte-for-byte — the eval predicts reality.

## What the search taught us (3B-specific)

1. **Examples anchor a 3B model more than rules.** v6 (rule lines, no examples)
   *regressed* the `review` cases that v1 got right; v5 (same rules + review/parallel
   examples) fixed them. Prose rules alone don't stick; JSON examples do.
2. **Adding locally-correct rules can knock over unrelated cases.** v2's
   "critique → claude" rule fixed the two analysis questions but dragged **10**
   correct cases (every `parallel_dispatch` "claude AND codex", plus `auto`/`ollama`)
   into `dispatch/claude`. v3's two extra examples destabilized `parallel_dispatch`
   because v1 had **zero** parallel examples to anchor them. The fix wasn't fewer
   rules — it was adding one `parallel_dispatch` example (v5) to rebalance.
3. **The biggest baseline failure was pipeline-verb leakage:** the 3B model
   keyword-matched `intel`/`review`/`research` whenever an utterance mentioned the
   codebase, "find", "context", "cleaner", or "diff". A crisp "pipeline = these 4
   exact ops; repo code-work = dispatch:claude" framing fixed 4 misses at once.

## Misses fixed (baseline → v5): 12 of 15

`tighten the systemPreamble…`, `explain what internal/router does…`,
`generate table-driven test stubs…`, `verify whether fable 5 is available…`,
`remember (that) I prefer table-driven tests…` (both), `are we under budget…`,
`make distill-and-restart trigger at 70% context`, `is there a cleaner way to
structure the thread lifecycle?`, `find every place we swallow errors…`,
`do a critical pass on the diff…`, `hand me an interactive session…`.

## Remaining 4 misses — bucketed

| Utterance | Got | Want | Bucket |
|-----------|-----|------|--------|
| `have codex double-check the algorithm in cosine()` | dispatch/codex but **malformed JSON** | dispatch/codex | **model/decoding limit** |
| `is this routing approach sound or am I missing something?` | reply | dispatch/claude | **label dispute** |
| `what's the blast radius if I change the Dispatch struct?` | reply | dispatch/claude | **label dispute** |
| `run the full build cycle on adding rate limiting` | dispatch/claude | pipeline/auto | **1 genuine miss** |

- **`cosine()` (model limit):** the routing decision is *correct* (codex) — the
  3B model just produced unterminated JSON (`"message":"…cosine()}]} }`); the `()`
  trips its structured-output serializer even under ollama's `format` grammar.
  No prompt fixes this. See escalation note.
- **The two disputes:** the model answers these questions directly (`reply`) with
  high confidence. That's defensible — they read like questions. The labels say
  "send to claude for analysis." Forcing this (v2) cost ~7 points of collateral,
  so it's not worth encoding on a 3B. **Needs your adjudication** (see below).
- **`run the full build cycle` → auto:** the one real residual. The model read
  "build cycle on adding rate limiting" as an implementation dispatch. Chasing it
  risks re-destabilizing `auto`/`parallel` (observed in v3/v6), so it was left.

## Label disputes to adjudicate (surfaced, not tuned)

1. **`is this routing approach sound…?` / `what's the blast radius…?`** —
   labeled `dispatch/claude`; the brain answers `reply`. Decide whether
   "is X sound / what's the blast radius" meta-questions should go to claude
   (analysis) or be answered directly. We did **not** tune to the label because
   v2 proved a 3B can't apply that rule without large collateral damage.
2. **`classify these test failures as flaky vs real` → ollama** (label) — an
   independent review flagged this: flaky-vs-real triage arguably needs claude's
   judgment, not a "classification → ollama" keyword shortcut. v5 currently
   matches the label (ollama); worth re-examining the label itself.

## Overfitting check (independent review)

An independent adversarial reviewer diffed baseline vs the candidate and judged
which new rules *generalize* vs *fit the 99 fixtures*. Verdict: mostly
principled. Three de-overfit fixes were folded into v5 over v3:

- **research** reframed from a brittle keyword (`"verify whether X is available
  → research"`, a near-lift of one fixture) to the *principle* "the answer lives
  outside this repo," plus an internal counter-example (`find out how our cooldown
  is configured → claude`).
- **review** scoped to *uncommitted* work (diff/changes/staged) vs a
  PR/module/plan/design → claude, removing the contradiction with the claude
  card's old "code review" wording.
- the claude capability line no longer says bare "code review."

Residual generalization gaps the reviewer flagged (out of scope for this
dataset, recommended as follow-ups): `escalate` is never exemplified and the
dataset has zero `escalate`/compound-intent/destructive-op cases, so real-world
behavior there is untested.

## Recommendation on escalation / ceiling

We are **not** near a problematic ceiling: excluding the model-limit JSON case
and the two disputes, effective routing accuracy is ~98%. So:

- **Don't keep grinding the prompt against these 99.** v3 and v6 both went
  *backwards* — the 3B is at the point where extra rules destabilize as much as
  they fix. Diminishing returns with real risk.
- **The confidence-threshold → haiku escalation valve won't recover these
  residuals.** All four misses are emitted with high confidence (0.8–0.9), so
  raising `ConfidenceThreshold` wouldn't catch them. Keep the valve for
  genuinely *uncertain* turns (the dataset has none), not for these.
- **Highest-value next lever is reliability, not routing:** the `cosine()` case
  routes correctly but emits truncated JSON. A JSON-repair/retry on
  structurally-incomplete output (the routing decision was already right) is a
  ~1-point gain with no prompt change and no model upgrade.
- **If real-world accuracy proves lower than 96%** (likely on the OOD categories
  the reviewer flagged — compound intents, internal-vs-external "find out"
  ambiguity, control-plane commands), the right response is to **grow the labeled
  dataset to cover those categories and re-run this harness**, not to over-fit the
  current 99. The escalation valve remains the safety net for low-confidence turns.
