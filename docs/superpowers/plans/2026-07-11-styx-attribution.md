# Styx Attribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every commit made by styx-dispatched agents ends with a `Co-Authored-By: styx-thetrickster[bot]` trailer, and every PR body styx creates ends with a "Generated with styx" footer.

**Architecture:** One new dependency-free package, `internal/attribution`, holds three exported constants (trailer line, prompt instruction, PR footer). Three consumers reference them: the auto-pipeline implement prompt (`internal/execute/execute.go`), the PR body builder (`internal/execute/ship.go`), and the conductor's dispatch message decoration (`cmd/styx/mcp_conductor.go`).

**Tech Stack:** Go stdlib only. Table-driven tests with `t.Run`.

**Spec:** `docs/superpowers/specs/2026-07-11-styx-attribution-design.md`

## Global Constraints

- Commit trailer, verbatim: `Co-Authored-By: styx-thetrickster[bot] <302670164+styx-thetrickster[bot]@users.noreply.github.com>`
- PR footer, verbatim (no emoji): `Generated with [styx](https://github.com/ishaanbatra/styx)`
- The identity is NOT configurable — constants only.
- Read-risk dispatches and ollama one-shots never get the trailer instruction (they don't commit).
- Before every commit: `go vet ./... && gofmt -w .`
- Docs drift contract: `docs/ARCHITECTURE.md` owns `internal/**` and `cmd/styx/**` — the new-package section lands in Task 1's commit and already describes all consumers, so Tasks 2–4 stay doc-consistent.

---

### Task 1: `internal/attribution` package

**Files:**
- Create: `internal/attribution/attribution.go`
- Create: `internal/attribution/attribution_test.go`
- Modify: `docs/ARCHITECTURE.md` (insert new section between `## Execute (internal/execute)` and `## Shipgate (internal/shipgate)`, currently lines 1054–1063)
- Modify: `docs/superpowers/specs/2026-07-11-styx-attribution-design.md` (helpers → constants wording)

**Interfaces:**
- Consumes: nothing.
- Produces: `attribution.Trailer`, `attribution.CommitInstruction`, `attribution.PRFooter` — all `const string`. Tasks 2–4 import `github.com/ishaanbatra/styx/internal/attribution`.

- [ ] **Step 1: Write the failing test**

Create `internal/attribution/attribution_test.go`:

```go
package attribution

import (
	"strings"
	"testing"
)

func TestIdentityConstants(t *testing.T) {
	// Values duplicated on purpose: changing the identity must touch the
	// spec (docs/superpowers/specs/2026-07-11-styx-attribution-design.md),
	// the constants, and this test together.
	wantTrailer := "Co-Authored-By: styx-thetrickster[bot] <302670164+styx-thetrickster[bot]@users.noreply.github.com>"
	if Trailer != wantTrailer {
		t.Errorf("Trailer = %q, want %q", Trailer, wantTrailer)
	}
	wantFooter := "Generated with [styx](https://github.com/ishaanbatra/styx)"
	if PRFooter != wantFooter {
		t.Errorf("PRFooter = %q, want %q", PRFooter, wantFooter)
	}
	if !strings.Contains(CommitInstruction, Trailer) {
		t.Error("CommitInstruction must embed Trailer verbatim")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/attribution/ -v`
Expected: FAIL (build error: `undefined: Trailer`)

- [ ] **Step 3: Write the implementation**

Create `internal/attribution/attribution.go`:

```go
// Package attribution defines the identity styx stamps onto work that
// lands in git. Styx never runs `git commit` itself — the agents it
// dispatches do — so commit attribution is an instruction embedded in
// write-capable agent prompts, plus a footer styx appends to PR bodies
// it creates. The email belongs to the styx-thetrickster GitHub App's
// bot user (ID 302670164), so GitHub renders the app's avatar on
// commits and in the Contributors sidebar.
package attribution

// Trailer is the exact Co-Authored-By line agents must end every commit
// message with.
const Trailer = "Co-Authored-By: styx-thetrickster[bot] <302670164+styx-thetrickster[bot]@users.noreply.github.com>"

// CommitInstruction is the sentence write-capable agent prompts embed so
// every commit carries Trailer.
const CommitInstruction = "End every git commit message with this exact trailer line, verbatim, on its own line at the very end: " + Trailer

// PRFooter is appended as its own final paragraph to every PR body styx
// creates.
const PRFooter = "Generated with [styx](https://github.com/ishaanbatra/styx)"
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/attribution/ -v`
Expected: PASS

- [ ] **Step 5: Update docs**

In `docs/ARCHITECTURE.md`, insert this section between the `## Execute (internal/execute)` section and `## Shipgate (internal/shipgate)`:

```markdown
## Attribution (internal/attribution)

The single identity styx stamps onto work that lands in git, as three
constants: `Trailer` (the `Co-Authored-By: styx-thetrickster[bot] <…>`
line — the styx GitHub App's bot user, so commits and the Contributors
sidebar render the styx logo avatar), `CommitInstruction` (the sentence
embedded in write-capable agent prompts so agents end every commit with
the trailer), and `PRFooter` (the "Generated with styx" link appended to
PR bodies). Three consumers: `execute.buildPrompt` (auto-pipeline
implementers), `execute.Ship` via `prBody` (PR bodies, default and
caller-supplied), and the conductor's `dispatch`/`dispatch_parallel`
via `attributedMessage` (edit/ship-risk messages; read-risk dispatches
and ollama one-shots pass through untouched).
```

In `docs/superpowers/specs/2026-07-11-styx-attribution-design.md`, replace:

```markdown
New package `internal/attribution` with three exported helpers and no
dependencies:

- `Trailer() string` — the `Co-Authored-By` line above.
- `CommitInstruction() string` — one sentence telling an agent to end
  every commit message with that exact trailer.
- `PRFooter() string` — the footer line above.
```

with:

```markdown
New package `internal/attribution` with three exported constants and no
dependencies:

- `Trailer` — the `Co-Authored-By` line above.
- `CommitInstruction` — one sentence telling an agent to end every
  commit message with that exact trailer.
- `PRFooter` — the footer line above.
```

- [ ] **Step 6: Vet, format, commit**

```bash
go vet ./... && gofmt -w .
git add internal/attribution/ docs/ARCHITECTURE.md docs/superpowers/specs/2026-07-11-styx-attribution-design.md
git commit -m "feat(attribution): styx identity constants — trailer, prompt instruction, PR footer"
```

---

### Task 2: Auto-pipeline implement prompt carries the trailer instruction

**Files:**
- Modify: `internal/execute/execute.go` (`buildPrompt`, lines 97–101)
- Test: `internal/execute/execute_test.go`

**Interfaces:**
- Consumes: `attribution.CommitInstruction` (Task 1).
- Produces: no new API — `buildPrompt(plan string) string` output now embeds the instruction.

- [ ] **Step 1: Write the failing test**

Add to `internal/execute/execute_test.go` (add `"github.com/ishaanbatra/styx/internal/attribution"` and `"strings"` to imports if absent):

```go
func TestBuildPromptIncludesAttribution(t *testing.T) {
	p := buildPrompt("PLAN BODY")
	if !strings.Contains(p, attribution.CommitInstruction) {
		t.Error("buildPrompt missing attribution.CommitInstruction")
	}
	if !strings.Contains(p, "PLAN BODY") {
		t.Error("buildPrompt missing plan content")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/execute/ -run TestBuildPromptIncludesAttribution -v`
Expected: FAIL ("buildPrompt missing attribution.CommitInstruction")

- [ ] **Step 3: Write the implementation**

In `internal/execute/execute.go`, add `"github.com/ishaanbatra/styx/internal/attribution"` to imports and replace `buildPrompt`:

```go
func buildPrompt(plan string) string {
	return "Please implement this plan autonomously. Your project context is in .claude/context.md. " +
		"Make all required code edits. Run any commands needed. Commit your work as you go using small, " +
		"descriptive commits. " + attribution.CommitInstruction + " " +
		"When done, report what you did.\n\n--- PLAN ---\n" + plan
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/execute/ -v`
Expected: PASS (all tests, not just the new one)

- [ ] **Step 5: Vet, format, commit**

```bash
go vet ./... && gofmt -w .
git add internal/execute/execute.go internal/execute/execute_test.go
git commit -m "feat(execute): implement prompt instructs agents to co-author commits as styx"
```

---

### Task 3: PR bodies end with the styx footer

**Files:**
- Modify: `internal/execute/ship.go` (extract `prBody`, use it in `Ship`)
- Test: `internal/execute/execute_test.go`

**Interfaces:**
- Consumes: `attribution.PRFooter` (Task 1).
- Produces: unexported `prBody(o ShipOptions) string` — Ship's PR body builder. Note the old default body line "Generated by styx auto." is intentionally dropped; the footer replaces it.

- [ ] **Step 1: Write the failing test**

Add to `internal/execute/execute_test.go`:

```go
func TestPRBody(t *testing.T) {
	tests := []struct {
		name string
		opts ShipOptions
		want string
	}{
		{
			name: "default body gets goal plus footer",
			opts: ShipOptions{Goal: "add attribution"},
			want: "Goal: add attribution\n\n" + attribution.PRFooter,
		},
		{
			name: "custom body keeps content, gains footer",
			opts: ShipOptions{PRBody: "Custom summary.\n"},
			want: "Custom summary.\n\n" + attribution.PRFooter,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prBody(tt.opts); got != tt.want {
				t.Errorf("prBody() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/execute/ -run TestPRBody -v`
Expected: FAIL (build error: `undefined: prBody`)

- [ ] **Step 3: Write the implementation**

In `internal/execute/ship.go`, add `"github.com/ishaanbatra/styx/internal/attribution"` to imports. Replace the body-building lines inside `Ship`:

```go
	body := o.PRBody
	if body == "" {
		body = "Generated by styx auto.\n\nGoal: " + o.Goal
	}
	prCmd := exec.CommandContext(ctx, "gh", "pr", "create", "--fill", "--body", body)
```

with:

```go
	prCmd := exec.CommandContext(ctx, "gh", "pr", "create", "--fill", "--body", prBody(o))
```

and add below `Ship`:

```go
// prBody builds the PR body: the caller's PRBody (or a goal-line default)
// with the styx attribution footer as its own final paragraph.
func prBody(o ShipOptions) string {
	body := o.PRBody
	if body == "" {
		body = "Goal: " + o.Goal
	}
	return strings.TrimRight(body, "\n") + "\n\n" + attribution.PRFooter
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/execute/ -v`
Expected: PASS (including the existing `TestShip_*` tests — the fake-git harness is unaffected)

- [ ] **Step 5: Vet, format, commit**

```bash
go vet ./... && gofmt -w .
git add internal/execute/ship.go internal/execute/execute_test.go
git commit -m "feat(execute): PR bodies end with the styx attribution footer"
```

---

### Task 4: Conductor dispatch decorates write-risk messages

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (add `attributedMessage`; wire into the `dispatch` and `dispatch_parallel` handlers' `agent.DispatchSpec` construction)
- Test: `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Consumes: `attribution.CommitInstruction` (Task 1).
- Produces: unexported `attributedMessage(msg, risk string) string`.

- [ ] **Step 1: Write the failing test**

Add to `cmd/styx/mcp_conductor_test.go` (ensure `"strings"` and `"github.com/ishaanbatra/styx/internal/attribution"` are imported):

```go
func TestAttributedMessage(t *testing.T) {
	tests := []struct {
		name, msg, risk string
		wantDecorated   bool
	}{
		{"read untouched", "summarize the router", "read", false},
		{"edit decorated", "fix the loader", "edit", true},
		{"ship decorated", "ship the branch", "ship", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attributedMessage(tt.msg, tt.risk)
			decorated := strings.HasSuffix(got, attribution.CommitInstruction)
			if decorated != tt.wantDecorated {
				t.Errorf("attributedMessage(%q, %q): decorated = %v, want %v",
					tt.msg, tt.risk, decorated, tt.wantDecorated)
			}
			if !strings.HasPrefix(got, tt.msg) {
				t.Errorf("original message must be preserved as prefix, got %q", got)
			}
			if !tt.wantDecorated && got != tt.msg {
				t.Errorf("read-risk message must pass through untouched, got %q", got)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestAttributedMessage -v`
Expected: FAIL (build error: `undefined: attributedMessage`)

- [ ] **Step 3: Write the implementation**

In `cmd/styx/mcp_conductor.go`, add `"github.com/ishaanbatra/styx/internal/attribution"` to imports. Add near `dispatchArgs` (around line 444):

```go
// attributedMessage appends the styx commit-trailer instruction to
// write-capable dispatch messages so agent commits credit styx.
// Read-risk agents never commit; their messages pass through untouched.
// (Ollama one-shots never route through here — they don't commit.)
func attributedMessage(msg, risk string) string {
	if risk == "read" {
		return msg
	}
	return msg + "\n\n" + attribution.CommitInstruction
}
```

Wire it into the `dispatch` handler's spec (currently ~line 569 — this is the thread path, after the ollama one-shot early return, so ollama is naturally excluded):

```go
				spec := agent.DispatchSpec{
					Thread: in.Thread, CLI: in.CLI, Model: model,
					Message: attributedMessage(in.Message, in.Risk), ExtraRoots: in.ExtraRoots,
					ReadOnly: in.Risk == "read",
				}
```

And into the `dispatch_parallel` handler's spec (currently ~line 934):

```go
					spec := agent.DispatchSpec{
						Thread: tin.Thread, CLI: tin.CLI, Model: model,
						Message: attributedMessage(tin.Message, tin.Risk), ExtraRoots: tin.ExtraRoots,
						ReadOnly: tin.Risk == "read",
					}
```

Leave `meta.Signals: dispatchSignals(in.Message)` / `dispatchSignals(tin.Message)` on the ORIGINAL message in both handlers — signal tagging must not see the injected instruction.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/styx/ -v -run 'TestAttributedMessage|TestConductor'`
Expected: PASS

- [ ] **Step 5: Full test sweep, vet, format, commit**

```bash
go vet ./... && gofmt -w . && go test ./...
git add cmd/styx/mcp_conductor.go cmd/styx/mcp_conductor_test.go
git commit -m "feat(conductor): dispatch decorates edit/ship-risk messages with styx co-author trailer"
```

---

### Final verification (after all tasks)

- [ ] `make build && make test` — green.
- [ ] `make e2e` — the build-tagged e2e package is skipped by `go test ./...`; run it before calling the work done.
- [ ] Live check once a styx-dispatched commit lands on GitHub: the commit shows the styx-thetrickster logo avatar (a gray silhouette means the email has a typo).
