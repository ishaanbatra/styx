# Styx attribution on commits and PRs

Date: 2026-07-11
Status: approved

## Goal

Work that lands in git via styx should credit styx, the way Claude Code
credits itself: a `Co-Authored-By` trailer on commits and a "Generated
with" footer on PR bodies.

## Identity

Styx never runs `git commit` itself — the agents it dispatches do. So
attribution is an instruction to those agents, plus a footer styx writes
into PR bodies directly:

- Commit trailer (exact line, verbatim):
  `Co-Authored-By: styx-thetrickster[bot] <302670164+styx-thetrickster[bot]@users.noreply.github.com>`
- PR footer (own paragraph at the end of the body):
  `Generated with [styx](https://github.com/ishaanbatra/styx)`

The identity is the bot user of the `styx-thetrickster` GitHub App
(App ID 4275975, owned by @ishaanbatra; bot user ID 302670164). GitHub
matches the trailer email to that bot, so commits and the Contributors
sidebar render the app's avatar (the styx logo) — the same mechanism
behind `dependabot[bot]`. The app holds no permissions and is never
installed; it exists only so the identity does. Not configurable — it is
one constant, trivially editable later if that changes.

## Design

New package `internal/attribution` with three exported constants and no
dependencies:

- `Trailer` — the `Co-Authored-By` line above.
- `CommitInstruction` — one sentence telling an agent to end every
  commit message with that exact trailer.
- `PRFooter` — the footer line above.

Three consumers:

1. `internal/execute/execute.go` — `buildPrompt` appends
   `CommitInstruction()` to the "Commit your work as you go" implement
   prompt, so `styx auto` executors add the trailer.
2. `internal/execute/ship.go` — `Ship` appends `PRFooter()` (blank line +
   footer) to the PR body: both the default body and a caller-supplied
   `PRBody`.
3. `cmd/styx/mcp_conductor.go` — `dispatch` and `dispatch_parallel`
   append `CommitInstruction()` to the outgoing message when risk is
   `edit` or `ship`. Read-risk dispatches never commit and stay
   untouched.

Rejected alternatives: inlining the strings at each site (three copies
drift), and a `routing.toml` knob for the identity (YAGNI).

## Testing

Table-driven, per repo standards:

- `buildPrompt` output contains the trailer instruction.
- `Ship` produces a body ending in the footer for both default and
  custom `PRBody` (existing fake-git harness in `execute_test.go`).
- Conductor: an edit-risk dispatch message is decorated with the commit
  instruction; a read-risk one is not.

## Docs

`docs/ARCHITECTURE.md` (owner of `internal/**` and `cmd/styx/**`) gains
the `internal/attribution` package in the same commit, `last_verified`
bumped, per the drift contract.
