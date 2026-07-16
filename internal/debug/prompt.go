package debug

import (
	"fmt"
	"strings"
)

func sweepPrompt(in Input) string {
	var b strings.Builder
	b.WriteString(`You are a senior debugging engineer with full read access to this repository.
Investigate the bug below. Read any file; trace call paths across the whole
codebase; reproduce if you can. DO NOT edit files, run destructive commands, or
propose a patch you have not traced to specific lines — this is a diagnosis pass.

Produce a DEBUG BRIEF in markdown with EXACTLY these sections:

## Symptom
Restate the bug in one paragraph. State how to reproduce, or say "could not
reproduce" explicitly — never guess a repro.

## Evidence
Bullet list. Each item MUST cite a file:line you actually read, with a one-line
note on what that code does and why it is relevant.

## Hypotheses
Ranked most-to-least likely root cause. For each: the mechanism, the file:line it
lives at, and the evidence that supports or contradicts it.

## Suggested minimal fix
The smallest change that would address the top hypothesis — DESCRIBED, not applied,
with the target file:line.

## Open questions
What you could not determine and what a reviewer should double-check.

Be concrete. Prefer file:line citations over prose.

--- BUG ---
`)
	b.WriteString(in.Bug)
	if in.TestName != "" {
		b.WriteString("\n--- FAILING TEST ---\n")
		b.WriteString(in.TestName)
	}
	if in.LogBody != "" {
		b.WriteString("\n--- LOG / STACK (truncated) ---\n")
		b.WriteString(in.LogBody)
	}
	if len(in.FileHints) > 0 {
		b.WriteString("\n--- START HERE (caller hints) ---\n")
		b.WriteString(strings.Join(in.FileHints, "\n"))
	}
	return b.String()
}

func reviewPromptMisread(brief string) string {
	return fmt.Sprintf(`You are reviewing another engineer's DEBUG BRIEF (below). You did NOT re-run the
investigation — judge the brief against the code it cites. ONE short pass:

- Confirm or challenge the TOP-RANKED hypothesis: is the cited file:line evidence
  real and sufficient, or did the author MISREAD it?
- Flag any evidence item that is wrong, out of date, or over-interpreted.
- Note any more-likely root cause the brief overlooked.

Return ONLY this JSON, nothing else:
{"blocking":["..."],"important":["..."],"nits":["..."]}

BLOCKING  = the top hypothesis is wrong or unsupported by the cited code.
IMPORTANT = a real gap that weakens the diagnosis but does not refute it.
NIT       = minor.

--- DEBUG BRIEF ---
%s`, brief)
}

func reviewPromptRootCause(brief string) string {
	return fmt.Sprintf(`You are independently reviewing a DEBUG BRIEF (below). Another reviewer is checking
for misreads; your lens is different: is the TOP hypothesis the ACTUAL root cause,
or a plausible symptom of something deeper? ONE short pass:

- Would the "Suggested minimal fix" actually resolve the symptom, or just mask it?
- Is there a simpler/more fundamental cause consistent with the same evidence?
- What concrete input or state would reproduce (or falsify) the top hypothesis?

Return ONLY this JSON, nothing else:
{"blocking":["..."],"important":["..."],"nits":["..."]}

BLOCKING  = top hypothesis is not the real root cause, or the fix would not resolve it.
IMPORTANT = a real gap that weakens the diagnosis but does not refute it.
NIT       = minor.

--- DEBUG BRIEF ---
%s`, brief)
}
