# Routing-brain prompt iteration — results

> **Update (2026-06-16) — brain upgraded to `qwen2.5-coder:7b` + per-dispatch
> risk tiers (`v15`, shipped).** The default brain was switched `llama3.2:3b` →
> `qwen2.5-coder:7b` (the 190-case re-tune below concluded the 3B was at its
> ceiling — further gains needed a bigger brain, not more prompt rules), the
> fixture set was extended **190 → 192** (2 explicit-`ship` cases; 8 now carry a
> `want_risk` label: 4 `read`, 2 `edit`, 2 `ship`), and a coarse per-dispatch
> **risk tier** (`read`|`edit`|`ship`) was taught via a risk paragraph + 5
> risk-annotated few-shot anchors. The gate now scores routing, risk-emission,
> and a folded gate accuracy separately. **`v15` is frozen and ships** (==
> `prompt.go`'s `systemPreamble` == `generated/preamble_shipped.txt`).

## 192-case `qwen2.5-coder:7b` run + risk tiers — `v15` (shipped)

**TL;DR:** on the 7b, the shipped preamble `v15` scores **routing 178/192 (93%),
risk-emission 6/8 (75%), folded gate 176/192 (92%)** on the canonical Go gate
(`TestRoutingAccuracy`), reproduced **byte-for-byte** by the promptfoo harness
(same numbers, same 16-miss set). Adding the per-dispatch risk prose was
**routing-neutral** — the no-risk baseline `v14` scored 177/192, so risk language
cost nothing (in fact +1) while lifting risk emission from 2/8 to 6/8.

### Numbers (192-case, Go gate == faithful promptfoo)

| Variant | What changed | Routing | Risk | Folded gate |
|---------|--------------|---------|------|-------------|
| v14 | previous shipped preamble, **no risk language** | 177/192 (92%) | 2/8 (25%) | 172/192 (90%) |
| **v15** | **v14 + risk paragraph + 5 risk-annotated anchors (2 read, 2 edit, 1 ship)** | **178/192 (93%)** | **6/8 (75%)** | **176/192 (92%)** |

`v14`'s 2/8 risk is structural: with no risk prose it emits no `risk`, so only the
2 genuine `edit` fixtures pass (empty risk → `edit` default); all 4 `read` and 2
`ship` fixtures miss. `v15`'s prose is what drives the 6/8.

### Why `v15` was frozen (not reverted)

The "set `risk` on every dispatch" wording was unvalidated on the 7b; the decision
rule was "keep ~91%+ routing **and** improve risk, else revert to a softer
optional-risk fallback." `v15` cleared both: routing 178/192 (≥ `v14`'s 177, well
above the floor) **and** risk 6/8 (> the prior 5/8 the softer wording got). The
risk prose is additive, not a routing tax — so `v15` stays. Per the small-model
findings below, the risk anchors were **not** expanded to chase the residual 2/8
(that overfits the 8 labeled fixtures); REPL-side enforcement guarantees safety
regardless of emission accuracy.

### Residual 16 misses (identical on Go gate and promptfoo)

- **2 risk-only misses** (routing correct, `risk` omitted → safe `edit` default):
  both are `read`-class `dispatch:claude` explains — "explain what internal/router
  … end to end" and "walk me through what internal/channel … end to end". The
  model under-emits `read` on long "explain … end to end" phrasings.
- **14 routing misses**, all in known buckets: the codex/claude implementation
  frontier (ship-risk-gate, harden-parser, ClassifiedError-pattern,
  ParseClaudeEvent-rework), pipeline-`review`/`research` keyword leakage
  ("evaluate this design", "walk the … design", "find out how … is configured",
  "the cooldown keeps misfiring"), and `reply`-vs-`claude` label disputes
  ("is this approach sound", "what's the blast radius", "remind me which
  checkpoint is next").

---

> **Update (2026-06-15) — codex-as-implementer rework + dataset expansion.** The
> brain was flipped to route well-scoped implementation to **codex** (claude =
> planner/architect/reviewer), and `testdata/brain/utterances.json` was expanded
> **99 → 190** (91 new user-voice utterances + 8 audited `claude`→`codex` flips;
> 4 borderline flips kept on claude). `prompt.go`/`cards.go` were reworked and
> `variants/v7.txt` is the new shipped preamble. **The reworked preamble has now
> been re-tuned on the 190-case gate: 83.7% (untuned) → 91.1% (173/190) on the
> canonical Go gate, stable across two runs — see the next section.** The
> 84.8%→96.0% numbers further below are the prior 99-case, pre-codex-policy
> result — kept as the iteration-method reference.

## 190-case re-tune (codex-as-implementer policy) — 83.7% → 91.1%

**TL;DR:** after the codex-as-implementer flip and the 99→190 dataset expansion,
the reworked-but-untuned preamble (`v7`) scored **83.7% (159/190)**. Iterating
**few-shot examples only** (no model/dataset/code/label change, no new prose
rules) brought the shipped preamble to **91.1% (173/190)** on the canonical Go
gate (`TestRoutingAccuracy`), **stable across two runs with an identical 17-miss
set**, and byte-matching the promptfoo harness exactly. Shipped prompt is
`variants/v7.txt`, ported verbatim into `internal/brain/prompt.go`.

### Numbers (190-case, faithful promptfoo == Go gate)

| Variant | What changed vs the previous kept variant | Accuracy |
|---------|-------------------------------------------|----------|
| v7 (untuned) | codex-as-implementer cards/preamble, **not yet tuned** | **83.7%** (159/190) |
| v8 | + **5 generalizing codex few-shot anchors** (add-col+migration, add-flag+thread, rename-sweep, mocks→fakes, write-tests) | 87.9% (167/190) |
| v9 | v8 + ollama-stub/review/intel examples | 87.9% (167/190) — **rejected** (destabilized codex) |
| v10 | v8 + reword test example + 1 handoff anchor | 87.9% (167/190) |
| v11 | v10 + **reply/review/intel/auto anchors** | 90.0% (171/190) |
| v12 | v11 + review-anchor reword (drop "before i commit") | 90.0% (171/190) |
| v13 | v12 + budget-neutral codex probe (−find-out, +apply-pattern) | 88.9% (169/190) — **rejected** |
| **v14** | **v12 + jot-down→remember + "think through together"→handoff** | **91.1%** (173/190) ← **shipped** |

**Canonical Go gate on v14 (== shipped `v7.txt`/`prompt.go`):** `173/190 = 91%`,
run twice, **identical 17-miss sets**; the Go-gate miss set equals the promptfoo
miss set byte-for-byte.

### What the 190-case search taught us (3B-specific, re-confirmed)

1. **Examples >> prose rules, again.** Every point of the +14 came from few-shot
   anchors; not one prose rule was added. The biggest single lever was the 5
   codex verb-class anchors (codex miss bucket 13→5).
2. **The 3B has a hard "example budget."** Adding anchors to one bucket
   *deterministically* knocked over cases in another: `v9` (ollama/review/intel
   batch) and `v13` (a budget-neutral swap that dropped the load-bearing
   `find out how our cooldown is configured → claude` example) both regressed the
   hard-won codex bucket and were discarded. The win came from anchoring
   *distinct, high-confidence* buckets (reply/review/intel/auto) rather than
   ollama, which sits adjacent to codex.
3. **The model is deterministic at temperature 0** — a control variant's 17-miss
   set was byte-identical across two separate runs — so collateral was
   attributable signal, not noise. This is also why the Go gate reproduces the
   promptfoo number and is stable across runs.

### Misses fixed (untuned v7 → v14): 16

7 codex policy-flip cases (provenance-columns+migration, both mocks→fakes,
rename-ParseClaudeEvent, distill@70%, --since flag), both review novel-phrasings,
intel-regenerate-context.md, 2 reply status questions, 2 handoff brainstorm
cases, the jot-down→remember case, the split→parallel case, and the
risk-tier-field claude case.

### Residual 17 misses — bucketed

| Bucket | Cases | Disposition |
|--------|-------|-------------|
| **True regressions vs untuned v7** (2) | `build…ship ollama HTTP adapter`→auto; `research…codex cli…`→research | jittery keyword casualties of the codex anchors ("ollama adapter"/"codex"); net is +14 so **left** rather than re-destabilize codex |
| **codex/claude frontier** (6) | ship-risk-gate, per-channel-timeouts, fix-off-by-one-budget, apply-ClassifiedError, dry-run-flag, ParseClaudeEvent-tests | all want codex; the 3B reads them as architectural/systemy → claude. Won't move without collateral |
| **model/decoding limit** (1) | `have codex double-check … cosine()` | routing is *correct* (codex); the 3B emits truncated/empty JSON on `()`. No prompt fixes it |
| **escalate** (2) | the 2 escalate exemplars | inherently hard for a 3B (it answers confidently as research/handoff) |
| **label dispute** (1) | `is this routing approach sound…?` | model answers `reply`; forcing claude cost ~7pts on the 99-set — **needs adjudication** |
| **contentious / OOD** (5) | brainstorming-*skill*→claude (token clash, lands intel); compound `do…and ship it`→auto; `exa:` mcp-auth; `stub…implementation of the Channel interface`→ollama; `wanna riff…together?`→handoff | left; chasing risks the codex gains |

### Labels surfaced for adjudication (not tuned to)

Unchanged from the 99-case analysis, plus two new families introduced by the
expansion: the 2 `escalate` exemplars and the compound "do X **and ship it**"
(terminal intent = auto) are inherently hard for a 3B to disambiguate — forcing
them regressed other cases, so they are reported, not encoded. `utterances.json`
labels were treated as the user's fixed gate and **not** changed. Per Checkpoint
A, the real levers beyond ~91% are a bigger brain or more/clearer fixtures, not
more prompt rules.

---

## (Reference) original 99-case iteration — 84.8% → 96.0%

The section below is the prior, pre-codex-policy result on the 99-utterance set.
It is kept as the **iteration-method reference** (it is not comparable to the
190-case numbers above — different policy and dataset).

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
