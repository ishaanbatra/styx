# Styx REPL Orchestrator — Design

**Date:** 2026-06-12
**Status:** Approved (pending final user review)

## Vision

Make styx a conversational AI orchestrator used like claude code: a persistent
REPL with memory that multiplexes live agent sessions (claude, codex, agy) and
local models (ollama), routing each utterance intelligently. Styx never grows
its own agentic tool loop — the channels are the agents; styx is the brain
that aims them, assembles their context, and remembers everything.

Optimization targets, in order: accuracy (frontier models where judgment
matters), efficiency (ollama for everything that doesn't cost accuracy; quota
spent only where it pays), performance (sub-second local turns, streamed
output, lazy resource use on 16GB RAM).

## Core model

- **Thin REPL orchestrator.** `styx` (no args) opens a persistent
  conversational session in the current project. `styx "..."` runs one turn of
  the same brain and exits. Existing verbs remain as explicit shortcuts.
- **Persistent agent threads.** A thread is a named, durable conversation with
  one agent (claude / codex / agy), independent of any OS process. The user's
  single styx conversation multiplexes all threads; the brain routes each
  utterance to the right thread(s) and streams structured events back.
- **Terminal handoff as zoom-in.** Open-ended collaboration hands the terminal
  to interactive claude code, resuming the same thread's session; on exit,
  styx ingests a summary into the conversation and memory.

## Architecture

```
cmd/styx            REPL frontend (new) + existing verbs (kept)
internal/brain      ollama turn router → structured Action     (new)
internal/agent      persistent agent threads + lifecycle       (new)
internal/memory     per-project + global memory, embeddings    (new)
internal/channel    one-shot sends (kept — brain & pipelines use it)
internal/pipeline   auto/research/etc (kept — become brain-invokable)
internal/intel      kept — feeds thread seeds and memory
internal/budget     kept — extended with per-model-tier counters
internal/progress   kept — renders thread streams in the REPL
```

**Turn loop:** user input → memory recall (embeddings, ~50ms) → brain decision
(ollama structured output, sub-second) → styx prints a one-line routing
decision (`◆ claude·fable › planning session refactor`) → act, streaming
results into the REPL. Seamless but never silent; anything is interruptible
mid-stream.

## Agent threads (internal/agent)

**Turn mechanics.** Each turn invokes the CLI fresh:
`<cli> --resume <session-id> -p --output-format stream-json` (claude;
codex/agy equivalents). Conversation state lives in the CLI's own session
store, so threads survive styx restarts, sleep, and crashes for free.
`--model` is per-invocation, enabling per-turn tier switching within one
thread (fable for the planning turn, sonnet for implementation). The ~2s CLI
startup per turn is accepted; a long-lived `--input-format stream-json`
process is a later optimization, not v1.

**Lifecycle:**

- **Lazy start.** Threads spawn on first dispatch (16GB RAM constraint —
  never eagerly). Seed = intel context + relevant memory + role line.
- **Context meter.** Stream-json usage events give exact per-thread context
  size; the REPL displays it (`claude ▮▮▮▮▯ 62%`).
- **Distill-and-restart at ~70% (configurable).** Styx asks the thread (cheap
  tier) for a structured handoff — decisions made, files touched, in-flight
  work, dead ends — starts a fresh session seeded with it, and saves the
  distillation to project memory. Thread name and conversation continuity are
  preserved; user sees `↻ claude thread compacted`.
- **Capability probing.** `styx doctor` probes each CLI for resume and
  stream-json support. CLIs lacking them (agy is the open question) degrade
  to styx-maintained continuity: each call seeded with styx's rolling summary
  of the thread. Same UX, slightly lossier; doctor reports which mode each
  agent runs in.
- **Crash recovery.** Failed resume → rebuild from last distillation +
  transcript tail. Threads are never lost, only rolled back to the last
  checkpoint.
- **Permissions.** Headless dispatches run with permissions pre-granted
  (`--dangerously-skip-permissions` or equivalent), matching today's
  `execute` behavior. Interactive handoff retains the CLI's native prompts.

## The brain (internal/brain)

**Model:** small instruct model (qwen3:4b class, ~2.5GB, pulled at setup) via
ollama **structured output** mode. It never free-generates; every turn it
emits JSON matching the Action schema:

```json
{
  "action": "reply | dispatch | parallel_dispatch | pipeline | handoff | remember | escalate",
  "dispatches": [{
    "thread": "claude | codex | agy | ollama",
    "model": "fable | opus | sonnet | haiku | <ollama model>",
    "message": "...",
    "cli_options": ["--add-dir ../other-repo"],
    "rationale": "one line, shown to user"
  }],
  "pipeline": "research | auto | review | intel",
  "reply": "...",
  "confidence": 0.0
}
```

Schema-constrained decoding makes a 4b model reliable: classification with
slots, not open-ended reasoning. Brain context is kept deliberately small —
rolling conversation summary + last few turns + thread status lines + top-k
memory hits + condensed capability cards — to stay sub-second.

**Capability cards (CLI expertise).** Each adapter ships a curated,
structured card encoding expert knowledge of its CLI surface: flags, modes,
session management, output formats, and "when to use what" guidance (e.g.
long multi-file work → claude with plan-mode handoff; quick sandboxed script
check → codex exec). Two layers keep cards honest:

1. **Curated card** — hand-written expertise, lives in the repo.
2. **Version validation** — `styx doctor` diffs each CLI's `--help`/
   `--version` output against the card and flags drift, so CLI updates
   surface as "knowledge stale" warnings instead of silent flag misuse.

The brain sees condensed cards every turn and may set `cli_options` per
dispatch.

**Model-tier policy** (declared in routing.toml, not hardcoded):

| Task kind | Tier | Why |
|---|---|---|
| brainstorm, architecture, plan, hard debugging | fable | judgment-heavy, worth the quota |
| implement, refactor, apply plan, review | sonnet (opus when the brain flags `complex`) | strong execution, cheaper |
| summarize, distill, commit messages, classify | ollama | free; haiku-tier work stays local |

Budget integration: per-tier message counters extend the existing budget DB.
When fable runs hot for the week, planning degrades to opus and the routing
line says so.

**Escalation & degradation:**

- Confidence below threshold → escalate the routing decision itself to claude
  haiku (one cheap message against the same schema).
- Ollama down or emitting invalid JSON twice → REPL asks the user directly
  ("which thread?"). The REPL never bricks.
- Routing corrections from the user ("no, codex should do this") are saved as
  `routing-preference` memories and recalled on similar turns — the brain
  learns to be this user's router.

## Memory (internal/memory)

**Stores.** SQLite per project at `~/.config/styx/state/memory/<project>.db`
plus `global.db` for cross-project facts and preferences. Item: kind
(`decision | todo | distillation | brief | fact | routing-preference`), text,
embedding, source (thread/session/pipeline), timestamp.

**Recall.** Embeddings via ollama `nomic-embed-text` (~270MB, ~50ms/query).
Every turn: embed utterance, top-k cosine over project + global stores,
inject hits into brain context. Brute-force cosine in Go — personal scale
(thousands of items) needs no vector DB.

**Write paths (mostly automatic):** thread distillations; session-end
summaries; research briefs and pipeline outputs (indexed from disk); routing
corrections; explicit `remember` actions ("note this for later").

## Existing verb mapping

| Verb | Fate |
|---|---|
| `styx` (bare) | New: opens REPL in current project |
| `styx "..."` | New: one-shot brain turn, exits |
| `research`, `auto`, `plan`, `review`, `intel` | Kept as verbs and brain-invokable pipelines; outputs indexed into memory |
| `grunt`, `think`, `explain`, `summarize`, `critique` | Kept as shortcuts; brain handles naturally in REPL |
| `check`, `budget`, `runs`, `route --explain` | Kept; also slash-commands `/status`, `/budget`, `/threads`, `/why` |
| `build`, `execute` | Absorbed into thread dispatch + interactive handoff |
| `doctor` | New: preflight — CLI presence/versions vs capability cards, resume/stream-json probing, ollama model pulls |

Nothing currently in use breaks; the REPL is additive.

## Reliability work (required by the REPL, pays down existing debt)

- Wire the existing-but-unused circuit breaker into routing: repeated channel
  failures → cooldown; the brain is informed and routes around.
- Timeouts on every subprocess turn (claude/codex currently have none),
  configurable per channel.
- Fix silent error swallowing (`c, _ := Parse(...)` in research loop, auto
  pipeline); errors surface as visible thread events in the REPL.
- Real token accounting for cloud channels from stream-json usage events,
  replacing `len/4` estimates (ollama keeps estimates; it is free).

## Resource budget (M1 Pro, 16GB)

Resident local models: brain (~2.5GB) + embeddings (~0.3GB), with
qwen2.5-coder:7b loaded on demand for grunt work. The 14b model is reserved
for explicit requests when memory allows. Agent threads are processes only
during a turn (per-turn resume), so steady-state RAM is the REPL + ollama
residents.

## Testing strategy

- **Fake agent CLI** (testdata script speaking stream-json) drives thread
  lifecycle tests: spawn, meter, distill-at-threshold, restart,
  resume-after-crash — no real CLIs needed.
- **Brain behind an interface**; unit tests use a scripted fake. An env-gated
  integration suite runs real ollama, asserting the schema holds and routing
  accuracy ≥ threshold on a fixture set of ~50 labeled utterances — the
  regression net for "is the 4b brain good enough."
- **Memory:** deterministic recall tests with fixed vectors.
- **E2E:** scripted REPL sessions against fake CLIs.

## Out of scope (v1)

- Long-lived streaming agent processes (per-turn resume first; optimize later).
- Styx-internal tool loop of any kind (file edits, bash) — channels only.
- Token-level cost optimization (subscriptions meter messages, not tokens).
- Routing real implementation work to ollama to save quota (accuracy is
  paramount; local models handle classification, compression, summarization,
  embeddings, and grunt one-shots only).

## Decisions log

- Thin REPL over one-shot dispatcher: conversation memory is how the user
  works; dispatcher-as-router alone adds no value over doing it mentally.
- Ollama brain with escalation over claude-as-brain: free sub-second turns;
  structured outputs + small constrained job make a 4b model sufficient, with
  haiku escalation as the safety valve.
- Per-turn `--resume` over persistent processes: durability for free, per-turn
  model switching, simpler failure handling; ~2s startup cost accepted.
- Per-project + global memory with automatic recall over session transcripts
  only: "what did we decide about X" must work without manual session
  archaeology.
- Evolve in place (approach A) over fresh binary: existing channels, budget,
  intel, and pipelines were built for this evolution.
