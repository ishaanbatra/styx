## Merged Review: BLOCKING / IMPORTANT / NIT

### BLOCKING
None. (Both reviewers agree.)

### IMPORTANT
None. Both reviewers independently verified the overflow arithmetic:
- `calc/calc.go:24-26` — `Square` (`v*v`) is correct and minimal; the doc comment at `calc/calc.go:21-23` correctly describes Go's defined (non-UB) wraparound for signed integer overflow.
- `calc/calc_test.go:47-56` — `strconv.IntSize`-gated boundary constants are arithmetically correct (independently verified: 46341² mod 2³² as int32 = −2147479015; 3037000500² mod 2⁶⁴ as int64 = −9223372036709301616; `MaxInt²`/`MinInt²` wrap to 1/0 generically for any word size).

### NIT
- **`calc/calc_test.go:66-67`** — The boundary case computes its expected value via the same `*` multiplication expression under test (`maxSquareRoot * maxSquareRoot`), making the oracle partly self-referential rather than independent. Suggested fix: replace with an explicit, architecture-specific expected constant (or at minimum a named constant with a comment noting "does not overflow by construction") so the test isn't validating itself. *(Both reviewers flagged this same spot from slightly different angles — codex on independence-of-oracle grounds, sonnet on readability grounds.)*
- **`calc/calc_test.go:48-55`** — The 32-bit boundary assertions only execute when tests run on a 32-bit target; ordinary `amd64` CI runs never exercise this branch. If 32-bit correctness matters, add a `GOARCH=386` CI job (confirmed the package cross-compiles for `linux/386`). *(codex)*
- **`calc/calc.go:21-23`** — Comment phrasing "wraps according to Go's signed integer overflow semantics" is slightly circular; "wraps modulo 2^n using two's-complement" would point more precisely at the actual mechanism. Style-only. *(sonnet)*
- **`.gitignore:2-3`** — Adding `.styx/` and `.claude/context.md` is unrelated to the `Square` change. Reasonable to bundle as tooling/generated-state ignores, but worth flagging if this PR is meant to stay scoped to the calc feature. *(sonnet)*

**Overall:** Both reviews converge — the diff is small, correct, and well-tested (`go test ./...` passes per codex), with the boundary-overflow math checking out under independent verification by both reviewers. No functional or security issues found; only minor test-hygiene and scope nits remain.
