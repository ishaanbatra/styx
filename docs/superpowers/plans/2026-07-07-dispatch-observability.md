# Live Dispatch Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make in-flight agent dispatch observable — surface each agent's live tool activity, flag stalls mechanically, and layer a free local-ollama watcher that reports cross-agent health — inline, via a REPL `/watch` command, and from a second terminal (`styx watch`).

**Architecture:** The `Runner` writes every parsed event to a shared, in-process `activity.Board` (this instruments sync *and* background dispatch at once — liveness no longer flows through the caller's `OnEvent`). A pure renderer paints per-agent lines with an idle-based stall flag. A best-effort ollama watcher reads the board and writes a natural-language note. A throttled disk mirror lets a separate `styx watch` process read the same board.

**Tech Stack:** Go, `modernc.org/sqlite` (unchanged), local ollama over `/api/chat` HTTP (pattern from `internal/learn/digest.go`), `github.com/mattn/go-isatty` (already used by `internal/progress`).

## Global Constraints

- Channels are CLIs/HTTP, never SDKs. The watcher calls local ollama directly over HTTP; it does NOT route through the budget ledger or `routing.toml` (observation, not dispatch — free, unmetered).
- Never swallow errors (`x, _ :=`). Mirror-write failures are narrated via `logStatus`, never fail dispatch — matching `writeTaskFile` in `cmd/styx/mcp_tasks.go`.
- File writes are atomic (tmp + rename); follow `internal/paths` helpers for locations.
- Table-driven tests with `t.Run`; fakes over mocks (`httptest` for ollama).
- Drift contract: after editing owned source, update the owner doc in the same commit and bump `last_verified`. `docs/ARCHITECTURE.md` owns `internal/**` and `cmd/styx/**`; `README.md` owns the verb table.
- The ollama base URL is `http://localhost:11434` (hardcoded default, matching `cmd/styx/learn.go:91-92`); the watcher model default is `routing.Brain.Model`.
- New packages get a doc comment explaining their role in the orchestration.

---

### Task 1: Tool events in the stream parser

Surface `tool_use` (claude) and non-message `item.completed` (codex) lines the parser currently drops, as a new `EventTool`.

**Files:**
- Modify: `internal/agent/event.go`
- Test: `internal/agent/event_test.go`

**Interfaces:**
- Produces: `agent.EventTool EventType = "tool"`; `Event.Tool string` field; `agent.ParseClaudeEvent`/`ParseCodexEvent` emit `EventTool` for tool activity.

- [ ] **Step 1: Write the failing tests**

In `internal/agent/event_test.go`, replace the existing `"assistant tool-use only (no text) ignored"` case (which asserts `ok: false`) and add codex coverage. Add these to the claude test table:

```go
{
	name: "claude tool_use surfaces EventTool with command target",
	line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...\nsecond line"}}]}}`,
	want: Event{Type: EventTool, Tool: "Bash", Text: "go test ./..."},
	ok:   true,
},
{
	name: "claude tool_use file target",
	line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x/y.go"}}]}}`,
	want: Event{Type: EventTool, Tool: "Read", Text: "/x/y.go"},
	ok:   true,
},
```

Add a codex-specific test function:

```go
func TestParseCodexToolEvent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Event
		ok   bool
	}{
		{
			name: "command_execution item surfaces EventTool",
			line: `{"type":"item.completed","item":{"type":"command_execution","command":"go build ./..."}}`,
			want: Event{Type: EventTool, Tool: "command_execution", Text: "go build ./..."},
			ok:   true,
		},
		{
			name: "agent_message still parses as text",
			line: `{"type":"item.completed","item":{"type":"agent_message","text":"hello"}}`,
			want: Event{Type: EventText, Text: "hello"},
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseCodexEvent([]byte(tt.line))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agent/ -run 'ParseClaude|ParseCodexTool' -v`
Expected: FAIL — `EventTool` undefined; codex default case not handled.

- [ ] **Step 3: Add the event type and field**

In `internal/agent/event.go`, extend the const block and struct:

```go
const (
	EventInit   EventType = "init"   // session started; SessionID set
	EventText   EventType = "text"   // intermediate assistant text
	EventTool   EventType = "tool"   // agent invoked a tool; Tool + Text (target) set
	EventResult EventType = "result" // final result; Text + token usage set
)

// Event is one parsed line of an agent's stream output.
type Event struct {
	Type         EventType
	SessionID    string
	Tool         string // tool name for EventTool (e.g. "Bash", "command_execution")
	Text         string
	InputTokens  int
	OutputTokens int
	IsError      bool
}
```

- [ ] **Step 4: Parse claude tool_use**

In `internal/agent/event.go`, extend the `claudeLine` content struct and the `assistant` case:

```go
	Message struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
```

Replace the `case "assistant":` body:

```go
	case "assistant":
		text := ""
		for _, c := range l.Message.Content {
			if c.Type == "tool_use" {
				return Event{Type: EventTool, Tool: c.Name, Text: claudeToolTarget(c.Input)}, true
			}
			if c.Type == "text" {
				text += c.Text
			}
		}
		if text == "" {
			return Event{}, false
		}
		return Event{Type: EventText, Text: text}, true
```

Add the target extractor at the bottom of the file:

```go
// claudeToolTarget pulls a best-effort target out of a tool_use input block:
// the shell command, the file path, the URL, or the search pattern — whichever
// is present. Empty when the tool takes none of these (target-less tools still
// surface via their name).
func claudeToolTarget(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		URL      string `json:"url"`
		Pattern  string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	switch {
	case m.Command != "":
		return firstLine(m.Command)
	case m.FilePath != "":
		return m.FilePath
	case m.Path != "":
		return m.Path
	case m.URL != "":
		return m.URL
	case m.Pattern != "":
		return m.Pattern
	}
	return ""
}

// firstLine returns the first line of s, trimmed, capped at 80 runes.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 80 {
		return string([]rune(s)[:80]) + "…"
	}
	return s
}
```

Add `"strings"` to the imports of `event.go`.

- [ ] **Step 5: Parse codex tool/command items**

In `internal/agent/event.go`, add a `Command` field to the codex item struct:

```go
	Item     struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
	} `json:"item"`
```

Replace the `case "item.completed":` body:

```go
	case "item.completed":
		switch l.Item.Type {
		case "agent_message":
			if l.Item.Text == "" {
				return Event{}, false
			}
			return Event{Type: EventText, Text: l.Item.Text}, true
		case "":
			return Event{}, false
		default:
			// Any non-message completed item is tool/command activity. codex item
			// types include command_execution, file_change, mcp_tool_call; exact
			// sub-field names vary by codex version, so surface the item type as
			// the tool label plus a best-effort command string. Verified against a
			// live `codex exec --json` stream; tighten if a richer field appears.
			return Event{Type: EventTool, Tool: l.Item.Type, Text: firstLine(l.Item.Command)}, true
		}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run 'ParseClaude|ParseCodexTool' -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/agent/event.go internal/agent/event_test.go
git commit -m "feat(agent): surface tool_use and codex command events as EventTool

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Activity board

The in-process, thread-safe substrate every surface reads. Strings + timestamps only — no `agent.Event` — so `internal/activity` never imports `internal/agent` (one-directional: agent → activity).

**Files:**
- Create: `internal/activity/board.go`
- Test: `internal/activity/board_test.go`
- Modify: `docs/ARCHITECTURE.md` (register the new package)

**Interfaces:**
- Produces: `activity.NewBoard() *Board`; `(*Board).Record(label, summary string)`, `(*Board).Done(label string, elapsed time.Duration)`, `(*Board).Snapshot() []AgentState`, `(*Board).Recent(label string) []string`, `(*Board).SetWatcherNote(note string)`, `(*Board).WatcherNote() string`, `(*Board).SetClock(func() time.Time)`; `activity.AgentState{Label, Last string; LastAt time.Time; Done bool; Elapsed time.Duration; Recent []string}`.

- [ ] **Step 1: Write the failing test**

Create `internal/activity/board_test.go`:

```go
package activity

import (
	"sync"
	"testing"
	"time"
)

func TestBoardRecordAndSnapshot(t *testing.T) {
	b := NewBoard()
	base := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return base })

	b.Record("claude", "Bash: go test")
	b.Record("codex", "Read: main.go")
	b.Record("claude", "WebFetch: example.com")

	snap := b.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("want 2 agents, got %d", len(snap))
	}
	if snap[0].Label != "claude" || snap[0].Last != "WebFetch: example.com" {
		t.Fatalf("claude row wrong: %+v", snap[0])
	}
	if !snap[0].LastAt.Equal(base) {
		t.Fatalf("LastAt not stamped from clock: %v", snap[0].LastAt)
	}
}

func TestBoardDone(t *testing.T) {
	b := NewBoard()
	b.Record("claude", "Bash: go build")
	b.Done("claude", 3*time.Minute)
	snap := b.Snapshot()
	if !snap[0].Done || snap[0].Elapsed != 3*time.Minute {
		t.Fatalf("done not recorded: %+v", snap[0])
	}
}

func TestBoardRecentCap(t *testing.T) {
	b := NewBoard()
	for i := 0; i < recentCap+5; i++ {
		b.Record("claude", "event")
	}
	if got := len(b.Recent("claude")); got != recentCap {
		t.Fatalf("recent cap = %d, want %d", got, recentCap)
	}
}

func TestBoardWatcherNote(t *testing.T) {
	b := NewBoard()
	b.SetWatcherNote("both healthy")
	if b.WatcherNote() != "both healthy" {
		t.Fatalf("note = %q", b.WatcherNote())
	}
}

func TestBoardConcurrentWriters(t *testing.T) {
	b := NewBoard()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b.Record("claude", "x") }()
	}
	wg.Wait()
	if len(b.Snapshot()) != 1 {
		t.Fatalf("want 1 agent after concurrent writes")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/activity/ -v`
Expected: FAIL — package/symbols do not exist.

- [ ] **Step 3: Write the board**

Create `internal/activity/board.go`:

```go
// Package activity is styx's live dispatch-observability substrate. Every
// agent turn writes its parsed events here as one-line summaries; renderers,
// the ollama watcher, and the cross-process disk mirror all read from it. It
// holds strings and timestamps only (no agent.Event) so it imports nothing
// from internal/agent — the dependency runs one way, agent -> activity.
package activity

import (
	"sync"
	"time"
)

// recentCap bounds the per-agent ring buffer the ollama watcher reads, keeping
// its prompt small.
const recentCap = 20

// AgentState is an immutable snapshot of one agent's liveness.
type AgentState struct {
	Label   string
	Last    string
	LastAt  time.Time
	Done    bool
	Elapsed time.Duration
	Recent  []string
}

type agentRec struct {
	last    string
	lastAt  time.Time
	done    bool
	elapsed time.Duration
	recent  []string
}

// Board is the shared, concurrency-safe liveness map for one styx session,
// keyed by agent label (thread name / task id).
type Board struct {
	mu    sync.Mutex
	now   func() time.Time
	order []string
	ag    map[string]*agentRec
	note  string
}

// NewBoard returns an empty board using the wall clock.
func NewBoard() *Board {
	return &Board{now: time.Now, ag: map[string]*agentRec{}}
}

// SetClock overrides the time source (tests).
func (b *Board) SetClock(fn func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = fn
}

// Record stamps one activity line for label, marking the agent live again.
func (b *Board) Record(label, summary string) {
	if summary == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.ag[label]
	if r == nil {
		r = &agentRec{}
		b.ag[label] = r
		b.order = append(b.order, label)
	}
	r.last = summary
	r.lastAt = b.now()
	r.done = false
	r.recent = append(r.recent, summary)
	if len(r.recent) > recentCap {
		r.recent = r.recent[len(r.recent)-recentCap:]
	}
}

// Done marks label finished with its total elapsed time.
func (b *Board) Done(label string, elapsed time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.ag[label]
	if r == nil {
		r = &agentRec{}
		b.ag[label] = r
		b.order = append(b.order, label)
	}
	r.done = true
	r.elapsed = elapsed
}

// Snapshot returns per-agent state in first-seen order.
func (b *Board) Snapshot() []AgentState {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]AgentState, 0, len(b.order))
	for _, label := range b.order {
		r := b.ag[label]
		recent := make([]string, len(r.recent))
		copy(recent, r.recent)
		out = append(out, AgentState{
			Label: label, Last: r.last, LastAt: r.lastAt,
			Done: r.done, Elapsed: r.elapsed, Recent: recent,
		})
	}
	return out
}

// Recent returns a copy of label's recent activity lines (oldest first).
func (b *Board) Recent(label string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.ag[label]
	if r == nil {
		return nil
	}
	out := make([]string, len(r.recent))
	copy(out, r.recent)
	return out
}

// SetWatcherNote stores the ollama watcher's latest health read.
func (b *Board) SetWatcherNote(note string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.note = note
}

// WatcherNote returns the latest health read ("" if none).
func (b *Board) WatcherNote() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.note
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/activity/ -race -v`
Expected: PASS (including `TestBoardConcurrentWriters` under `-race`).

- [ ] **Step 5: Register the package in ARCHITECTURE.md**

In `docs/ARCHITECTURE.md`, add a bullet to the subsystem list:

```
- **Activity** (`internal/activity/`): live dispatch-observability board —
  per-agent heartbeat, stall detection, ollama watcher, and the cross-process
  disk mirror behind `styx watch`.
```

Bump the doc's `last_verified` date in its frontmatter to `2026-07-07`.

- [ ] **Step 6: Commit**

```bash
git add internal/activity/board.go internal/activity/board_test.go docs/ARCHITECTURE.md
git commit -m "feat(activity): concurrency-safe liveness board substrate

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Pure renderer + mechanical stall flag

Deterministic formatter: snapshot → display lines, with an idle-based `⚠` stall marker. No ollama, no time-of-day dependence (now is injected).

**Files:**
- Create: `internal/activity/render.go`
- Test: `internal/activity/render_test.go`

**Interfaces:**
- Consumes: `AgentState`, `(*Board).Snapshot`, `(*Board).WatcherNote` (Task 2).
- Produces: `activity.Render(states []AgentState, note string, stall time.Duration, now time.Time) []string`; `activity.DefaultStall = 90 * time.Second`.

- [ ] **Step 1: Write the failing test**

Create `internal/activity/render_test.go`:

```go
package activity

import (
	"strings"
	"testing"
	"time"
)

func TestRenderLiveAndStalled(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	states := []AgentState{
		{Label: "claude", Last: "WebFetch: example.com", LastAt: now.Add(-2 * time.Second)},
		{Label: "codex", Last: "Bash: go test ./...", LastAt: now.Add(-94 * time.Second)},
	}
	lines := Render(states, "", 90*time.Second, now)
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "▸") || !strings.Contains(lines[0], "WebFetch: example.com") {
		t.Errorf("claude line wrong: %q", lines[0])
	}
	if !strings.Contains(lines[1], "⚠") || !strings.Contains(lines[1], "idle") {
		t.Errorf("codex should be stalled: %q", lines[1])
	}
}

func TestRenderDoneAndNote(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	states := []AgentState{{Label: "claude", Done: true, Elapsed: 3*time.Minute + 12*time.Second}}
	lines := Render(states, "both healthy", 90*time.Second, now)
	if !strings.Contains(lines[0], "✓ done") || !strings.Contains(lines[0], "3m12s") {
		t.Errorf("done line wrong: %q", lines[0])
	}
	last := lines[len(lines)-1]
	if !strings.Contains(last, "watch (ollama): both healthy") {
		t.Errorf("note line wrong: %q", last)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/activity/ -run TestRender -v`
Expected: FAIL — `Render` / `DefaultStall` undefined.

- [ ] **Step 3: Write the renderer**

Create `internal/activity/render.go`:

```go
package activity

import (
	"fmt"
	"time"
)

// DefaultStall is the idle duration past which an agent is flagged ⚠.
const DefaultStall = 90 * time.Second

// Render paints one line per agent plus an optional watcher note. now is
// injected so output is deterministic. A live agent shows "▸ <last> <idle> ago";
// past the stall threshold it flips to "⚠ idle <idle>"; a finished agent shows
// "✓ done (<elapsed>)".
func Render(states []AgentState, note string, stall time.Duration, now time.Time) []string {
	out := make([]string, 0, len(states)+1)
	for _, s := range states {
		if s.Done {
			out = append(out, fmt.Sprintf("%-8s ✓ done (%s)", s.Label, short(s.Elapsed)))
			continue
		}
		idle := now.Sub(s.LastAt)
		if idle > stall {
			out = append(out, fmt.Sprintf("%-8s ⚠ idle %-6s (last: %s)", s.Label, short(idle), s.Last))
			continue
		}
		out = append(out, fmt.Sprintf("%-8s ▸ %-30s %s ago", s.Label, s.Last, short(idle)))
	}
	if note != "" {
		out = append(out, "watch (ollama): "+note)
	}
	return out
}

// short renders a duration as 2s / 4m03s / 1h12m.
func short(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/activity/ -run TestRender -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/activity/render.go internal/activity/render_test.go
git commit -m "feat(activity): deterministic renderer with idle-based stall flag

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Runner and Manager write to the board

Move liveness capture into the `Runner` (independent of `OnEvent`), so background dispatch — which passes `nil` OnEvent — is instrumented too. `Manager` owns the board and marks `Done` with elapsed.

**Files:**
- Modify: `internal/agent/runner.go`
- Modify: `internal/agent/manager.go`
- Test: `internal/agent/runner_test.go`

**Interfaces:**
- Consumes: `activity.Board`, `activity.NewBoard`, `(*Board).Record`, `(*Board).Done`, `(*Board).Snapshot` (Task 2).
- Produces: `Runner.Board *activity.Board`, `Runner.Label string`; `Manager.Board *activity.Board`; `agent.summarize(Event) string`.

- [ ] **Step 1: Write the failing test**

Add to `internal/agent/runner_test.go` (the package already builds a scripted fake adapter/thread for `TestRunner`; reuse that harness — model this test on the existing streaming test in that file, feeding a script that emits a tool line):

```go
func TestRunnerRecordsActivityToBoard(t *testing.T) {
	board := activity.NewBoard()
	// scriptedAdapter is the existing test fake in this package that emits the
	// given stream-json lines; reuse whatever constructor the other tests use.
	ad := newScriptedAdapter(
		`{"type":"system","subtype":"init","session_id":"s1"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test"}}]}}`,
		`{"type":"result","session_id":"s1","result":"done","usage":{"output_tokens":3}}`,
	)
	th := &Thread{Name: "claude", CLI: "claude"}
	r := &Runner{Adapter: ad, Thread: th, Board: board, Label: "claude"}
	if _, err := r.Send(context.Background(), "hi", "", nil, false); err != nil {
		t.Fatalf("send: %v", err)
	}
	snap := board.Snapshot()
	if len(snap) != 1 || snap[0].Label != "claude" {
		t.Fatalf("board not populated: %+v", snap)
	}
	found := false
	for _, line := range board.Recent("claude") {
		if line == "Bash: go test" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tool activity not recorded: %v", board.Recent("claude"))
	}
}
```

> If `newScriptedAdapter` is not the exact fake name in `runner_test.go`, use the fake already present there — the existing `TestRunner` shows how this package scripts a streaming adapter. Do not invent a new fake if one exists.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/ -run TestRunnerRecordsActivity -v`
Expected: FAIL — `Runner.Board`/`Runner.Label` undefined.

- [ ] **Step 3: Add board fields and recording to the Runner**

In `internal/agent/runner.go`, add imports (`"github.com/ishaanbatra/styx/internal/activity"`) and fields:

```go
type Runner struct {
	Adapter Adapter
	Thread  *Thread
	WorkDir string
	Timeout time.Duration // 0 = no timeout
	OnEvent func(Event)   // streaming callback (REPL prints); may be nil
	Board   *activity.Board // liveness sink; may be nil
	Label   string          // board key (thread name); "" disables recording
}
```

In the streaming `for sc.Scan()` loop, right after the existing `if r.OnEvent != nil { r.OnEvent(ev) }` block, add:

```go
		if r.Board != nil && r.Label != "" {
			r.Board.Record(r.Label, summarize(ev))
		}
```

Add the summarizer at the bottom of `runner.go`:

```go
// summarize renders one event as a board activity line.
func summarize(ev Event) string {
	switch ev.Type {
	case EventInit:
		return "session started"
	case EventTool:
		if ev.Text != "" {
			return ev.Tool + ": " + ev.Text
		}
		return ev.Tool
	case EventText:
		return "thinking"
	case EventResult:
		return "finishing"
	}
	return ""
}
```

- [ ] **Step 4: Wire the board through the Manager**

In `internal/agent/manager.go`, add the field to `Manager`:

```go
	Board        *activity.Board // liveness board for /watch + heartbeat; nil ok
```

Add `"github.com/ishaanbatra/styx/internal/activity"` to the imports. In `Manager.Dispatch`, stamp the start, pass board+label into the Runner, and mark done:

```go
func (m *Manager) Dispatch(ctx context.Context, spec DispatchSpec, onEvent func(Event)) (TurnResult, error) {
	start := time.Now()
	ad, ok := m.Adapters[spec.CLI]
	if !ok {
		return TurnResult{}, fmt.Errorf("no adapter for CLI %q", spec.CLI)
	}
	name := spec.Thread
	if name == "" {
		name = spec.CLI
	}
	th := m.Threads.Get(name, spec.CLI)
	run := &Runner{Adapter: ad, Thread: th, WorkDir: m.Project.Path, Timeout: m.Timeout, OnEvent: onEvent, Board: m.Board, Label: name}
```

At the end of `Dispatch`, mark the agent done on the board on both the error and success return paths. Add this line immediately before `if err != nil {` (after `m.record(...)`):

```go
	if m.Board != nil {
		m.Board.Done(name, time.Since(start))
	}
```

(`m.record` already runs before this; `time` is already imported in manager.go.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/agent/ -run TestRunnerRecordsActivity -v && go test ./internal/agent/ -race`
Expected: PASS (whole package green under `-race`).

- [ ] **Step 6: Commit**

```bash
git add internal/agent/runner.go internal/agent/manager.go internal/agent/runner_test.go
git commit -m "feat(agent): capture liveness in the Runner, wire board through Manager

Instruments sync and background dispatch alike — liveness no longer flows
through the caller's OnEvent (which background tasks pass as nil).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Watch configuration

Add a `[watch]` section to the routing config with sane defaults and accessors.

**Files:**
- Modify: `internal/config/routing.go`
- Test: `internal/config/routing_test.go`

**Interfaces:**
- Produces: `routing.WatchCap{StallThresholdSeconds, IntervalSeconds int; OllamaEnabled bool}` on the routing struct as field `Watch`; accessors `(WatchCap).StallThreshold() time.Duration` (default 90s), `(WatchCap).Interval() time.Duration` (default 15s).

- [ ] **Step 1: Write the failing test**

Add to `internal/config/routing_test.go`:

```go
func TestWatchDefaults(t *testing.T) {
	var w WatchCap // zero value
	if w.StallThreshold() != 90*time.Second {
		t.Errorf("stall default = %v, want 90s", w.StallThreshold())
	}
	if w.Interval() != 15*time.Second {
		t.Errorf("interval default = %v, want 15s", w.Interval())
	}
	w2 := WatchCap{StallThresholdSeconds: 30, IntervalSeconds: 5}
	if w2.StallThreshold() != 30*time.Second || w2.Interval() != 5*time.Second {
		t.Errorf("explicit values not honored: %+v", w2)
	}
}
```

Ensure `"time"` is imported in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestWatchDefaults -v`
Expected: FAIL — `WatchCap` undefined.

- [ ] **Step 3: Add the config type and field**

In `internal/config/routing.go`, add the struct and hang it off the top-level routing struct (the struct that already holds `Brain` and `Ollama` — add the field next to them):

```go
// WatchCap configures live dispatch observability (styx watch / heartbeat).
type WatchCap struct {
	StallThresholdSeconds int  `toml:"stall_threshold_seconds"`
	IntervalSeconds       int  `toml:"interval_seconds"`
	OllamaEnabled         bool `toml:"ollama_enabled"`
}

// StallThreshold is the idle duration past which an agent is flagged; default 90s.
func (w WatchCap) StallThreshold() time.Duration {
	if w.StallThresholdSeconds > 0 {
		return time.Duration(w.StallThresholdSeconds) * time.Second
	}
	return 90 * time.Second
}

// Interval is the ollama watcher cadence; default 15s.
func (w WatchCap) Interval() time.Duration {
	if w.IntervalSeconds > 0 {
		return time.Duration(w.IntervalSeconds) * time.Second
	}
	return 15 * time.Second
}
```

Add the field to the routing struct (next to `Brain`/`Ollama`):

```go
	Watch WatchCap `toml:"watch"`
```

Add `"time"` to `routing.go` imports if absent.

> Note: `OllamaEnabled` defaults to `false` on a zero value. The watcher-start call sites (Tasks 7-8) treat "enabled" as `true` unless the user explicitly sets `ollama_enabled = false`; see `default_routing.go` seeding below.

- [ ] **Step 4: Seed the default in default_routing.go**

Per CLAUDE.md, `routing.toml` defaults live in `cmd/styx/default_routing.go`. Add a `[watch]` block to the seeded TOML string so new installs get the watcher on by default:

```toml

[watch]
stall_threshold_seconds = 90
interval_seconds = 15
ollama_enabled = true
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/ ./cmd/styx/ -run 'Watch|Routing|DefaultRouting' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/config/routing.go internal/config/routing_test.go cmd/styx/default_routing.go
git commit -m "feat(config): [watch] section for stall threshold, interval, ollama toggle

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Ollama watcher

A best-effort goroutine that periodically feeds cross-agent activity to local ollama and writes a health note to the board. Degrades silently when ollama is down.

**Files:**
- Create: `internal/activity/watcher.go`
- Test: `internal/activity/watcher_test.go`

**Interfaces:**
- Consumes: `(*Board).Snapshot`, `(*Board).Recent`, `(*Board).SetWatcherNote` (Task 2).
- Produces: `activity.Watcher{BaseURL, Model string; Board *Board; Interval time.Duration}`; `(*Watcher).Run(ctx context.Context)` (blocks until ctx done); `(*Watcher).pollOnce(ctx context.Context) error` (one cycle, exported-for-test via same package).

- [ ] **Step 1: Write the failing test**

Create `internal/activity/watcher_test.go`:

```go
package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWatcherPollWritesNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": "both agents healthy, no stalls"},
		})
	}))
	defer srv.Close()

	b := NewBoard()
	b.Record("claude", "Bash: go test")
	w := &Watcher{BaseURL: srv.URL, Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if b.WatcherNote() != "both agents healthy, no stalls" {
		t.Fatalf("note = %q", b.WatcherNote())
	}
}

func TestWatcherDegradesWhenOllamaDown(t *testing.T) {
	b := NewBoard()
	b.Record("claude", "Bash: go test")
	w := &Watcher{BaseURL: "http://127.0.0.1:1", Model: "test", Board: b} // nothing listening
	if err := w.pollOnce(context.Background()); err == nil {
		t.Fatalf("expected error when ollama unreachable")
	}
	if b.WatcherNote() != "" {
		t.Fatalf("note should stay empty on failure, got %q", b.WatcherNote())
	}
}

func TestWatcherNoAgentsNoCall(t *testing.T) {
	b := NewBoard()
	w := &Watcher{BaseURL: "http://127.0.0.1:1", Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatalf("empty board should be a no-op, got %v", err)
	}
}

func TestWatcherRunStopsOnContext(t *testing.T) {
	b := NewBoard()
	w := &Watcher{BaseURL: "http://127.0.0.1:1", Model: "test", Board: b, Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/activity/ -run TestWatcher -v`
Expected: FAIL — `Watcher` undefined.

- [ ] **Step 3: Write the watcher**

Create `internal/activity/watcher.go`:

```go
package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const watcherSystem = `You are a watch process observing parallel AI coding agents.
In 1-2 terse sentences, say whether they look healthy or stuck. Call out loops,
repeated identical actions, and long idles. Do not give advice; just report.`

// Watcher periodically summarizes cross-agent activity via local ollama and
// writes the result to the board. Strictly best-effort: every failure path
// leaves the mechanical layer (renderer + stall flag) untouched.
type Watcher struct {
	BaseURL  string // e.g. http://localhost:11434
	Model    string
	Board    *Board
	Interval time.Duration // 0 => 15s

	client *http.Client
}

func (w *Watcher) httpClient() *http.Client {
	if w.client == nil {
		w.client = &http.Client{Timeout: 30 * time.Second}
	}
	return w.client
}

// Run polls until ctx is cancelled. Poll errors are swallowed here on purpose:
// a down ollama must not spam or crash the session; the note simply stays stale.
func (w *Watcher) Run(ctx context.Context) {
	iv := w.Interval
	if iv <= 0 {
		iv = 15 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = w.pollOnce(ctx)
		}
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string         `json:"model"`
	Stream    bool           `json:"stream"`
	Think     bool           `json:"think"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options"`
	Messages  []chatMessage  `json:"messages"`
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// pollOnce runs one watch cycle. It is a no-op (nil error) when no agents are
// live. On success it stores the note; on failure it returns the error and
// leaves the existing note untouched.
func (w *Watcher) pollOnce(ctx context.Context) error {
	snap := w.Board.Snapshot()
	live := snap[:0]
	for _, s := range snap {
		if !s.Done {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil
	}

	var u strings.Builder
	for _, s := range live {
		fmt.Fprintf(&u, "agent %s (last: %s):\n", s.Label, s.Last)
		for _, line := range w.Board.Recent(s.Label) {
			fmt.Fprintf(&u, "  - %s\n", line)
		}
	}

	body, err := json.Marshal(chatRequest{
		Model:     w.Model,
		Stream:    false,
		Think:     false,
		KeepAlive: "30m",
		Options:   map[string]any{"temperature": 0},
		Messages: []chatMessage{
			{Role: "system", Content: watcherSystem},
			{Role: "user", Content: u.String()},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal watch request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build watch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("watch call (is ollama up?): %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read watch response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("ollama watch %d: %s", resp.StatusCode, string(raw))
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return fmt.Errorf("parse watch response: %w", err)
	}
	note := strings.TrimSpace(cr.Message.Content)
	if note != "" {
		w.Board.SetWatcherNote(note)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/activity/ -run TestWatcher -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/activity/watcher.go internal/activity/watcher_test.go
git commit -m "feat(activity): best-effort ollama watcher writes cross-agent health notes

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Live inline renderer loop

A TTY-aware refresh loop that repaints the board in place. Testable via a single `paint()` against a buffer.

**Files:**
- Create: `internal/activity/live.go`
- Test: `internal/activity/live_test.go`

**Interfaces:**
- Consumes: `(*Board).Snapshot`, `(*Board).WatcherNote`, `Render`, `DefaultStall` (Tasks 2-3).
- Produces: `activity.NewLiveRenderer(w io.Writer, b *Board, stall time.Duration) *LiveRenderer`; `(*LiveRenderer).Start()`, `(*LiveRenderer).Stop()`, `(*LiveRenderer).paint()`.

- [ ] **Step 1: Write the failing test**

Create `internal/activity/live_test.go`:

```go
package activity

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestLiveRendererPaint(t *testing.T) {
	b := NewBoard()
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return now })
	b.Record("claude", "Bash: go test")

	var buf bytes.Buffer
	lr := NewLiveRenderer(&buf, b, DefaultStall)
	lr.now = func() time.Time { return now }
	lr.paint()

	if !strings.Contains(buf.String(), "claude") || !strings.Contains(buf.String(), "Bash: go test") {
		t.Fatalf("paint output missing agent: %q", buf.String())
	}
}

func TestLiveRendererStartStop(t *testing.T) {
	b := NewBoard()
	var buf bytes.Buffer
	lr := NewLiveRenderer(&buf, b, DefaultStall)
	lr.Start()
	lr.Stop() // must not hang or panic
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/activity/ -run TestLiveRenderer -v`
Expected: FAIL — `NewLiveRenderer` undefined.

- [ ] **Step 3: Write the live renderer**

Create `internal/activity/live.go`:

```go
package activity

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
)

// LiveRenderer repaints the board to w on a ticker. On a TTY it clears the
// previous frame in place; on a non-TTY it appends frames (quiet cadence). One
// per session; Start/Stop bracket a watched span.
type LiveRenderer struct {
	w     io.Writer
	board *Board
	stall time.Duration
	isTTY bool
	now   func() time.Time

	mu   sync.Mutex
	prev int // lines painted last frame (TTY clear)
	stop chan struct{}
	done chan struct{}
}

// NewLiveRenderer builds a renderer. TTY detection mirrors internal/progress.
func NewLiveRenderer(w io.Writer, b *Board, stall time.Duration) *LiveRenderer {
	isTTY := false
	if f, ok := w.(*os.File); ok {
		isTTY = isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
	}
	return &LiveRenderer{w: w, board: b, stall: stall, isTTY: isTTY, now: time.Now}
}

// paint writes one frame.
func (l *LiveRenderer) paint() {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := Render(l.board.Snapshot(), l.board.WatcherNote(), l.stall, l.now())
	if l.isTTY && l.prev > 0 {
		fmt.Fprintf(l.w, "\033[%dA", l.prev) // cursor up prev lines
	}
	for _, line := range lines {
		if l.isTTY {
			fmt.Fprint(l.w, "\r\033[K")
		}
		fmt.Fprintln(l.w, line)
	}
	l.prev = len(lines)
}

// Start begins repainting every second until Stop.
func (l *LiveRenderer) Start() {
	l.stop = make(chan struct{})
	l.done = make(chan struct{})
	go func() {
		defer close(l.done)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				l.paint()
			}
		}
	}()
}

// Stop halts repainting and paints a final frame.
func (l *LiveRenderer) Stop() {
	if l.stop == nil {
		return
	}
	close(l.stop)
	<-l.done
	l.paint()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/activity/ -run TestLiveRenderer -race -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/activity/live.go internal/activity/live_test.go
git commit -m "feat(activity): TTY-aware live board renderer

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Wire the board into the REPL and conductor

Construct one board per session, inject it into the Manager(s), start the watcher, render the live board during a dispatch, and add a `/watch` REPL command.

**Files:**
- Modify: `cmd/styx/repl.go`
- Modify: `cmd/styx/mcp_conductor.go`
- Test: `cmd/styx/repl_test.go`

**Interfaces:**
- Consumes: `activity.NewBoard`, `activity.Watcher`, `activity.NewLiveRenderer`, `activity.Render`, `Manager.Board` (Tasks 2-7); `routing.WatchCap` accessors (Task 5).
- Produces: `replSession.board *activity.Board`; `/watch` command output.

- [ ] **Step 1: Write the failing test**

Add to `cmd/styx/repl_test.go` a test that the session board renders via `/watch`. Model it on an existing repl_test that drives a command and captures `s.println` output. Minimum viable assertion:

```go
func TestReplWatchCommandRendersBoard(t *testing.T) {
	s := newTestSession(t) // existing helper in repl_test.go
	s.board.Record("claude", "Bash: go test")
	out := s.runCommandCapture("/watch") // existing capture helper
	if !strings.Contains(out, "claude") {
		t.Fatalf("/watch did not render board: %q", out)
	}
}
```

> Use the session/capture helpers already present in `repl_test.go`. If none exists, assert on `activity.Render(s.board.Snapshot(), "", activity.DefaultStall, time.Now())` directly instead of driving the command, and cover the command dispatch manually.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestReplWatch -v`
Expected: FAIL — `s.board` undefined / `/watch` unknown.

- [ ] **Step 3: Add the board to the session and Manager**

In `cmd/styx/repl.go`, add a `board *activity.Board` field to `replSession` and initialize it where the session is constructed (alongside `runID`/`tracker`):

```go
	board: activity.NewBoard(),
```

Import `"github.com/ishaanbatra/styx/internal/activity"`. In the Manager construction at `repl.go:97`, add:

```go
		Board:        s.board,
```

- [ ] **Step 4: Start the watcher for the session**

Where the session starts its background context (near where `s.runID` and the root context are set up), start the watcher when enabled:

```go
	if s.a == nil || s.a.routing.Watch.OllamaEnabled || s.watchDefaultOn {
		w := &activity.Watcher{
			BaseURL:  "http://localhost:11434",
			Model:    s.watchModel(), // routing.Brain.Model
			Board:    s.board,
			Interval: s.watchInterval(), // routing.Watch.Interval()
		}
		go w.Run(s.ctx)
	}
```

Add small helpers on `replSession` that read from the loaded routing (fall back to defaults when routing is absent in tests):

```go
func (s *replSession) watchModel() string {
	if s.a != nil {
		return s.a.routing.Brain.Model
	}
	return ""
}
func (s *replSession) watchInterval() time.Duration {
	if s.a != nil {
		return s.a.routing.Watch.Interval()
	}
	return 15 * time.Second
}
```

> Match the actual field names in `replSession` for the loaded app/routing (grep `s.a`/`routing` usage in `repl.go`). The watcher goroutine ends when the session context is cancelled.

- [ ] **Step 5: Render the live board during dispatch**

In the dispatch path (`repl.go:286`), bracket the streaming dispatch with a live renderer so the user sees per-agent heartbeat instead of a silent wait:

```go
	lr := activity.NewLiveRenderer(s.errWriter(), s.board, s.watchStall())
	lr.Start()
	res, err := bp.mgr.Dispatch(ctx, agent.DispatchSpec{
		Thread:     d.Thread,
		CLI:        d.Thread,
		Model:      model,
		Message:    d.Message,
		Extra:      d.CLIOptions,
		ExtraRoots: roots,
		ReadOnly:   d.Risk == brain.RiskRead,
	}, s.printEvent)
	lr.Stop()
```

Add `watchStall()` (returns `s.a.routing.Watch.StallThreshold()` or `activity.DefaultStall`) and `errWriter()` (the session's stderr writer; match how `s.print`/`progress` already get their writer). Keep `s.printEvent` — the live board and the existing streaming coexist (board is liveness, printEvent is content).

- [ ] **Step 6: Add the `/watch` command**

In the REPL command switch (grep for where `/status` or other `/`-commands are handled in `repl.go`), add:

```go
	case "/watch":
		for _, line := range activity.Render(s.board.Snapshot(), s.board.WatcherNote(), s.watchStall(), time.Now()) {
			s.println(line)
		}
		return nil
```

- [ ] **Step 7: Wire the board into the conductor Manager**

In `cmd/styx/mcp_conductor.go` at the Manager construction (`:245`), add a board to the `managed` struct and inject it:

```go
	board := activity.NewBoard()
	m := &managed{mem: mem, board: board, mgr: &agent.Manager{
		...
		Board: board,
	}}
```

Add `board *activity.Board` to the `managed` struct definition and import `activity`. Start a watcher goroutine off the server root context for the conductor, same shape as Step 4, when `d.a.routing.Watch.OllamaEnabled`.

- [ ] **Step 8: Enrich the piggyback bg line with last-event + idle**

Per spec Component 6(b), the compact `bg` status the conductor sees on every tool result (`taskRegistry.StatusLine` in `cmd/styx/mcp_tasks.go`) should carry each running task's last action, not just its elapsed clock. Give the registry a board reference and read it per running task (the board is keyed by thread label; `taskSpec.Thread` is that key). Add a `board *activity.Board` field to `taskRegistry`, set it in `newTaskRegistry` (thread the conductor's `board` through the constructor call), and in the `taskRunning` branch of `StatusLine` append the board's last activity for `t.Spec.Thread`:

```go
		case taskRunning:
			line := fmt.Sprintf("%s running (%s, %s)", t.ID, t.Spec.CLI, elapsedShort(time.Since(t.Started)))
			if r.board != nil {
				for _, s := range r.board.Snapshot() {
					if s.Label == t.Spec.Thread && s.Last != "" {
						line += " — " + s.Last
						break
					}
				}
			}
			parts = append(parts, line)
```

Keep `r.board` nil-safe (unit tests construct the registry without a board). Import `activity` in `mcp_tasks.go`.

- [ ] **Step 9: Run tests and build**

Run: `go build ./... && go test ./cmd/styx/ -run TestReplWatch -v`
Expected: build OK; test PASS.

- [ ] **Step 10: Commit**

```bash
git add cmd/styx/repl.go cmd/styx/mcp_conductor.go cmd/styx/mcp_tasks.go cmd/styx/repl_test.go
git commit -m "feat(repl): live heartbeat board during dispatch + /watch + session watcher

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Disk mirror + cross-process `styx watch`

Throttled mirror of the board to disk so a second `styx` process can render it, plus the `styx watch` verb and the doc drift updates.

**Files:**
- Create: `internal/activity/mirror.go`
- Create: `cmd/styx/watch.go`
- Test: `internal/activity/mirror_test.go`
- Modify: `cmd/styx/dispatch.go` (register the verb)
- Modify: `cmd/styx/mcp_conductor.go` or `cmd/styx/repl.go` (drive throttled mirror writes)
- Modify: `README.md` (verb table), `docs/ARCHITECTURE.md` (mirror layout)

**Interfaces:**
- Consumes: `(*Board).Snapshot`, `(*Board).WatcherNote`, `Render`, `DefaultStall` (Tasks 2-3); `internal/paths` for the state dir.
- Produces: `activity.WriteMirror(path string, states []AgentState, note string) error`; `activity.ReadMirror(path string) (states []AgentState, note string, err error)`; `activity.MirrorThrottle(b *Board, path string, min time.Duration) func() error` (returns a debounced writer to call after each dispatch event; returns any write error to narrate, never swallowed); `cmdWatch(args []string) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/activity/mirror_test.go`:

```go
package activity

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMirrorRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "board.json")
	states := []AgentState{
		{Label: "claude", Last: "Bash: go test", LastAt: time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)},
	}
	if err := WriteMirror(path, states, "healthy"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, note, err := ReadMirror(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if note != "healthy" || len(got) != 1 || got[0].Label != "claude" || got[0].Last != "Bash: go test" {
		t.Fatalf("round trip mismatch: %+v note=%q", got, note)
	}
}

func TestReadMirrorMissing(t *testing.T) {
	_, _, err := ReadMirror(filepath.Join(t.TempDir(), "nope.json"))
	if err == nil {
		t.Fatal("want error for missing mirror")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/activity/ -run TestMirror -v`
Expected: FAIL — `WriteMirror`/`ReadMirror` undefined.

- [ ] **Step 3: Write the mirror**

Create `internal/activity/mirror.go`:

```go
package activity

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// mirrorFile is the on-disk shape a second process reads. Timestamps survive
// so the reader computes idle/stall against its own clock.
type mirrorFile struct {
	Note   string          `json:"note"`
	States []mirrorState   `json:"states"`
}

type mirrorState struct {
	Label   string        `json:"label"`
	Last    string        `json:"last"`
	LastAt  time.Time     `json:"last_at"`
	Done    bool          `json:"done"`
	Elapsed time.Duration `json:"elapsed"`
}

// WriteMirror writes the board snapshot atomically (tmp + rename).
func WriteMirror(path string, states []AgentState, note string) error {
	mf := mirrorFile{Note: note}
	for _, s := range states {
		mf.States = append(mf.States, mirrorState{
			Label: s.Label, Last: s.Last, LastAt: s.LastAt, Done: s.Done, Elapsed: s.Elapsed,
		})
	}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mirror: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write mirror tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename mirror: %w", err)
	}
	return nil
}

// ReadMirror loads a board snapshot written by WriteMirror.
func ReadMirror(path string) ([]AgentState, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read mirror: %w", err)
	}
	var mf mirrorFile
	if err := json.Unmarshal(b, &mf); err != nil {
		return nil, "", fmt.Errorf("parse mirror: %w", err)
	}
	out := make([]AgentState, 0, len(mf.States))
	for _, s := range mf.States {
		out = append(out, AgentState{
			Label: s.Label, Last: s.Last, LastAt: s.LastAt, Done: s.Done, Elapsed: s.Elapsed,
		})
	}
	return out, mf.Note, nil
}

// MirrorThrottle returns a debounced writer: calling it mirrors the board to
// path at most once per min interval. Write failures are returned to the caller
// to narrate (never swallowed). The returned func is safe for concurrent use.
func MirrorThrottle(b *Board, path string, min time.Duration) func() error {
	var mu sync.Mutex
	var last time.Time
	return func() error {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if now.Sub(last) < min {
			return nil
		}
		last = now
		return WriteMirror(path, b.Snapshot(), b.WatcherNote())
	}
}
```

- [ ] **Step 4: Write the `styx watch` command**

Create `cmd/styx/watch.go`:

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/paths"
)

// cmdWatch renders the live dispatch board written by a running styx session or
// `styx mcp` server. Reads the on-disk mirror; refreshes until interrupted.
func cmdWatch(args []string) error {
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return err
	}
	path := filepath.Join(paths.StateDir(), "watch", proj.ID+".json")

	for {
		states, note, err := activity.ReadMirror(path)
		if err != nil {
			if os.IsNotExist(errUnwrap(err)) {
				fmt.Println("(no live activity — no styx session is dispatching in this project)")
				return nil
			}
			return err
		}
		fmt.Print("\033[H\033[2J") // clear screen
		for _, line := range activity.Render(states, note, activity.DefaultStall, time.Now()) {
			fmt.Println(line)
		}
		time.Sleep(time.Second)
	}
}

// errUnwrap unwraps to the root error for os.IsNotExist checks.
func errUnwrap(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}
```

> `paths.StateDir()` — use the existing state-dir helper in `internal/paths` (the same root `mcp_tasks.go` writes task mirrors under: `~/.config/styx/state/`). If the helper has a different name, use it and keep the `watch/<projectID>.json` subpath. Ensure the `watch` subdir exists on the writer side (Step 6).

- [ ] **Step 5: Register the verb**

In `cmd/styx/dispatch.go`, add to the **first** switch (the pre-`loadApp` block, next to `case "runs":`) — `styx watch` only reads files, no app needed:

```go
	case "watch":
		return cmdWatch(args)
```

- [ ] **Step 6: Drive throttled mirror writes from the session**

In `cmd/styx/repl.go` (and the conductor in `mcp_conductor.go`), create the throttle after the board and call it from `s.printEvent` / the dispatch event path so the mirror updates as events arrive:

```go
	mirrorDir := filepath.Join(paths.StateDir(), "watch")
	if err := paths.EnsureDir(mirrorDir); err != nil {
		logStatus("watch mirror dir: %v", err)
	}
	s.mirror = activity.MirrorThrottle(s.board, filepath.Join(mirrorDir, focusProjectID+".json"), 2*time.Second)
```

In `printEvent` (or right after `lr.paint` triggers), call the throttle and narrate failures rather than swallowing:

```go
	if s.mirror != nil {
		if err := s.mirror(); err != nil {
			logStatus("watch mirror: %v", err)
		}
	}
```

Add `mirror func() error` to `replSession`. Use the session's focus project id for the filename so `styx watch` in that project finds it.

- [ ] **Step 7: Run tests and build**

Run: `go test ./internal/activity/ -run TestMirror -v && go build ./... && ./... ` — build only:
`go build ./...`
Expected: PASS + build OK.

- [ ] **Step 8: Update the docs (drift contract)**

- `README.md`: add to the verb table:

```
| `styx watch` | Live dispatch board for the current project — per-agent heartbeat + stall flags, refreshed from a running session or `styx mcp`. |
```

- `docs/ARCHITECTURE.md`: under the Activity subsystem, document the mirror layout:

```
Live board mirror: `~/.config/styx/state/watch/<projectID>.json` (atomic
tmp+rename, throttled ~2s), read by `styx watch` in a separate process.
```

Bump both docs' `last_verified` to `2026-07-07`.

- [ ] **Step 9: Commit**

```bash
git add internal/activity/mirror.go internal/activity/mirror_test.go cmd/styx/watch.go cmd/styx/dispatch.go cmd/styx/repl.go cmd/styx/mcp_conductor.go README.md docs/ARCHITECTURE.md
git commit -m "feat(watch): cross-process styx watch via throttled board mirror

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Final Verification

- [ ] **Full test suite:** `go test ./... -race` — all green.
- [ ] **Vet + format:** `go vet ./... && gofmt -l .` — no output.
- [ ] **Manual smoke (heartbeat):** in a REPL session, dispatch a research-style prompt that fans out to claude + codex; confirm per-agent lines update and a `watch (ollama): …` note appears within ~15s (with ollama running).
- [ ] **Manual smoke (cross-process):** during that dispatch, run `styx watch` in a second terminal in the same project; confirm the same board renders and refreshes.
- [ ] **Degradation:** stop ollama (`osascript -e 'quit app "Ollama"'` or kill the server), repeat the dispatch; confirm the mechanical per-agent lines and `⚠` stall flag still work and no errors spam the session.
- [ ] **Resume safety:** confirm `styx auto --resume` and `styx runs ls` still work — this plan adds no fields to `pipeline` state, only to in-memory/agent structs.
