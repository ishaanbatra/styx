# Styx Phase A — Reliability & Drift Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a naive MCP conductor's first contact with every styx tool succeed, fix the stale channel-capability assumptions that silently destroy dispatch quality, and add a permanent E2E harness.

**Architecture:** All changes stay inside existing seams: conductor tool handlers (`cmd/styx/mcp_conductor.go`), agent adapters (`internal/agent/adapter.go`), the transport-only MCP server (`internal/mcpserver`), and the ollama HTTP callers. No new packages except `e2e/`. Spec: `docs/superpowers/specs/2026-07-06-styx-improvement-roadmap-design.md`.

**Tech Stack:** Go 1.x, stdlib only (no new deps), `testdata/fakeagent` bash fixture, httptest for ollama.

## Global Constraints

- Pure Go, no cgo, no provider SDKs — channels shell out to CLIs or speak local HTTP.
- Never swallow errors: wrap with context (`fmt.Errorf("load registry: %w", err)`).
- Status to stderr via `logStatus` (respects `--quiet`); stdout is results/protocol only. In `styx mcp`, stdout carries JSON-RPC exclusively.
- All file writes atomic (tmp + rename).
- Never break `styx auto --resume` (no pipeline state changes in this plan — keep it that way).
- **Drift contract:** every task that changes behavior described in `docs/ARCHITECTURE.md` updates that doc **in the same commit** and bumps its `last_verified` date (currently a `2026-07-02` line in its frontmatter → set to the commit date). The commit steps below name the sections to edit.
- Before every commit: `go vet ./... && gofmt -w .`
- Run `make test` (full suite) before each commit, not just the new tests.

---

### Task 1: `styx version` verb + non-TTY conductor guard

Today `styx version` / `styx --version` fall through to the conductor launch path and die inside claude with a cryptic `--print` error. Also, bare `styx` on a non-TTY stdin execs claude into the same confusing failure.

**Files:**
- Modify: `cmd/styx/main.go` (add `styxVersion` const)
- Modify: `cmd/styx/dispatch.go` (verb switch, first tier — near the `case "help", "-h", "--help":` arm at ~line 260)
- Modify: `cmd/styx/launch.go` (TTY guard in `launchConductor`)
- Test: `cmd/styx/launch_test.go` (create if absent)
- Modify: `README.md` (verb table), `cmd/styx/help.go` or wherever the help text lives (`grep -rn '"help"' cmd/styx` shows the printer — add a `version` row)

**Interfaces:**
- Consumes: `logStatus` (existing), `launcher.ClaudeHost` (existing).
- Produces: `const styxVersion string` in `main.go`; `func stdinIsTTY() bool` var-swappable in `launch.go` (Task 12's E2E asserts `styx version` output).

- [x] **Step 1: Write the failing test**

```go
// cmd/styx/launch_test.go
package main

import (
	"strings"
	"testing"
)

func TestEnsureInteractiveTTY(t *testing.T) {
	orig := stdinIsTTY
	defer func() { stdinIsTTY = orig }()

	stdinIsTTY = func() bool { return false }
	err := ensureInteractiveTTY()
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("non-TTY stdin must refuse conductor launch, got %v", err)
	}

	stdinIsTTY = func() bool { return true }
	if err := ensureInteractiveTTY(); err != nil {
		t.Fatalf("TTY stdin must pass, got %v", err)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestEnsureInteractiveTTY -v`
Expected: FAIL — `undefined: stdinIsTTY` / `undefined: ensureInteractiveTTY`

- [x] **Step 3: Implement**

In `cmd/styx/main.go`, top-level (near the package comment):

```go
// styxVersion is printed by `styx version`. Bump on tagged releases.
const styxVersion = "0.4.0-dev"
```

In `cmd/styx/dispatch.go`, add to the FIRST-tier verb switch (verbs that don't need the app), alongside `case "help", "-h", "--help":`:

```go
case "version", "--version", "-V":
	fmt.Println("styx " + styxVersion)
	return nil
```

(Match the surrounding arms' return style — if the switch arms call a `cmdX` func and return its error, inline the two lines the same way `help` does.)

In `cmd/styx/launch.go`:

```go
// stdinIsTTY reports whether stdin is a character device. Var for tests.
var stdinIsTTY = func() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ensureInteractiveTTY refuses a conductor launch when there is no terminal
// to hand to Claude Code — exec'ing claude on a pipe dies with a confusing
// "--print requires input" error instead of anything actionable.
func ensureInteractiveTTY() error {
	if stdinIsTTY() {
		return nil
	}
	return fmt.Errorf("the conductor needs an interactive terminal (stdin is not a TTY); use a verb like `styx research` or `styx \"<task>\"` for scripted runs")
}
```

At the top of `launchConductor` (before `resolveLaunchTarget`):

```go
if err := ensureInteractiveTTY(); err != nil {
	return err
}
```

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestEnsureInteractiveTTY -v` → PASS
Run: `make build && ./bin/styx version` → prints `styx 0.4.0-dev`
Run: `echo "" | ./bin/styx` → clear TTY error, exit 1 (not a claude stack trace)
Run: `make test` → all green

- [x] **Step 5: Update docs + commit**

- `README.md`: add `version` row to the verb table.
- Help text: add `version` line.
- `docs/ARCHITECTURE.md` "cmd/styx — verbs and app wiring" section: mention the `version` verb and the launcher TTY guard; bump `last_verified: 2026-07-06`.

```bash
go vet ./... && gofmt -w .
git add -A
git commit -m "feat(cli): styx version verb + refuse conductor launch without a TTY"
```

---

### Task 2: `thread_status` returns `[]`, never `null`

**Files:**
- Modify: `internal/agent/manager.go:202-219` (`StatusLines`)
- Test: `internal/agent/manager_test.go`

**Interfaces:**
- Produces: `Manager.StatusLines() []string` guaranteed non-nil (Task 10 and the E2E rely on `{"threads": []}` JSON shape).

- [x] **Step 1: Write the failing test**

Add to `internal/agent/manager_test.go`:

```go
func TestStatusLinesEmptyIsNotNil(t *testing.T) {
	m := &Manager{Threads: &ThreadStore{Threads: map[string]*Thread{}}}
	got := m.StatusLines()
	if got == nil {
		t.Fatal("StatusLines() with no threads must return [], not nil (JSON null breaks MCP consumers)")
	}
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %v", got)
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestStatusLinesEmptyIsNotNil -v`
Expected: FAIL — `StatusLines() with no threads must return [], not nil`

- [x] **Step 3: Implement**

In `StatusLines`, change `var out []string` to:

```go
out := []string{}
```

- [x] **Step 4: Run tests**

Run: `go test ./internal/agent/ -v` → PASS (all)

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Conductor MCP tools" `thread_status` bullet: note the guaranteed-`[]` shape.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "fix(mcp): thread_status returns [] instead of null when a project has no threads"
```

---

### Task 3: `dispatch(cli=ollama)` defaults its model

E2E evidence: omitting `model` today → `ollama 400: model is required`.

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (dispatch handler, ollama branch ~line 187; schema description ~line 155)
- Test: `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Consumes: `d.a.routing.Brain.Model` (defaulted to `"qwen2.5-coder:7b"` by `applyBrainDefaults` in `internal/config/routing.go`), `channel.Channel` interface.
- Produces: dispatch(cli=ollama) usable with no `model` argument.

- [x] **Step 1: Write the failing test**

Add to `cmd/styx/mcp_conductor_test.go` (note: `callTool` helper and imports for `budget`, `channel`, `config` already exist in this file):

```go
// captureChannel records the last Request and returns a canned response.
type captureChannel struct{ last channel.Request }

func (c *captureChannel) Name() string { return "ollama" }
func (c *captureChannel) Send(_ context.Context, req channel.Request) (channel.Response, error) {
	c.last = req
	return channel.Response{Text: "pong", EstTokensIn: 3, EstTokensOut: 1}, nil
}
func (c *captureChannel) BudgetState(_ context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

func TestDispatchOllamaDefaultsModel(t *testing.T) {
	tr, err := budget.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cap := &captureChannel{}
	d := &conductorDeps{
		gate: shipgate.New(shipgate.ModeOff),
		a: &app{
			channels: map[string]channel.Channel{"ollama": cap},
			tracker:  tr,
			routing:  config.Routing{Brain: config.BrainConfig{Model: "qwen2.5-coder:7b"}},
		},
	}
	res, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "ollama", "message": "say pong", "risk": "read",
	})
	if err != nil {
		t.Fatalf("ollama dispatch without model must succeed: %v", err)
	}
	if cap.last.Model != "qwen2.5-coder:7b" {
		t.Fatalf("model must default to routing Brain.Model, got %q", cap.last.Model)
	}
	if res["text"] != "pong" {
		t.Fatalf("want text pong, got %v", res["text"])
	}
}
```

(If `config.BrainConfig` is named differently, check `internal/config/routing.go:40` — the struct holding `Model`/`EmbedModel`/`ContextThresholdPct` — and use its real name. If `budget.Open` has a different constructor name, mirror whatever `cmd/styx/mcp_test.go` uses to build a test tracker.)

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestDispatchOllamaDefaultsModel -v`
Expected: FAIL — error `ollama dispatch: ollama 400 ...` is NOT returned (fake channel accepts), so failure mode is `model must default to routing Brain.Model, got ""`

- [x] **Step 3: Implement**

In the dispatch handler's ollama branch (`cmd/styx/mcp_conductor.go` ~line 187), before `ch.Send`:

```go
if in.CLI == "ollama" { // one-shot, no thread machinery
	ch, ok := d.a.channels["ollama"]
	if !ok {
		return nil, fmt.Errorf("dispatch ollama: ollama channel unavailable")
	}
	model := in.Model
	if model == "" {
		model = d.a.routing.Brain.Model // seeded default: qwen2.5-coder:7b
	}
	if model == "" {
		model = "qwen2.5-coder:7b"
	}
	resp, err := ch.Send(ctx, channel.Request{
		Model: model, Prompt: in.Message,
	})
```

…and use `model` (not `in.Model`) in the `budget.Event{... Model: model ...}` record below it.

Update the schema description for `model` (~line 155):

```go
"model": map[string]any{"type": "string", "description": "tier (opus|sonnet|haiku) or raw model id; empty = channel default (ollama defaults to the routing brain model)"},
```

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestDispatch -v` → PASS (including existing gate/validation tests)

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Conductor MCP tools" `dispatch` bullet: note the ollama model default.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "fix(mcp): dispatch cli=ollama defaults model to the routing brain model"
```

---

### Task 4: empty `project` resolves to the server's cwd project; errors list the registry

E2E evidence: `dispatch` without `project` → `resolve project: no target: name a project (--project), pass --dir, or cd into a repo` — shell advice an MCP client can't follow. `pipeline_run` already resolves via cwd; `dispatch`/`thread_status`/`memory_save` must match. Named-but-unknown aliases stay loud errors, now listing registered names.

**Files:**
- Modify: `cmd/styx/mcp_conductor.go:63-69` (`managerFor`), schema descriptions
- Test: `cmd/styx/mcp_conductor_test.go`
- Modify: `docs/ARCHITECTURE.md` (this changes a documented contract)

**Interfaces:**
- Consumes: `resolveGlobalTarget(arg string) (project.Project, error)` (`cmd/styx/dispatch.go:42`), `config.LoadProjects() ([]Project, error)`, `target.Resolve`.
- Produces: `managerFor("")` = launch-directory project; `registeredProjectNames() string` helper.

- [x] **Step 1: Write the failing tests**

Add to `cmd/styx/mcp_conductor_test.go`:

```go
func TestManagerForEmptyAliasUsesCwd(t *testing.T) {
	// Isolated config home; cwd (this repo) auto-registers on resolution.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &conductorDeps{managers: map[string]*managed{}}
	// managerFor needs deps only past resolution; a nil app panics later,
	// so resolve-only is asserted through the error path shape:
	// build minimal deps the way managerForProject needs them.
	tr, err := budget.Open(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	d.a = &app{tracker: tr, routing: config.Routing{}}
	m, err := d.managerFor("")
	if err != nil {
		t.Fatalf("empty alias must resolve via cwd (launch dir), got %v", err)
	}
	if m.mgr.Project.Name == "" {
		t.Fatal("resolved project must be the cwd repo")
	}
}
```

(Caution: `managerForProject` calls `rawChannel(d.a.channels["ollama"])` for the Summarize closure — if `rawChannel` panics on a nil channel, give the test app `channels: map[string]channel.Channel{"ollama": &captureChannel{}}` reusing Task 3's stub. The closure is lazy, so a stub that's never Sent is fine.)

```go

func TestManagerForUnknownAliasListsRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &conductorDeps{managers: map[string]*managed{}}
	_, err := d.managerFor("definitely-not-registered")
	if err == nil {
		t.Fatal("unknown alias must stay a loud error")
	}
	if !strings.Contains(err.Error(), "registered projects") {
		t.Fatalf("error must list registered projects for the MCP consumer, got: %v", err)
	}
}
```

- [x] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run TestManagerFor -v`
Expected: `TestManagerForEmptyAliasUsesCwd` FAILs with the old "no target: name a project (--project)…" error; `TestManagerForUnknownAliasListsRegistry` FAILs on the missing "registered projects" text.

- [x] **Step 3: Implement**

Replace `managerFor` in `cmd/styx/mcp_conductor.go`:

```go
// managerFor lazily binds a project. An empty alias resolves to the server's
// cwd project — the launcher starts `styx mcp` in the launch directory, so
// cwd IS the caller's project for the conductor (same rule pipeline_run
// already uses). A named alias resolves strictly (no fallback); resolution
// failures list the registry so an MCP consumer can self-correct — it cannot
// "pass --dir" or "cd into a repo".
func (d *conductorDeps) managerFor(alias string) (*managed, error) {
	var p project.Project
	var err error
	if alias == "" {
		p, err = resolveGlobalTarget("")
	} else {
		p, err = target.Resolve(target.Spec{Alias: alias})
	}
	if err != nil {
		return nil, fmt.Errorf("resolve project %q: %w (registered projects: %s)",
			alias, err, registeredProjectNames())
	}
	return d.managerForProject(p)
}

// registeredProjectNames renders the registry for MCP error messages.
func registeredProjectNames() string {
	projs, err := config.LoadProjects()
	if err != nil || len(projs) == 0 {
		return "none"
	}
	names := make([]string, len(projs))
	for i, p := range projs {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}
```

Add `"strings"` and the `config` import to the file's import block if not present (`config` is `github.com/ishaanbatra/styx/internal/config`).

Update the `project` schema descriptions on all three tools:

```go
"project": map[string]any{"type": "string", "description": "registered project alias; empty = the project styx was launched in"},
```

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestManagerFor -v` → PASS
Run: `go test ./cmd/styx/ -v` → existing `TestThreadStatusNoThreads` may now expect the new error text — update its assertion to match `registered projects` if it asserted the old wording.

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md`: update the `managerFor` bullet in "Conductor MCP tools" AND the "no cwd fallback" sentences in "MCP server" — the revised contract is: *strict resolution for named projects; empty = launch directory project (conductor case), matching pipeline_run*. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "fix(mcp): empty project resolves to the launch-dir project; errors list the registry"
```

---

### Task 5: launch guidance names the focus project alias

**Files:**
- Modify: `cmd/styx/launch.go` (`launchConductor` ~line 66-75)
- Test: `cmd/styx/launch_test.go`

**Interfaces:**
- Produces: `func conductorGuidance(base string, focusName string, extraNote, prefs string) string` — pure assembly, testable without exec.

- [x] **Step 1: Write the failing test**

Add to `cmd/styx/launch_test.go`:

```go
func TestConductorGuidanceNamesFocusProject(t *testing.T) {
	got := conductorGuidance("BASE", "styx", "", "")
	if !strings.Contains(got, "`styx`") || !strings.Contains(got, "project") {
		t.Fatalf("guidance must name the focus project alias, got:\n%s", got)
	}
	if !strings.HasPrefix(got, "BASE") {
		t.Fatal("base guidance must come first")
	}
	withExtras := conductorGuidance("BASE", "styx", "- ai-ta: /x (extra)\n", "- prefer codex\n")
	for _, want := range []string{"Bound repos beyond styx", "Routing preferences", "prefer codex"} {
		if !strings.Contains(withExtras, want) {
			t.Fatalf("missing %q in:\n%s", want, withExtras)
		}
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestConductorGuidance -v`
Expected: FAIL — `undefined: conductorGuidance`

- [x] **Step 3: Implement**

In `cmd/styx/launch.go`, extract the assembly from `launchConductor`:

```go
// conductorGuidance assembles the final --append-system-prompt content:
// base guidance, the focus project's registry alias (so the brain knows what
// to pass as `project` on dispatch/thread_status/memory_save), extra-repo
// notes, and learned routing preferences.
func conductorGuidance(base, focusName, extraNote, prefs string) string {
	g := base
	g += "\n\n## This session's project\n" +
		"Registry alias: `" + focusName + "`. Pass it as `project` on dispatch/" +
		"thread_status/memory_save (an empty project also resolves to this repo)."
	if extraNote != "" {
		g += "\n\n## Bound repos beyond " + focusName + "\n" + extraNote
	}
	if prefs != "" {
		g += "\n\n## Routing preferences (learned)\n" + prefs
	}
	return g
}
```

In `launchConductor`, replace the inline `guide += …` blocks:

```go
guide, err := guidance.Load(p.Path)
if err != nil {
	return fmt.Errorf("load guidance: %w", err)
}
guide = conductorGuidance(guide, p.Name, extraNote.String(), recallRoutingPrefs(a))
```

(Delete the now-redundant `if extraNote.Len() > 0` and `if prefs := …` blocks.)

- [x] **Step 4: Run tests**

Run: `go test ./cmd/styx/ -run TestConductorGuidance -v` → PASS
Run: `make test` → green

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Launcher" / conductor-data-flow paragraph: guidance now includes the focus alias. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(launch): guidance names the focus project alias for the conductor brain"
```

---

### Task 6: honest claude context window (1M, env opt-out)

E2E evidence: "context 24%" after one trivial turn → distill-and-restart fires at ~14% of the real window. Opus 4.8 / Sonnet 5 / Fable 5 run 1M-token windows on the API and Max plans; `CLAUDE_CODE_DISABLE_1M_CONTEXT=1` opts out.

**Files:**
- Modify: `internal/agent/adapter.go:39-44` (`ClaudeAdapter.ContextWindow`)
- Test: `internal/agent/adapter_test.go`

**Interfaces:**
- Produces: `ClaudeAdapter.ContextWindow()` = 1_000_000 default, 200_000 when `CLAUDE_CODE_DISABLE_1M_CONTEXT=1`, `Window` field still overrides for tests.

- [x] **Step 1: Update the failing test**

`internal/agent/adapter_test.go:51-52` currently asserts 200000. Replace with:

```go
func TestClaudeContextWindow(t *testing.T) {
	a := NewClaudeAdapter()
	if a.ContextWindow() != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000 (opus/sonnet/fable are 1M-class)", a.ContextWindow())
	}
	t.Setenv("CLAUDE_CODE_DISABLE_1M_CONTEXT", "1")
	if a.ContextWindow() != 200000 {
		t.Errorf("with 1M disabled, ContextWindow = %d, want 200000", a.ContextWindow())
	}
	b := &ClaudeAdapter{Window: 12345}
	if b.ContextWindow() != 12345 {
		t.Errorf("explicit Window override must win, got %d", b.ContextWindow())
	}
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestClaudeContextWindow -v`
Expected: FAIL — `ContextWindow = 200000, want 1000000`

- [x] **Step 3: Implement**

```go
func (a *ClaudeAdapter) ContextWindow() int {
	if a.Window > 0 {
		return a.Window
	}
	// Opus 4.8 / Sonnet 5 / Fable 5 run the 1M window on the Anthropic API
	// and Max plans; honor Claude Code's own opt-out env. Haiku threads
	// (rare) over-estimate their window — acceptable: distill still fires,
	// just later; claude's own compaction is the backstop.
	if os.Getenv("CLAUDE_CODE_DISABLE_1M_CONTEXT") == "1" {
		return 200000
	}
	return 1000000
}
```

Add `"os"` to the imports.

- [x] **Step 4: Run tests**

Run: `go test ./internal/agent/ -v` → PASS. If `manager_test.go` fixtures relied on the 200k default to trigger distill, they set `Window` explicitly (they use `ThresholdPct: 70` with fake adapters) — fix any that break by setting `Window` on the fixture adapter.

- [x] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Agent threads" section: replace "a 200k token context window" with the 1M/env-opt-out rule. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "fix(agent): claude threads meter against the real 1M context window"
```

---

### Task 7: `--bare` on dispatched claude turns

E2E evidence: a "pong" dispatch = 48,551 input tokens of session bootstrap (hooks, CLAUDE.md, skills, MCP autodiscovery — including styx's own MCP server, recursively). `--bare` skips all of it; the conductor session, not the dispatched thread, owns that context. The thread seed line must stop claiming `.claude/context.md` auto-loads (it no longer does under `--bare`).

**Files:**
- Modify: `internal/agent/adapter.go:52-66` (`claudeArgs`)
- Modify: `internal/agent/manager.go:100-103` (`seedMessage` role line)
- Test: `internal/agent/adapter_test.go`

**Interfaces:**
- Consumes: claude CLI ≥ 2.1.x `--bare` flag (verified present in installed 2.1.201 via `claude --help`).
- Produces: every headless dispatched claude turn runs `--bare`. Interactive `Handoff` (manager.go:226) builds its own args and stays un-bare — do not touch it.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/adapter_test.go`:

```go
func TestClaudeArgsBare(t *testing.T) {
	args := claudeArgs("sess-1", "sonnet", "do it", nil, false)
	found := false
	for _, a := range args {
		if a == "--bare" {
			found = true
		}
	}
	if !found {
		t.Fatalf("headless dispatch must pass --bare (skip hooks/CLAUDE.md/MCP bootstrap), got %v", args)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestClaudeArgsBare -v`
Expected: FAIL — `must pass --bare`

- [ ] **Step 3: Implement**

In `claudeArgs`:

```go
args = append(args, "-p", msg, "--output-format", "stream-json", "--verbose", "--bare")
```

In `manager.go` `seedMessage`, replace the fresh-thread role line:

```go
parts = append(parts, fmt.Sprintf(
	"You are the long-running %q agent thread of styx for project %s. You run bare — no hooks or auto-loaded project files — so rely on the message content and the files on disk in the working directory.",
	th.Name, m.Project.Name))
```

- [ ] **Step 4: Run tests + live verification**

Run: `go test ./internal/agent/ -v` → PASS (existing `claudeArgs` assertions in adapter_test.go may enumerate exact args — update them to include `--bare`).
Live check (one cheap real call):

```bash
claude --bare -p 'reply with exactly: ok' --output-format stream-json --verbose | tail -1
```

Expected: a `result` line whose `usage.input_tokens + cache_*` total is a small fraction of ~48k. If `--bare` is rejected by the installed CLI, STOP and flag — do not ship this task.

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md` "Agent threads": document `--bare` and the new seed wording. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(agent): dispatched claude turns run --bare, cutting ~48k bootstrap tokens per turn"
```

---

### Task 8: codex becomes a resume-capable, stream-parsing adapter

Codex has had native `codex exec resume <id>` (Stable) and `--json` JSONL events with exact usage for months. Today's `PlainAdapter` runs it stateless with len/4 estimates — the primary implementer has amnesia. Bonus fix: the old ArgsFn never passed `--sandbox workspace-write`, so edit-risk codex dispatches ran read-only (codex exec default).

**Files:**
- Modify: `internal/agent/adapter.go` (replace `NewCodexAdapter`'s `PlainAdapter` with a new `CodexAdapter` type)
- Modify: `internal/agent/event.go` (add `ParseCodexEvent` + `codexLine`)
- Modify: `internal/agent/runner.go` (track last `EventText` as result-text fallback)
- Modify: `testdata/fakeagent` (add `FAKEAGENT_PROTO=codex` mode)
- Modify: `internal/brain/cards.go` (codex card: sessions + `--json`)
- Test: `internal/agent/event_test.go`, `internal/agent/adapter_test.go`, `internal/agent/manager_test.go`

**Interfaces:**
- Produces:
  - `type CodexAdapter struct { BinPath string; Window int }`, `NewCodexAdapter() *CodexAdapter` (constructor name unchanged — callers in `repl.go` and `mcp_conductor.go` need no edits, but their `map[string]agent.Adapter` values still compile because both satisfy `Adapter`).
  - `func ParseCodexEvent(line []byte) (Event, bool)`.
  - Runner: `EventResult` with empty `Text` falls back to the last `EventText` payload.
  - fakeagent env knob `FAKEAGENT_PROTO=codex`.

- [x] **Step 1: Write the failing event-parser tests**

Add to `internal/agent/event_test.go`:

```go
func TestParseCodexEvent(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Event
		ok   bool
	}{
		{"thread started", `{"type":"thread.started","thread_id":"th-9"}`,
			Event{Type: EventInit, SessionID: "th-9"}, true},
		{"agent message", `{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`,
			Event{Type: EventText, Text: "hi"}, true},
		{"turn completed with usage",
			`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":40,"output_tokens":9}}`,
			Event{Type: EventResult, InputTokens: 140, OutputTokens: 9}, true},
		{"turn failed", `{"type":"turn.failed","error":{"message":"boom"}}`,
			Event{Type: EventResult, Text: "boom", IsError: true}, true},
		{"ignored", `{"type":"item.completed","item":{"type":"command_execution"}}`, Event{}, false},
		{"garbage", `not json`, Event{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseCodexEvent([]byte(tc.line))
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got %+v ok=%v, want %+v ok=%v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
```

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/agent/ -run TestParseCodexEvent -v`
Expected: FAIL — `undefined: ParseCodexEvent`

- [x] **Step 3: Implement the parser**

Add to `internal/agent/event.go`:

```go
// codexLine mirrors the subset of `codex exec --json` events styx reads:
// thread.started (thread_id = resumable session), item.completed
// agent_message items (assistant text), turn.completed (exact usage),
// turn.failed (error).
type codexLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ParseCodexEvent parses one codex exec --json line. ok is false for lines
// styx does not care about (command executions, deltas, malformed input).
func ParseCodexEvent(line []byte) (Event, bool) {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil {
		return Event{}, false
	}
	switch l.Type {
	case "thread.started":
		if l.ThreadID == "" {
			return Event{}, false
		}
		return Event{Type: EventInit, SessionID: l.ThreadID}, true
	case "item.completed":
		if l.Item.Type != "agent_message" || l.Item.Text == "" {
			return Event{}, false
		}
		return Event{Type: EventText, Text: l.Item.Text}, true
	case "turn.completed":
		return Event{
			Type:         EventResult,
			InputTokens:  l.Usage.InputTokens + l.Usage.CachedInputTokens,
			OutputTokens: l.Usage.OutputTokens,
		}, true
	case "turn.failed":
		return Event{Type: EventResult, Text: l.Error.Message, IsError: true}, true
	}
	return Event{}, false
}
```

Run: `go test ./internal/agent/ -run TestParseCodexEvent -v` → PASS

- [x] **Step 4: Write the failing adapter tests**

Add to `internal/agent/adapter_test.go`:

```go
func TestCodexAdapterArgs(t *testing.T) {
	a := NewCodexAdapter()
	if !a.SupportsResume() || !a.SupportsStream() {
		t.Fatal("codex adapter must be resume- and stream-capable")
	}
	if a.ContextWindow() != 400000 {
		t.Fatalf("codex window = %d, want 400000 (GPT-5.5 in Codex)", a.ContextWindow())
	}

	fresh := a.BuildArgs("fix it", "", "gpt-5.5", nil, false)
	want := []string{"--model", "gpt-5.5", "exec", "--json", "--sandbox", "workspace-write", "fix it"}
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("fresh args = %v, want %v", fresh, want)
	}

	resumed := a.BuildArgs("continue", "th-9", "", []string{"--add-dir", "/x"}, true)
	want = []string{"exec", "resume", "th-9", "--json", "--add-dir", "/x", "continue"}
	if !reflect.DeepEqual(resumed, want) {
		t.Fatalf("resume args = %v, want %v", resumed, want)
	}
}
```

(Add `"reflect"` to the test file imports if absent.)

- [x] **Step 5: Run to verify failure**

Run: `go test ./internal/agent/ -run TestCodexAdapterArgs -v`
Expected: FAIL — `NewCodexAdapter` returns `*PlainAdapter` (no resume/stream), args mismatch.

- [x] **Step 6: Implement the adapter**

In `internal/agent/adapter.go`, replace `NewCodexAdapter` (delete the old `PlainAdapter` construction; `PlainAdapter` itself stays — agy uses it):

```go
// CodexAdapter drives `codex exec --json` with native session resume
// (`codex exec resume <thread_id>`). Usage comes from turn.completed events —
// exact tokens, not estimates. Edit-risk turns run --sandbox workspace-write
// (codex exec defaults to read-only); read-risk turns keep the default.
type CodexAdapter struct {
	BinPath string // override for tests; "" means "codex" on PATH
	Window  int    // override for tests; 0 means 400000 (GPT-5.5 in Codex)
}

// NewCodexAdapter returns the production codex adapter.
func NewCodexAdapter() *CodexAdapter { return &CodexAdapter{} }

func (a *CodexAdapter) CLI() string { return "codex" }

func (a *CodexAdapter) Bin() string {
	if a.BinPath != "" {
		return a.BinPath
	}
	return "codex"
}

func (a *CodexAdapter) SupportsResume() bool { return true }
func (a *CodexAdapter) SupportsStream() bool { return true }

func (a *CodexAdapter) ContextWindow() int {
	if a.Window > 0 {
		return a.Window
	}
	return 400000
}

func (a *CodexAdapter) BuildArgs(msg, sessionID, model string, extra []string, readOnly bool) []string {
	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "exec")
	if sessionID != "" {
		args = append(args, "resume", sessionID)
	}
	args = append(args, "--json")
	if !readOnly {
		args = append(args, "--sandbox", "workspace-write")
	}
	args = append(args, extra...)
	return append(args, msg)
}

func (a *CodexAdapter) ParseEvent(line []byte) (Event, bool) { return ParseCodexEvent(line) }
```

Run: `go test ./internal/agent/ -run TestCodexAdapterArgs -v` → PASS

- [x] **Step 7: Runner result-text fallback (failing test first)**

Codex's `turn.completed` carries usage but no text; the text arrived in a prior `item.completed`. Add to `internal/agent/runner_test.go` a case using the fakeagent codex mode (see Step 9 for the fixture; write the test now):

```go
func TestRunnerCodexProtocol(t *testing.T) {
	th := &Thread{Name: "codex", CLI: "codex"}
	ad := &CodexAdapter{BinPath: fakeagentPath(t)} // reuse the existing helper that locates testdata/fakeagent
	r := &Runner{Adapter: ad, Thread: th}
	t.Setenv("FAKEAGENT_PROTO", "codex")
	t.Setenv("FAKEAGENT_SESSION", "th-42")
	t.Setenv("FAKEAGENT_TEXT", "done: patched")
	t.Setenv("FAKEAGENT_IN", "500")
	t.Setenv("FAKEAGENT_OUT", "50")
	res, err := r.Send(context.Background(), "patch it", "", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "done: patched" {
		t.Fatalf("result text must fall back to the last agent_message, got %q", res.Text)
	}
	if th.SessionID != "th-42" {
		t.Fatalf("thread must capture codex thread_id, got %q", th.SessionID)
	}
	if res.InputTokens != 500 || res.OutputTokens != 50 {
		t.Fatalf("usage must come from turn.completed, got %d/%d", res.InputTokens, res.OutputTokens)
	}
}
```

(If `runner_test.go` has no `fakeagentPath` helper, mirror how its existing tests locate `testdata/fakeagent` — typically `filepath.Join("..", "..", "testdata", "fakeagent")` resolved absolute.)

- [x] **Step 8: Implement the runner fallback**

In `runner.go`'s scan loop:

```go
var res TurnResult
var resultErr bool
var lastText string
for sc.Scan() {
	ev, ok := r.Adapter.ParseEvent(sc.Bytes())
	if !ok {
		continue
	}
	if r.OnEvent != nil {
		r.OnEvent(ev)
	}
	switch ev.Type {
	case EventInit:
		r.Thread.SessionID = ev.SessionID
	case EventText:
		lastText = ev.Text
	case EventResult:
		res.Text = ev.Text
		res.InputTokens = ev.InputTokens
		res.OutputTokens = ev.OutputTokens
		resultErr = ev.IsError
		if ev.SessionID != "" {
			r.Thread.SessionID = ev.SessionID
		}
	}
}
...
if res.Text == "" {
	res.Text = lastText // codex: text arrives in item.completed, not turn.completed
}
```

(Place the fallback right after the scan loop, before `cmd.Wait()` error handling uses `res`.)

- [x] **Step 9: Extend fakeagent with the codex protocol**

Edit `testdata/fakeagent` — add after the knobs comment block:

```bash
#   FAKEAGENT_PROTO        "codex": emit codex exec --json events instead
```

and before the claude-protocol echo lines:

```bash
if [ "$FAKEAGENT_PROTO" = "codex" ]; then
  if [ "$FAKEAGENT_FAIL_RESUME" = "1" ]; then
    for a in "$@"; do
      if [ "$a" = "resume" ]; then
        echo "session not found" >&2
        exit 1
      fi
    done
  fi
  echo "{\"type\":\"thread.started\",\"thread_id\":\"$S\"}"
  echo "{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"$T\"}}"
  echo "{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":$IN,\"cached_input_tokens\":0,\"output_tokens\":$OUT}}"
  exit 0
fi
```

(The `S`/`T`/`IN`/`OUT` defaults are set above the claude block — move those four default lines ABOVE the codex block so both protocols share them.)

Run: `go test ./internal/agent/ -run TestRunnerCodexProtocol -v` → PASS

- [x] **Step 10: Seed-message transition for existing codex threads**

Existing codex threads carry a rolling `Summary` but no `SessionID`/`LastDistillation`; the resume-capable seed path would drop it. In `manager.go` `seedMessage`, extend the resume branch:

```go
if ad.SupportsResume() {
	if th.SessionID != "" {
		return msg
	}
	var parts []string
	if th.LastDistillation != "" {
		parts = append(parts, "Handoff from the previous session of this thread:\n"+th.LastDistillation)
	} else if th.Summary != "" {
		// One-time transition: threads created while this CLI was
		// summary-based (pre-native-resume codex) seed from that summary.
		parts = append(parts, "Context from earlier in this conversation:\n"+th.Summary)
	} else if th.Turns == 0 {
		parts = append(parts, fmt.Sprintf(
			"You are the long-running %q agent thread of styx for project %s. You run bare — no hooks or auto-loaded project files — so rely on the message content and the files on disk in the working directory.",
			th.Name, m.Project.Name))
	}
	parts = append(parts, msg)
	return strings.Join(parts, "\n\n")
}
```

Add a `manager_test.go` case: a thread with `Summary: "prior context"`, `SessionID: ""`, CLI codex → dispatched message contains "prior context" (assert via `FAKEAGENT_ARGS_LOG`, which records the final message argument).

- [x] **Step 11: Update the codex + agy capability cards**

`internal/brain/cards.go` codex entry: in `Condensed`, replace the sentence `"No interactive handoff; route ambiguous or architectural implementation to claude instead."` with `"Persistent sessions via native exec resume; no interactive handoff — route ambiguous or architectural implementation to claude instead."` and add `"--json"` to `ExpectedFlags`.

Same file, agy entry: extend its `Condensed` (or the comment on `ResumeProbe: ""` at cards.go:35) with the tracked upstream gap: `// agy has --continue/--conversation <id> but never surfaces conversation IDs in --print output (google-antigravity/antigravity-cli#7) — headless resume stays impossible; styx-maintained summaries remain correct until that lands.` This keeps doctor's drift probes honest about WHY agy runs in degraded-continuity mode.

- [x] **Step 12: Full test pass + live smoke**

Run: `make test` → green (fix any repl/conductor tests that constructed `*PlainAdapter` for codex).
Live (burns one small codex turn):

```bash
codex exec --json 'reply with exactly: ok' 2>/dev/null | grep -E 'thread.started|turn.completed' | head -2
```

Expected: a `thread.started` line with a `thread_id` and a `turn.completed` line with `usage`. If field names differ from `codexLine`, adjust the struct to match reality — the installed CLI is the source of truth, then re-run tests.

- [x] **Step 13: Commit**

`docs/ARCHITECTURE.md`: rewrite the codex sentences in "Agent threads" (plain adapter → resume-capable stream adapter, exact usage, sandbox rule, Summary transition) and the `--add-dir` arg-order note if the new arg layout changes it. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(agent): codex threads use native exec resume, --json usage, and workspace-write sandbox"
```

---

### Task 9: ollama keep_alive + num_ctx + MCP-start preload

Ollama unloads models after 5 idle minutes (3–10s cold reload) and defaults context to 4096 tokens — the brain's ~40-exemplar prompt may silently truncate.

**Files:**
- Modify: `internal/channel/ollama/ollama.go` (`chatRequest`, `Send`)
- Modify: `internal/brain/brain.go` (`brainChatRequest`, `chat`)
- Modify: `internal/memory/embed.go` (`embedRequest`, `Embed`)
- Modify: `cmd/styx/mcp.go` (`cmdMCP`: background preload)
- Test: `internal/channel/ollama/ollama_test.go`, `internal/brain/brain_test.go` (or wherever `chat` payloads are asserted), `internal/memory/embed_test.go` (create if absent)

**Interfaces:**
- Produces: every styx→ollama request carries `keep_alive: "30m"`; chat requests whose estimated prompt tokens approach 4096 carry `options.num_ctx`; `preloadOllamaModels(a *app)` fire-and-forget at MCP server start.

- [x] **Step 1: Write the failing channel test**

Extend `TestSend_EmitsCorrectPayload` in `internal/channel/ollama/ollama_test.go` (it already decodes the posted body — add assertions):

```go
if body["keep_alive"] != "30m" {
	t.Errorf("keep_alive = %v, want 30m (avoid 5-min unload / cold reload)", body["keep_alive"])
}
```

And a new case for num_ctx:

```go
func TestSend_SetsNumCtxForLargePrompts(t *testing.T) {
	// ~24k chars ≈ 6k estimated tokens > the 4096 ollama default.
	big := strings.Repeat("x ", 12000)
	// ...same httptest setup as TestSend_EmitsCorrectPayload, capture body...
	opts, _ := body["options"].(map[string]any)
	if opts == nil || opts["num_ctx"] == nil {
		t.Fatal("large prompts must set options.num_ctx (ollama default 4096 silently truncates)")
	}
}
```

(Mirror the existing test's server/decode scaffolding exactly — same `httptest.NewServer` + capture pattern already in the file.)

- [x] **Step 2: Run to verify failure**

Run: `go test ./internal/channel/ollama/ -v`
Expected: FAIL on both new assertions.

- [x] **Step 3: Implement channel changes**

`chatRequest` gains two fields:

```go
type chatRequest struct {
	Model     string         `json:"model"`
	Stream    bool           `json:"stream"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	Messages  []chatMessage  `json:"messages"`
}
```

In `Send`, build the request:

```go
creq := chatRequest{Model: req.Model, Stream: false, KeepAlive: "30m", Messages: msgs}
if est := estimateTokens(prompt + req.System); est+1024 > 4096 {
	// Ollama defaults num_ctx to 4096 and silently truncates beyond it.
	creq.Options = map[string]any{"num_ctx": est + 2048}
}
body, err := json.Marshal(creq)
```

- [x] **Step 4: Brain + embedder (same pattern)**

`internal/brain/brain.go` — `brainChatRequest` gains `KeepAlive string \`json:"keep_alive,omitempty"\``; in `chat()` the literal gains `KeepAlive: "30m"` and the Options map gains num_ctx sizing:

```go
opts := map[string]any{"temperature": 0}
if est := (len(system) + len(user)) / 4; est+1024 > 4096 {
	opts["num_ctx"] = est + 2048
}
```

…and pass `Options: opts`. Add/extend a brain test asserting `keep_alive` in the posted payload (the brain tests already run an httptest ollama — extend its handler capture).

`internal/memory/embed.go` — `embedRequest` gains `KeepAlive string \`json:"keep_alive,omitempty"\``, set to `"30m"` in `Embed`. Test with an httptest server asserting the field.

- [x] **Step 5: Preload at MCP server start**

In `cmd/styx/mcp.go`, inside `cmdMCP` before `srv.Serve(...)`:

```go
go preloadOllamaModels(a) // best-effort: overlaps model load with the host handshake
```

New function in the same file:

```go
// preloadOllamaModels warms the brain + embedding models with keep_alive so
// the first real dispatch/recall doesn't pay a 3-10s cold load. Best-effort:
// failures are narrated, never fatal (ollama may simply be down).
func preloadOllamaModels(a *app) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, m := range []string{a.routing.Brain.Model, a.routing.Brain.EmbedModel} {
		if m == "" {
			continue
		}
		body, _ := json.Marshal(map[string]any{"model": m, "keep_alive": "30m"})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://localhost:11434/api/generate", bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logStatus("ollama preload %s skipped: %v", m, err)
			continue
		}
		resp.Body.Close()
	}
}
```

(Add `"bytes"`, `"net/http"`, `"time"` imports as needed. An empty `/api/generate` call with just model+keep_alive loads the model — documented ollama preload idiom.)

- [x] **Step 5b: Batch-embed audit (spec A2 closeout)**

The spec calls for batching multi-embed calls through `/api/embed`'s array input. Audit for loops that call `Embed` per item:

Run: `grep -rn "\.Embed(" --include="*.go" internal/ cmd/ | grep -v _test.go`

If every call site embeds a single text per user action (expected today: recall query, memory_save, distillation, brief indexing), batching has no call site — record that in the commit message and move on; the backlog keeps it for when intel indexing embeds in bulk. If a loop IS found, add an `EmbedBatch(ctx, texts []string) ([][]float32, error)` method to `OllamaEmbedder` mirroring `Embed` with `"input": texts`, and use it in that loop (test with httptest asserting one POST for N inputs).

- [x] **Step 6: Brain prompt size audit**

Add a regression test in `internal/brain/` (e.g. `prompt_size_test.go`):

```go
func TestBrainPromptFitsDefaultContextOrSetsNumCtx(t *testing.T) {
	sys, user := BuildPrompt(Turn{Utterance: "fix the flaky test"})
	est := (len(sys) + len(user)) / 4
	t.Logf("brain prompt ≈ %d tokens", est)
	// The chat() sizing rule must engage before ollama's 4096 default truncates.
	if est+1024 > 4096 {
		t.Log("prompt exceeds ollama's default window — chat() must set num_ctx (asserted in the payload test)")
	}
}
```

This documents the measured size in test output; the payload test from Step 4 is the actual gate.

- [x] **Step 7: Run everything + commit**

Run: `make test` → green.

`docs/ARCHITECTURE.md`: "Channels" ollama bullet + "Brain" + "Memory" sections gain the keep_alive/num_ctx/preload sentences. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "perf(ollama): keep_alive on every request, num_ctx sizing, model preload at MCP start"
```

---

### Task 10: progress notifications + dispatch duration/model fields

E2E evidence: zero output during a dispatch — a multi-minute codex run is indistinguishable from a hang.

**Files:**
- Modify: `internal/mcpserver/server.go` (progressToken parsing, notification emission, context plumbing)
- Modify: `cmd/styx/mcp_conductor.go` (dispatch handler: onEvent → progress; result fields)
- Test: `internal/mcpserver/server_test.go`, `cmd/styx/mcp_conductor_test.go`

**Interfaces:**
- Produces:
  - `mcpserver.ProgressFn(ctx) (func(progress float64, message string), bool)` — handlers emit progress; no-op absent a client token.
  - dispatch results gain `"duration_s"` (float, 1-decimal) and `"model"` (string) — additive, existing consumers unaffected.

- [ ] **Step 1: Write the failing server test**

Add to `internal/mcpserver/server_test.go`:

```go
func TestProgressNotifications(t *testing.T) {
	tool := Tool{
		Name: "slow",
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			if notify, ok := ProgressFn(ctx); ok {
				notify(1, "working")
			}
			return map[string]any{"done": true}, nil
		},
	}
	srv := New("t", "0", []Tool{tool})
	in := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow","arguments":{},"_meta":{"progressToken":"tok-1"}}}` + "\n")
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("want notification + response, got %d lines: %s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"notifications/progress"`) ||
		!strings.Contains(lines[0], `"tok-1"`) ||
		!strings.Contains(lines[0], `"working"`) {
		t.Fatalf("first line must be the progress notification, got %s", lines[0])
	}
	if !strings.Contains(lines[1], `"id":1`) {
		t.Fatalf("second line must be the response, got %s", lines[1])
	}
}

func TestProgressFnAbsentWithoutToken(t *testing.T) {
	tool := Tool{
		Name: "plain",
		Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
			if _, ok := ProgressFn(ctx); ok {
				t.Error("ProgressFn must report absent when the client sent no progressToken")
			}
			return "ok", nil
		},
	}
	srv := New("t", "0", []Tool{tool})
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"plain","arguments":{}}}` + "\n")
	var out bytes.Buffer
	_ = srv.Serve(context.Background(), in, &out)
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/mcpserver/ -run TestProgress -v`
Expected: FAIL — `undefined: ProgressFn`

- [ ] **Step 3: Implement in mcpserver**

In `server.go`:

```go
// progressKey carries the per-call progress emitter through handler context.
type progressKey struct{}

// ProgressFn returns the progress emitter installed for this tool call, if
// the client requested progress (params._meta.progressToken). Handlers call
// it to narrate long-running work; it is nil-safe via the ok bool.
func ProgressFn(ctx context.Context) (func(progress float64, message string), bool) {
	fn, ok := ctx.Value(progressKey{}).(func(float64, string))
	return fn, ok
}
```

Server gains an encoder guard (also groundwork for Phase B):

```go
type Server struct {
	name    string
	version string
	tools   []Tool
	byName  map[string]Tool

	mu  sync.Mutex    // serializes writes to enc
	enc *json.Encoder // set for the duration of Serve
}

func (s *Server) write(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(v)
}
```

In `Serve`: set `s.enc = json.NewEncoder(out)` before the loop and replace both `enc.Encode(...)` calls with `s.write(...)`.

`callParams` gains `_meta`:

```go
type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta"`
}
```

In `callTool`, after resolving the tool and before invoking the handler:

```go
if len(p.Meta.ProgressToken) > 0 && string(p.Meta.ProgressToken) != "null" {
	tok := p.Meta.ProgressToken
	ctx = context.WithValue(ctx, progressKey{}, func(progress float64, message string) {
		_ = s.write(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params": map[string]any{
				"progressToken": tok,
				"progress":      progress,
				"message":       message,
			},
		})
	})
}
result, err := tool.Handler(ctx, p.Arguments)
```

(`callTool`'s signature must gain `ctx` from `handle` — it already receives it via the `s.callTool(ctx, req.Params)` call at server.go:113; thread it through.)

Add `"sync"` to imports.

Run: `go test ./internal/mcpserver/ -v` → PASS

- [ ] **Step 4: Wire dispatch narration + result fields**

In `cmd/styx/mcp_conductor.go`'s dispatch handler:

At the top of the Handler (after arg validation), capture start time:

```go
start := time.Now()
```

For the ollama branch result:

```go
return map[string]any{"cli": "ollama", "text": resp.Text,
	"model": model, "duration_s": math.Round(time.Since(start).Seconds()*10) / 10}, nil
```

For the thread branch, replace the `nil` onEvent in `m.mgr.Dispatch(ctx, spec, nil)`:

```go
notify, hasNotify := mcpserver.ProgressFn(ctx)
var events int
onEvent := func(ev agent.Event) {
	events++
	var msg string
	switch ev.Type {
	case agent.EventInit:
		msg = in.CLI + ": session started"
	case agent.EventText:
		if events%5 != 0 { // throttle streaming chatter
			return
		}
		msg = fmt.Sprintf("%s: streaming (%d events)", in.CLI, events)
	case agent.EventResult:
		msg = in.CLI + ": finishing"
	}
	if msg == "" {
		return
	}
	logStatus("dispatch %s", msg)
	if hasNotify {
		notify(float64(events), msg)
	}
}
res, err := m.mgr.Dispatch(ctx, agent.DispatchSpec{
	Thread: in.Thread, CLI: in.CLI, Model: model,
	Message: in.Message, ExtraRoots: in.ExtraRoots,
	ReadOnly: in.Risk == "read",
}, onEvent)
```

(The `DispatchSpec` literal is unchanged from the current code at mcp_conductor.go:219-223 — only the final argument changes from `nil` to `onEvent`.)

…and extend the success result:

```go
return map[string]any{
	"thread": thread, "cli": in.CLI, "text": res.Text,
	"tokens_in": res.InputTokens, "tokens_out": res.OutputTokens,
	"model": model, "duration_s": math.Round(time.Since(start).Seconds()*10) / 10,
}, nil
```

Add `"math"`, `"time"`, and the `agent` import usages as needed (`agent` is already imported).

- [ ] **Step 5: Conductor-level test**

Add to `cmd/styx/mcp_conductor_test.go` an assertion on the new fields using the Task 3 fake channel test — extend `TestDispatchOllamaDefaultsModel`:

```go
if _, ok := res["duration_s"]; !ok {
	t.Fatal("dispatch result must include duration_s")
}
if res["model"] != "qwen2.5-coder:7b" {
	t.Fatalf("dispatch result must echo the resolved model, got %v", res["model"])
}
```

Run: `go test ./cmd/styx/ -run TestDispatch -v` → PASS

- [ ] **Step 6: Commit**

`docs/ARCHITECTURE.md`: "MCP server" section — progress notifications + `_meta.progressToken` support, write serialization; "Conductor MCP tools" — dispatch narration + new result fields. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(mcp): progress notifications during dispatch + duration/model result fields"
```

---

### Task 11: restore the fable tier

`~/.config/styx/routing.toml` and the seeded defaults map `fable → opus` from the 2026-06-12 suspension; Fable 5 is callable again (this machine's Claude Code runs it). Doctor already probes each distinct tier alias, so the flip is self-verifying.

**Files:**
- Modify: `cmd/styx/default_routing.go` (~lines 120-128: `[tiers]` block + comments)
- Modify: `internal/config/routing.go` (~lines 85-87: tier defaults map + comment)
- Modify: `internal/config/upgrade.go` (new `EnsureFableTier`, wired into `UpgradeRoutingFile`)
- Modify: `internal/brain/cards.go` (claude card: drop the "suspended" parenthetical)
- Modify: `internal/guidance/guidance.go` (`Seed`: model-tier line mentions fable, if it names tiers — check `grep -n "tier" internal/guidance/guidance.go`)
- Test: `internal/config/upgrade_test.go`, `internal/config/routing_test.go`

**Interfaces:**
- Produces: `Tiers["fable"] == "fable"` by default; `EnsureFableTier(content string) (string, bool)` idempotent migration for existing user configs (only rewrites the exact seeded `fable  = "opus"` line — a user-customized mapping is left alone).

- [ ] **Step 1: Write the failing tests**

`internal/config/routing_test.go` — find the existing tier-default assertion (grep `"fable"`) and flip it:

```go
if r.Tiers["fable"] != "fable" {
	t.Errorf(`default fable tier = %q, want "fable" (suspension lifted)`, r.Tiers["fable"])
}
```

`internal/config/upgrade_test.go`:

```go
func TestEnsureFableTier(t *testing.T) {
	seeded := "[tiers]\nfable  = \"opus\"\nopus   = \"opus\"\n"
	got, changed := EnsureFableTier(seeded)
	if !changed || !strings.Contains(got, `fable  = "fable"`) {
		t.Fatalf("seeded fable mapping must upgrade, got changed=%v:\n%s", changed, got)
	}
	// Idempotent.
	again, changed2 := EnsureFableTier(got)
	if changed2 || again != got {
		t.Fatal("second run must be a no-op")
	}
	// User customization is respected.
	custom := "[tiers]\nfable  = \"sonnet\"\n"
	_, changed3 := EnsureFableTier(custom)
	if changed3 {
		t.Fatal("user-customized fable mapping must be left alone")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run 'TestEnsureFableTier|TestRouting' -v`
Expected: FAIL — `undefined: EnsureFableTier` + old default assertion.

- [ ] **Step 3: Implement**

`internal/config/routing.go` tier defaults (~line 85):

```go
// fable = Claude Fable 5, the top tier, callable again since mid-2026
// (was mapped to opus during the 2026-06-12 suspension). Safety
// classifiers may transparently serve opus for flagged requests.
"fable": "fable", "opus": "opus", "sonnet": "sonnet", "haiku": "haiku",
```

`cmd/styx/default_routing.go`: update the `[tiers]` block comment (drop the suspension paragraph, keep one line noting the history) and set `fable  = "fable"`. Update the `fable_weekly_cap` comment: no longer vestigial — it caps fable messages before tier degradation to opus. While in this file, also extend the comment on any rule carrying `effort` (the `research.critic` example) to document the full claude value set: `# effort: low|medium|high|xhigh|max (claude); codex maps to model_reasoning_effort` (spec A2 effort closeout).

`internal/config/upgrade.go`:

```go
// EnsureFableTier upgrades the exact seeded suspension-era mapping
// `fable  = "opus"` to `fable  = "fable"`. Only the seeded spelling is
// rewritten — any user-customized mapping is preserved. Returns the new
// content and whether a rewrite happened.
func EnsureFableTier(content string) (string, bool) {
	const old = `fable  = "opus"`
	const new_ = `fable  = "fable"`
	if !strings.Contains(content, old) {
		return content, false
	}
	return strings.Replace(content, old, new_, 1), true
}
```

Wire into `UpgradeRoutingFile` alongside `EnsureImplementRules` (same read-modify-write path, same backup behavior; extend the function's returns/logging the way implementInjected is handled — follow the existing pattern in the function body at upgrade.go:246).

`internal/brain/cards.go` claude card: delete the trailing `(A 'fable' tier exists for the most demanding work but is currently suspended and maps to opus - prefer opus.)` and change the tier list to `fable (the most demanding judgment work), opus (deep planning, architecture, hard debugging, complex/ambiguous implementation), sonnet (…), haiku (…)`.

`internal/guidance/guidance.go`: if the `Seed` model-tier guidance line enumerates tiers, add fable as the top tier. **Note:** changing `Seed` requires the seed-upgrade mechanic — retain the previous seed content as the next `seedVN` constant so unmodified guidance files upgrade transparently (see the "Guidance" section of ARCHITECTURE.md; follow the existing `seedV1` pattern).

- [ ] **Step 4: Run tests + doctor**

Run: `go test ./internal/config/ ./internal/brain/ ./internal/guidance/ -v` → PASS
Run: `make build && ./bin/styx doctor` → expect `tier fable -> claude --model fable - callable`. If NOT callable on this machine, STOP: revert the default flip to a routing.toml-comment-only change and flag to the user — the migration must not ship pointing at an uncallable model.

- [ ] **Step 5: Commit**

`docs/ARCHITECTURE.md`: "Routing" section `Tiers` sentence (fable no longer suspended); "Brain" card description note. Bump `last_verified`. Also update the README if it mentions the fable suspension.

```bash
go vet ./... && gofmt -w . && make test
git add -A
git commit -m "feat(routing): restore the fable tier — suspension lifted, seeded configs migrate"
```

---

### Task 12: E2E harness (`make e2e`)

The regression net: drives a real `styx mcp` subprocess over JSON-RPC exactly as a conductor would, with fakeagent CLIs on PATH and isolated config. Hermetic by default; `STYX_E2E_LIVE=1` adds real-CLI smoke.

**Files:**
- Create: `e2e/e2e_test.go` (build tag `e2e`)
- Create: `e2e/doc.go` (package comment, no build tag, so the package is visible to tooling)
- Modify: `Makefile` (`e2e` target)
- Modify: `README.md` (testing section), `docs/ARCHITECTURE.md` (testing conventions)

**Interfaces:**
- Consumes: `./bin/styx` (built by the target), `testdata/fakeagent` (both protocols from Task 8), Tasks 1-10 behaviors.
- Produces: `make e2e`.

- [ ] **Step 1: Makefile target**

```makefile
e2e: build
	go test -tags e2e ./e2e/ -v -count=1
```

Add `e2e` to `.PHONY`.

- [ ] **Step 2: Write the harness**

`e2e/doc.go`:

```go
// Package e2e drives a real `styx mcp` subprocess over JSON-RPC — the exact
// first-contact sequences an MCP conductor performs — with fakeagent CLIs on
// PATH and isolated XDG config. Hermetic by default (no quota, no network
// beyond a possibly-absent local ollama); STYX_E2E_LIVE=1 adds real-CLI smoke.
// Run via `make e2e`.
package e2e
```

`e2e/e2e_test.go`:

```go
//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rpc is one JSON-RPC exchange helper over the server's stdio.
type mcpClient struct {
	t      *testing.T
	stdin  io.WriteCloser
	out    *bufio.Scanner
	nextID int
}

func (c *mcpClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.nextID++
	req := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		c.t.Fatalf("write %s: %v", method, err)
	}
	deadline := time.After(60 * time.Second)
	for {
		lineCh := make(chan string, 1)
		go func() {
			if c.out.Scan() {
				lineCh <- c.out.Text()
			} else {
				close(lineCh)
			}
		}()
		select {
		case <-deadline:
			c.t.Fatalf("timeout waiting for response to %s", method)
		case line, ok := <-lineCh:
			if !ok {
				c.t.Fatalf("server closed stdout waiting for %s", method)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				continue
			}
			if id, _ := m["id"].(float64); int(id) == c.nextID {
				return m
			}
			// notifications (progress) fall through the loop
		}
	}
}

func (c *mcpClient) toolCall(name string, args any) (map[string]any, bool) {
	c.t.Helper()
	resp := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		c.t.Fatalf("tools/call %s: no result in %v", name, resp)
	}
	isErr, _ := result["isError"].(bool)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		c.t.Fatalf("tools/call %s: empty content", name)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		payload = map[string]any{"_raw": text}
	}
	return payload, isErr
}

// startServer builds the isolated environment and spawns `styx mcp`.
func startServer(t *testing.T) (*mcpClient, string) {
	t.Helper()
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(repoRoot, "bin", "styx")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("run `make build` first (or `make e2e`): %v", err)
	}

	home := t.TempDir()
	// Fake CLIs: fakeagent as both `claude` and `codex` on PATH.
	fakeBinDir := filepath.Join(home, "fakebin")
	if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeagent, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "fakeagent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(fakeBinDir, name), fakeagent, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// A temp git repo to be the launch project.
	proj := filepath.Join(home, "demo-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"commit", "--allow-empty", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = proj
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@test",
			"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@test")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	srv := exec.Command(bin, "mcp")
	srv.Dir = proj
	srv.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKEAGENT_TEXT=e2e-ok",
	)
	stdin, err := srv.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := srv.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stdin.Close(); srv.Process.Kill(); srv.Wait() })

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	c := &mcpClient{t: t, stdin: stdin, out: sc}

	init := c.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "e2e", "version": "0"},
	})
	if fmt.Sprint(init["result"].(map[string]any)["serverInfo"].(map[string]any)["name"]) != "styx" {
		t.Fatalf("bad initialize: %v", init)
	}
	// initialized notification (no id, no response expected)
	c.stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	return c, proj
}

func TestFirstContact(t *testing.T) {
	c, _ := startServer(t)

	// tools/list: all 11 tools present.
	resp := c.call("tools/list", nil)
	tools, _ := resp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 11 {
		t.Fatalf("want 11 tools, got %d", len(tools))
	}

	// route: pure local decision.
	route, isErr := c.toolCall("route", map[string]any{"task": "implement the retry logic", "verb": "implement"})
	if isErr || route["channel"] == "" {
		t.Fatalf("route failed: %v", route)
	}

	// budget_status: all four channels.
	if _, isErr := c.toolCall("budget_status", map[string]any{}); isErr {
		t.Fatal("budget_status errored")
	}

	// dispatch cli=claude WITHOUT project: resolves via server cwd (Task 4).
	disp, isErr := c.toolCall("dispatch", map[string]any{
		"cli": "claude", "message": "reply ok", "risk": "read",
	})
	if isErr {
		t.Fatalf("naive dispatch must succeed via cwd project: %v", disp)
	}
	if disp["text"] != "e2e-ok" {
		t.Fatalf("want fakeagent text, got %v", disp["text"])
	}
	if _, ok := disp["duration_s"]; !ok {
		t.Fatal("dispatch result missing duration_s (Task 10)")
	}

	// thread_status without project: [] shape + the thread just created.
	ts, isErr := c.toolCall("thread_status", map[string]any{})
	if isErr {
		t.Fatalf("thread_status errored: %v", ts)
	}
	threads, ok := ts["threads"].([]any)
	if !ok {
		t.Fatalf("threads must be an array (never null), got %T", ts["threads"])
	}
	if len(threads) != 1 || !strings.Contains(threads[0].(string), "claude") {
		t.Fatalf("want the claude thread listed, got %v", threads)
	}

	// unknown project: loud error listing the registry (Task 4).
	errRes, isErr := c.toolCall("thread_status", map[string]any{"project": "nope-not-real"})
	if !isErr {
		t.Fatalf("unknown project must error, got %v", errRes)
	}
	if raw, _ := errRes["_raw"].(string); !strings.Contains(raw, "registered projects") {
		t.Fatalf("error must list registered projects, got %v", errRes)
	}
}

func TestVersionVerb(t *testing.T) {
	repoRoot, _ := filepath.Abs("..")
	out, err := exec.Command(filepath.Join(repoRoot, "bin", "styx"), "version").CombinedOutput()
	if err != nil {
		t.Fatalf("styx version: %v: %s", err, out)
	}
	if !strings.HasPrefix(string(out), "styx ") {
		t.Fatalf("want 'styx <version>', got %q", out)
	}
}

func TestLiveSmoke(t *testing.T) {
	if os.Getenv("STYX_E2E_LIVE") != "1" {
		t.Skip("set STYX_E2E_LIVE=1 for real-CLI smoke (uses quota)")
	}
	// Live mode: real PATH (no fakes), real ollama.
	// 1. doctor
	repoRoot, _ := filepath.Abs("..")
	bin := filepath.Join(repoRoot, "bin", "styx")
	if out, err := exec.Command(bin, "doctor").CombinedOutput(); err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	// 2. ollama one-shot with the model default (Task 3), via a live server.
	//    Reuses startServer but strips the fake PATH prefix and cwd-runs in
	//    this repo. Implemented as a minimal inline variant:
	srv := exec.Command(bin, "mcp")
	srv.Dir = repoRoot
	stdin, _ := srv.StdinPipe()
	stdout, _ := srv.StdoutPipe()
	srv.Stderr = os.Stderr
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); srv.Process.Kill(); srv.Wait() }()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	c := &mcpClient{t: t, stdin: stdin, out: sc}
	c.call("initialize", map[string]any{"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "e2e-live", "version": "0"}})
	stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"))
	res, isErr := c.toolCall("dispatch", map[string]any{
		"cli": "ollama", "message": "Reply with exactly: pong", "risk": "read",
	})
	if isErr {
		t.Fatalf("live ollama dispatch with default model failed: %v", res)
	}
	if !strings.Contains(strings.ToLower(fmt.Sprint(res["text"])), "pong") {
		t.Logf("note: unexpected text %v (model behavior, not plumbing)", res["text"])
	}
}
```

- [ ] **Step 3: Run**

Run: `make e2e`
Expected: `TestFirstContact` + `TestVersionVerb` PASS, `TestLiveSmoke` SKIP.
Debug tips: server stderr is passed through — `[styx]` lines show what the server did; a hang in `startServer` usually means the fake CLI wasn't found on PATH or auto-registration failed (check `git` succeeded in the temp repo).

Run once live on this machine: `STYX_E2E_LIVE=1 make e2e` → all PASS (needs ollama up).

- [ ] **Step 4: Commit**

`docs/ARCHITECTURE.md` "Testing conventions": add the e2e harness paragraph (hermetic fakeagent-on-PATH design, live mode, why no Docker). `README.md`: `make e2e` in the build/test section. Bump `last_verified`.

```bash
go vet ./... && gofmt -w . && make test && make e2e
git add -A
git commit -m "test(e2e): JSON-RPC first-contact harness driving styx mcp with fake CLIs"
```

---

## Post-plan verification (whole-phase acceptance)

After all 12 tasks:

- [ ] `make test && make e2e` green.
- [ ] `STYX_E2E_LIVE=1 make e2e` green on this machine (ollama up).
- [ ] Manual conductor session: `make install`, then `styx` in this repo; ask the conductor to "dispatch a trivial task to codex and show me thread status". Verify: no tool errors, progress visible, `thread_status` shows sane context %, `styx budget` shows the recorded usage.
- [ ] `docs/ARCHITECTURE.md` `last_verified` is the final commit date; `git log --oneline` shows one commit per task.
