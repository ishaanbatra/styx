# Routing-brain eval harness (promptfoo)

A **fast iteration loop** for the styx routing brain's system prompt
(`internal/brain/prompt.go`). It is a dev tool only — no Go deps, no `go.mod`
changes, run via `npx`.

**The canonical gate is the Go test, not promptfoo:**

```bash
STYX_BRAIN_IT=1 go test ./internal/brain/ -run TestRoutingAccuracy -v -count=1 -timeout 30m
```

promptfoo numbers are directional and exist only to iterate quickly; this
harness is built to *byte-match* `brain.go` so the two agree (they did: baseline
and the shipped prompt scored identically on both). If they ever diverge, trust
the Go test and fix the harness.

## Why it's faithful

- **`provider.js`** replicates `brain.go`'s `chat()` exactly: `POST /api/chat`
  with `stream:false`, `think:false`, `format:ActionSchema`,
  `options.temperature:0`, a `[system,user]` message pair, and the same
  retry-once-on-empty/invalid loop as `Ollama.Decide`. (A custom provider, not
  the native `ollama:chat`, because that exact body — `think:false` *with* a
  structured `format` — is what real styx sends.)
- **`prompt.js`** assembles the prompt exactly as `BuildPrompt` does for the
  integration-test path: `system = <preamble> + cards block`, `user =
  "User utterance:\n<text>"`.
- **`gate.js`** replicates `Action.Valid()` + `TestRoutingAccuracy`'s match
  logic, so a promptfoo "pass" means exactly what a Go-gate "correct" means.
- **`gen-tests.js`** generates tests from `testdata/brain/utterances.json` — the
  *same* dataset the Go gate uses, never a fork. The two can't disagree on labels.
- **`generated/`** is produced from the Go source itself (`braindump` tool) so
  the cards block and schema never drift from `cards.go` / `action.go`.

## Run it

```bash
cd eval/promptfoo
npx promptfoo@latest eval -j 1 --no-cache   # -j 1 = serial, faithful to the Go gate
npx promptfoo@latest view                   # inspect per-utterance misses in the browser
python3 compare.py /tmp/out.json            # per-variant FIXED/REGRESSED vs baseline (use -o /tmp/out.json)
```

`-j 1` matters: the local 3B model serializes anyway, and parallel requests
cause empty-content responses that don't reflect real (sequential) styx behavior.

## Layout

| path | role |
|------|------|
| `promptfooconfig.yaml` | matrix config (default: `v5` vs `v7`) |
| `prompt.js` | builds `[system,user]`; one named export per variant |
| `provider.js` | byte-faithful ollama `/api/chat` client |
| `gate.js`, `gate-assert.js` | gate-equivalent scoring |
| `gen-tests.js` | tests from `testdata/brain/utterances.json` |
| `variants/baseline.txt` | **frozen** pre-iteration preamble (84.8%) |
| `variants/v1..v6.txt` | iteration history (see `RESULTS.md`); `v5` = previous shipped (pre-codex-implementer) |
| `variants/v7.txt` | **what ships** -- equals `prompt.go`'s `systemPreamble` (codex-as-implementer, re-tuned to 91% on the 190-case gate) |
| `variants/v8..v14.txt` | 190-case re-tune trail (see `RESULTS.md`); `v14` == `v7` (the shipped winner) |
| `generated/` | code-mirrored: `cards_block.txt`, `schema.json`, `preamble_shipped.txt` |
| `braindump/main.go` | regenerates `generated/` from Go source |
| `buckets.py` | groups a run's misses by want-label (`python3 buckets.py /tmp/out.json [variant]`) -- the per-bucket view used to drive iteration |
| `RESULTS.md` | the iteration writeup + escalation recommendation |

## Regenerate after editing `prompt.go` / `cards.go`

```bash
go run ./eval/promptfoo/braindump -outdir eval/promptfoo/generated
# fidelity check: the shipped prompt should equal the shipped variant
diff <(cat eval/promptfoo/generated/preamble_shipped.txt) eval/promptfoo/variants/v7.txt
```
