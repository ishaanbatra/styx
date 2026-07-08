# Awaited Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Conductor dispatches wait for their work by default — streaming live board-derived progress into the Claude Code chat and returning findings inline — with parallel fan-out (`dispatch_parallel`), a concurrent MCP server with cancellation (Esc detaches, never kills), and a mechanical pulse that keeps `styx watch` live with no ollama dependency.

**Architecture:** Awaited = observed background: an awaited dispatch spawns ordinary `taskRegistry` background tasks and a per-call awaiter loop watches them until terminal, emitting `notifications/progress` lines composed from the `activity.Board` + registry + ollama watcher note. The MCP server runs `tools/call` handlers on goroutines and honors `notifications/cancelled`. A server-side pulse goroutine refreshes the disk mirror every second while anything is live.

**Tech Stack:** Go 1.22, stdlib only (no new deps). Spec: `docs/superpowers/specs/2026-07-08-styx-awaited-dispatch-design.md`.

## Global Constraints

- Channels are CLIs/HTTP, never SDKs; never call provider HTTP APIs for claude/codex/agy.
- Never swallow errors (`x, _ :=`); wrap with context: `fmt.Errorf("load registry: %w", err)`. Documented exceptions only (e.g. response write after host hang-up), each with a comment.
- Every subprocess bounded: agent turns already run under `Manager.Timeout` (default 10 min) — do not remove that path.
- File writes atomic (tmp + rename) via existing helpers (`activity.WriteMirror`, `writeTaskFile`).
- Table-driven tests with `t.Run`; fakes over mocks (`testdata/fakeagent` for CLI agents; no sleeps for lifecycle control — use `blockingRun`/`waitFor` from `cmd/styx/mcp_tasks_test.go`).
- Status/narration to stderr via `logStatus`; results to stdout (JSON-RPC only on stdout in MCP mode).
- The ollama watcher (`internal/activity/watcher.go`) is NOT modified: it stays narration-only, 15s cadence, gated on `watch.ollama_enabled`.
- The mechanical layer (pulse, awaiter heartbeats, mirror) must work with ollama completely off.
- Drift contract: every task's commit includes its `docs/ARCHITECTURE.md` update and bumps `last_verified: 2026-07-08` in that file's frontmatter (it is already 2026-07-08 — leave as is; bump if the calendar date has moved).
- Before every commit: `gofmt -w . && go vet ./...`.
- No new config knobs: `awaitTick` = 1s and `pulseTick` = 1s are package `var`s (overridable in tests), mirror throttle stays 2s.

## File Structure

- `internal/mcpserver/server.go` — concurrent `tools/call`, `notifications/cancelled`, `Tool.Serial` (Task 1)
- `internal/mcpserver/server_test.go` — concurrency/cancellation/serial tests (Task 1)
- `cmd/styx/mcp.go` — mark `refresh_intel` Serial (Task 1)
- `cmd/styx/mcp_await.go` — NEW: awaiter loop + heartbeat line composer (Task 2)
- `cmd/styx/mcp_await_test.go` — NEW (Task 2)
- `cmd/styx/mcp_conductor.go` — pulse goroutine (Task 3); dispatch handler rewrite + `dispatch_parallel` + Serial on `pipeline_run` (Tasks 4–5)
- `cmd/styx/mcp_conductor_test.go` — fixture updates, new awaited/parallel tests (Tasks 3–5)
- `cmd/styx/mcp_tasks.go` — remove now-unused `Busy` (Task 4)
- `cmd/styx/mcp_tasks_test.go` — remove `Busy` tests (Task 4)
- `docs/ARCHITECTURE.md`, `README.md`, `docs/superpowers/specs/2026-07-07-styx-dispatch-observability-design.md` — docs (each task + Task 6)

---

### Task 1: Concurrent MCP server with cancellation and Serial lane

**Files:**
- Modify: `internal/mcpserver/server.go`
- Modify: `internal/mcpserver/server_test.go`
- Modify: `cmd/styx/mcp.go` (the `refresh_intel` tool literal)
- Modify: `docs/ARCHITECTURE.md` (section `## MCP server (internal/mcpserver + cmd/styx/mcp.go + cmd/styx/mcp_conductor.go)`, ~line 1145)

**Interfaces:**
- Consumes: nothing new.
- Produces: `Tool.Serial bool` field; behavior contract used by Tasks 4–5: a `tools/call` handler's `ctx` is cancelled by `notifications/cancelled` with the matching `requestId`, and multiple `tools/call`s run concurrently. `ProgressFn` unchanged.

- [ ] **Step 1: Write the failing tests**

Append to `internal/mcpserver/server_test.go` (add `"sync/atomic"` and `"time"` to imports):

```go
// TestConcurrentToolCalls proves two tools/call requests run at once: the
// slow tool (sent first) blocks until the fast tool — which the serial
// server would never reach — releases it. Completing at all is the
// concurrency proof; once the gate opens both goroutines race to the
// encoder, so response ORDER is deliberately not asserted.
func TestConcurrentToolCalls(t *testing.T) {
	gate := make(chan struct{})
	slow := Tool{Name: "slow", Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-gate
		return map[string]any{"who": "slow"}, nil
	}}
	fast := Tool{Name: "fast", Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(gate)
		return map[string]any{"who": "fast"}, nil
	}}
	lines := serve(t, New("t", "0", []Tool{slow, fast}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fast","arguments":{}}}`)
	if len(lines) != 2 {
		t.Fatalf("want 2 responses, got %d: %v", len(lines), lines)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{`"id":1`, `"id":2`, "slow", "fast"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("responses must cover both calls (missing %q): %v", want, lines)
		}
	}
}

// TestCancelledNotificationCancelsCall: notifications/cancelled must cancel
// the matching in-flight call's context — this is how a host-side interrupt
// (Esc in Claude Code) reaches a long-running handler.
func TestCancelledNotificationCancelsCall(t *testing.T) {
	slow := Tool{Name: "slow", Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return map[string]any{"cancelled": true}, nil
	}}
	lines := serve(t, New("t", "0", []Tool{slow}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow","arguments":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	if len(lines) != 1 || !strings.Contains(lines[0], `"cancelled":true`) {
		t.Fatalf("cancelled call must unblock and answer, got: %v", lines)
	}
}

// TestEOFCancelsInflightCalls: when the host closes stdin, Serve cancels
// every in-flight call, waits for handlers to wind down, and still writes
// their responses before returning.
func TestEOFCancelsInflightCalls(t *testing.T) {
	slow := Tool{Name: "slow", Handler: func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return map[string]any{"detached": true}, nil
	}}
	lines := serve(t, New("t", "0", []Tool{slow}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow","arguments":{}}}`)
	if len(lines) != 1 || !strings.Contains(lines[0], `"detached":true`) {
		t.Fatalf("EOF must cancel and drain in-flight calls, got: %v", lines)
	}
}

// TestSerialToolsDoNotOverlap: Tool.Serial handlers share one lane.
func TestSerialToolsDoNotOverlap(t *testing.T) {
	var active, overlaps atomic.Int32
	mk := func(name string) Tool {
		return Tool{Name: name, Serial: true, Handler: func(context.Context, json.RawMessage) (any, error) {
			if active.Add(1) > 1 {
				overlaps.Add(1)
			}
			time.Sleep(20 * time.Millisecond)
			active.Add(-1)
			return "ok", nil
		}}
	}
	serve(t, New("t", "0", []Tool{mk("a"), mk("b")}),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"b","arguments":{}}}`)
	if overlaps.Load() != 0 {
		t.Fatalf("Serial tools overlapped %d time(s)", overlaps.Load())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mcpserver/ -run 'TestConcurrentToolCalls|TestCancelledNotificationCancelsCall|TestEOFCancelsInflightCalls|TestSerialToolsDoNotOverlap' -v -timeout 20s`
Expected: compile error `unknown field Serial`, and after a temporary `Serial bool` stub the concurrency/cancellation tests DEADLOCK (timeout panic) — the serial server never reaches the releasing call. Either failure mode is acceptable evidence.

- [ ] **Step 3: Implement the concurrent server**

In `internal/mcpserver/server.go`:

Add to `Tool`:

```go
// Tool is a single callable exposed over MCP.
type Tool struct {
	Name        string
	Description string
	InputSchema any // serialized as a JSON Schema object in tools/list
	Handler     func(ctx context.Context, args json.RawMessage) (any, error)

	// Serial routes this tool through a single shared lane: Serial handlers
	// never run concurrently with each other. Set it on handlers not audited
	// for concurrent use (e.g. whole-pipeline runners); everything else runs
	// in parallel now that tools/call is handled per-goroutine.
	Serial bool
}
```

Extend `Server` and `New`:

```go
// Server is a registry of tools served over one MCP stdio connection.
type Server struct {
	name    string
	version string
	tools   []Tool
	byName  map[string]Tool

	mu  sync.Mutex    // serializes writes to enc
	enc *json.Encoder // set for the duration of Serve

	serialMu sync.Mutex // shared lane for Tool.Serial handlers

	callsMu sync.Mutex
	calls   map[string]context.CancelFunc // in-flight tools/call by request id
	wg      sync.WaitGroup                // outstanding tools/call goroutines
}

// New builds a Server with the given identity and tool set.
func New(name, version string, tools []Tool) *Server {
	s := &Server{name: name, version: version, tools: tools,
		byName: make(map[string]Tool, len(tools)),
		calls:  map[string]context.CancelFunc{}}
	for _, t := range tools {
		s.byName[t.Name] = t
	}
	return s
}
```

Replace `Serve` and `handle` (and delete `handle`'s notification bool — notifications are filtered in the loop now):

```go
// Serve reads JSON-RPC messages from in and writes responses to out until EOF.
// tools/call requests run on their own goroutine so a minutes-long awaited
// dispatch cannot stall other calls or the read loop — the loop must stay
// free to read notifications/cancelled. Everything else answers inline.
// On EOF the host has hung up: every in-flight call is cancelled and awaited
// before returning, so handlers wind down (an awaited dispatch detaches; its
// background tasks belong to the caller's root context, not to Serve).
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	// A line larger than this cap makes scanner.Err() return bufio.ErrTooLong and
	// Serve returns — acceptable for a local, single-host v1 (no untrusted input).
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate large tool payloads
	s.enc = json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			if encErr := s.write(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32700, Message: "parse error"}}); encErr != nil {
				return fmt.Errorf("write parse error: %w", encErr)
			}
			continue
		}
		if len(req.ID) == 0 {
			s.handleNotification(req)
			continue
		}
		if req.Method == "tools/call" {
			s.startCall(ctx, req)
			continue
		}
		if err := s.write(s.handle(req)); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	s.cancelInflight()
	s.wg.Wait()
	return scanner.Err()
}

// startCall runs one tools/call on its own goroutine, tracked by request id
// for notifications/cancelled. The response write error is deliberately
// dropped: a failed write means the host hung up, and the read loop is about
// to see EOF and return on its own.
func (s *Server) startCall(ctx context.Context, req rpcRequest) {
	callCtx, cancel := context.WithCancel(ctx)
	key := string(req.ID)
	s.callsMu.Lock()
	s.calls[key] = cancel
	s.callsMu.Unlock()
	s.wg.Add(1)
	go func() {
		defer func() {
			s.callsMu.Lock()
			delete(s.calls, key)
			s.callsMu.Unlock()
			cancel()
			s.wg.Done()
		}()
		result, rpcErr := s.callTool(callCtx, req.Params)
		_ = s.write(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
	}()
}

// handleNotification processes id-less messages. Only notifications/cancelled
// is meaningful today: it cancels the matching in-flight tools/call, which is
// how a host-side interrupt (Esc) reaches a long-running handler. The id is
// matched on its raw JSON form — hosts cancel with the same id shape they
// called with.
func (s *Server) handleNotification(req rpcRequest) {
	if req.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return // malformed cancel: nothing to correlate, nothing to answer
	}
	s.callsMu.Lock()
	cancel, ok := s.calls[string(p.RequestID)]
	s.callsMu.Unlock()
	if ok {
		cancel()
	}
}

// cancelInflight cancels every outstanding tools/call (EOF path).
func (s *Server) cancelInflight() {
	s.callsMu.Lock()
	defer s.callsMu.Unlock()
	for _, cancel := range s.calls {
		cancel()
	}
}

// handle answers the inline (non-tools/call) request types.
func (s *Server) handle(req rpcRequest) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		resp.Result = map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.toolList()}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp
}
```

In `callTool`, after the `tool, ok := s.byName[p.Name]` lookup succeeds, add the serial lane before the progress-token block:

```go
	if tool.Serial {
		s.serialMu.Lock()
		defer s.serialMu.Unlock()
	}
```

- [ ] **Step 4: Run the mcpserver tests**

Run: `go test ./internal/mcpserver/ -v -timeout 60s`
Expected: ALL PASS (new tests plus every pre-existing test — `TestServe_NotificationGetsNoResponse`, `TestProgressNotifications`, etc. must still pass unchanged).

- [ ] **Step 5: Mark the two pipeline-running tools Serial**

In `cmd/styx/mcp.go`, find the tool literal with `Name: "refresh_intel"` inside `mcpTools` and add `Serial: true,` after its `Name:` line. In `cmd/styx/mcp_conductor.go`, find `Name: "pipeline_run"` inside `conductorTools` and add `Serial: true,` after its `Name:` line. Both drive multi-stage pipelines over shared app state and are not audited for concurrent use.

Run: `go build ./... && go test ./cmd/styx/ -timeout 300s`
Expected: build OK, tests PASS.

- [ ] **Step 6: Update ARCHITECTURE.md (same commit)**

In `docs/ARCHITECTURE.md`, section `## MCP server (internal/mcpserver + cmd/styx/mcp.go + cmd/styx/mcp_conductor.go)`: find the sentence describing request handling (it describes a serial loop) and replace/extend it with:

```
`tools/call` requests run concurrently, one goroutine per call, tracked by
request id; `notifications/cancelled` cancels the matching call's context
(this is how a host-side Esc reaches a long-running handler). `initialize`
and `tools/list` answer inline. On EOF the server cancels and drains every
in-flight call before returning. Tools whose handlers are not audited for
concurrent use set `Tool.Serial` and share a single lane (`pipeline_run`,
`refresh_intel`).
```

- [ ] **Step 7: Commit**

```bash
gofmt -w . && go vet ./...
git add internal/mcpserver/ cmd/styx/mcp.go cmd/styx/mcp_conductor.go docs/ARCHITECTURE.md
git commit -m "feat(mcpserver): concurrent tools/call with cancellation and Serial lane

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Awaiter — observe registry tasks, stream board-derived progress

**Files:**
- Create: `cmd/styx/mcp_await.go`
- Create: `cmd/styx/mcp_await_test.go`
- Modify: `docs/ARCHITECTURE.md` (section `### Background task registry (cmd/styx/mcp_tasks.go)`, ~line 1535)

**Interfaces:**
- Consumes: `taskRegistry` (`Get`, `Snapshot`, `Claim` via `collectOne`), `bgTask` + state consts, `elapsedShort` (all `cmd/styx/mcp_tasks.go`); `d.board *activity.Board`; `agent.BoardLabel(projectID, thread) string`; `activity.DefaultStall`; `collectOne(reg, tk) map[string]any` (`cmd/styx/mcp_conductor.go` — claims terminal tasks).
- Produces (used by Tasks 4–5):
  - `var awaitTick = 1 * time.Second`
  - `type awaitOutcome struct { Detached bool; Results []map[string]any }`
  - `func (d *conductorDeps) awaitTasks(ctx context.Context, ids []string, notify func(float64, string)) awaitOutcome`
  - `func taskHeartbeat(tk bgTask, states map[string]activity.AgentState, now time.Time) string`
  - `func isTerminal(state string) bool`

- [ ] **Step 1: Write the failing tests**

Create `cmd/styx/mcp_await_test.go`:

```go
package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/agent"
)

// fastAwait shrinks the awaiter tick for tests. Not parallel-safe: tests
// using it must not call t.Parallel().
func fastAwait(t *testing.T) {
	t.Helper()
	old := awaitTick
	awaitTick = 5 * time.Millisecond
	t.Cleanup(func() { awaitTick = old })
}

func TestTaskHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	label := agent.BoardLabel("p1", "claude")
	for _, tc := range []struct {
		name   string
		tk     bgTask
		states map[string]activity.AgentState
		want   string
	}{
		{"queued behind", bgTask{ID: "t2", State: taskQueued, QueuedBehind: "t1",
			Spec: taskSpec{CLI: "claude"}, Created: now.Add(-12 * time.Second)},
			nil, "t2 claude queued behind t1 (12s)"},
		{"queued at cap", bgTask{ID: "t3", State: taskQueued,
			Spec: taskSpec{CLI: "codex"}, Created: now.Add(-3 * time.Second)},
			nil, "t3 codex queued at cap (3s)"},
		{"running with fresh board entry", bgTask{ID: "t1", State: taskRunning,
			Spec: taskSpec{ProjectID: "p1", Thread: "claude", CLI: "claude"}},
			map[string]activity.AgentState{label: {Label: label, Last: "Grep internal/router", LastAt: now.Add(-4 * time.Second)}},
			"t1 claude ▸ Grep internal/router (4s)"},
		{"running stalled", bgTask{ID: "t1", State: taskRunning,
			Spec: taskSpec{ProjectID: "p1", Thread: "claude", CLI: "claude"}},
			map[string]activity.AgentState{label: {Label: label, Last: "go test ./...", LastAt: now.Add(-96 * time.Second)}},
			"t1 claude ⚠ idle 1m36s (last: go test ./...)"},
		{"running without board entry", bgTask{ID: "t1", State: taskRunning,
			Spec: taskSpec{ProjectID: "p1", Thread: "claude", CLI: "claude"}, Started: now.Add(-7 * time.Second)},
			nil, "t1 claude ▸ running (7s)"},
		{"terminal", bgTask{ID: "t1", State: taskDone, Spec: taskSpec{CLI: "claude"}},
			nil, "t1 claude ✓ done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskHeartbeat(tc.tk, tc.states, now); got != tc.want {
				t.Fatalf("heartbeat = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAwaitTasksCollectsAndClaims(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg, board: activity.NewBoard()}
	run1, started1, release1 := blockingRun(map[string]any{"text": "one", "cli": "claude"})
	run2, started2, release2 := blockingRun(map[string]any{"text": "two", "cli": "codex"})
	id1, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "a", CLI: "claude", Risk: "read"}, run1)
	id2, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "b", CLI: "codex", Risk: "read"}, run2)
	<-started1
	<-started2

	var mu sync.Mutex
	var lines []string
	notify := func(_ float64, msg string) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	}
	outCh := make(chan awaitOutcome, 1)
	go func() { outCh <- d.awaitTasks(context.Background(), []string{id1, id2}, notify) }()

	waitFor(t, "progress emitted", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) > 0 && strings.Contains(lines[0], "0/2 done")
	})
	close(release1)
	close(release2)
	out := <-outCh
	if out.Detached {
		t.Fatal("completed await must not report detached")
	}
	if len(out.Results) != 2 || out.Results[0]["text"] != "one" || out.Results[1]["text"] != "two" {
		t.Fatalf("results mismatch: %v", out.Results)
	}
	for _, id := range []string{id1, id2} {
		if tk, _ := reg.Get(id); !tk.Claimed {
			t.Fatalf("awaited task %s must be claimed (results were delivered inline)", id)
		}
	}
}

func TestAwaitTasksDetachOnCancel(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg, board: activity.NewBoard()}
	run1, started1, release1 := blockingRun(map[string]any{"text": "late"})
	id1, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "a", CLI: "claude", Risk: "read"}, run1)
	<-started1
	defer close(release1)

	ctx, cancel := context.WithCancel(context.Background())
	outCh := make(chan awaitOutcome, 1)
	go func() { outCh <- d.awaitTasks(ctx, []string{id1}, nil) }()
	cancel()
	out := <-outCh
	if !out.Detached || out.Results != nil {
		t.Fatalf("cancelled await must detach with no results, got %+v", out)
	}
	tk, _ := reg.Get(id1)
	if tk.Claimed || tk.State != taskRunning {
		t.Fatalf("detached task must keep running unclaimed, got claimed=%v state=%s", tk.Claimed, tk.State)
	}
}

func TestAwaitTasksAnnouncesUnrelatedCompletions(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg, board: activity.NewBoard()}
	// Unrelated task, already terminal BEFORE the await starts: never announced.
	runOld, startedOld, releaseOld := blockingRun(map[string]any{"text": "old"})
	idOld, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "old", CLI: "codex", Risk: "read"}, runOld)
	<-startedOld
	close(releaseOld)
	waitFor(t, "old task done", func() bool { return state(reg, idOld) == taskDone })

	runAw, startedAw, releaseAw := blockingRun(map[string]any{"text": "aw"})
	idAw, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "aw", CLI: "claude", Risk: "read"}, runAw)
	runBg, startedBg, releaseBg := blockingRun(map[string]any{"text": "bg"})
	idBg, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "bg", CLI: "codex", Risk: "read"}, runBg)
	<-startedAw
	<-startedBg

	var mu sync.Mutex
	var lines []string
	notify := func(_ float64, msg string) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	}
	outCh := make(chan awaitOutcome, 1)
	go func() { outCh <- d.awaitTasks(context.Background(), []string{idAw}, notify) }()

	close(releaseBg) // unrelated completion mid-await
	waitFor(t, "unrelated completion announced", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range lines {
			if strings.Contains(l, idBg+" done — collect") {
				return true
			}
		}
		return false
	})
	close(releaseAw)
	<-outCh

	mu.Lock()
	defer mu.Unlock()
	announced := 0
	for _, l := range lines {
		if strings.Contains(l, idBg+" done — collect") {
			announced++
		}
		if strings.Contains(l, idOld) {
			t.Fatalf("pre-terminal task %s must never be announced: %q", idOld, l)
		}
	}
	if announced != 1 {
		t.Fatalf("unrelated completion must be announced exactly once, got %d in %q", announced, lines)
	}
	if tk, _ := reg.Get(idBg); tk.Claimed {
		t.Fatal("announcing an unrelated completion must not claim it")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run 'TestTaskHeartbeat|TestAwaitTasks' -v -timeout 30s`
Expected: compile errors — `undefined: awaitTick`, `undefined: taskHeartbeat`, `undefined: awaitOutcome`.

- [ ] **Step 3: Implement the awaiter**

Create `cmd/styx/mcp_await.go`:

```go
package main

// Awaited-dispatch observer (spec: docs/superpowers/specs/
// 2026-07-08-styx-awaited-dispatch-design.md). An awaited dispatch spawns
// ordinary registry background tasks; awaitTasks watches them until every
// one is terminal, streaming a board-derived progress line via MCP
// notifications, then claims each task (results are delivered inline) and
// returns the combined results. Cancellation — the host's Esc arriving as
// notifications/cancelled, or the server's EOF drain — detaches instead:
// the observer returns immediately, claims nothing, and the tasks keep
// running on the server's root context as collectible background work.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/agent"
)

// awaitTick is the observer cadence. The mirror throttle (2s) and the ollama
// watcher note (15s) ride on top of it; identical lines are not re-emitted.
// A var so tests can shrink it.
var awaitTick = 1 * time.Second

// awaitOutcome is what awaiting a set of tasks produced.
type awaitOutcome struct {
	Detached bool
	Results  []map[string]any // per task, in ids order; nil when detached
}

// awaitTasks blocks until every id is terminal or ctx is cancelled.
// notify may be nil (the client sent no progressToken); progress floats
// carry the finished-task count.
func (d *conductorDeps) awaitTasks(ctx context.Context, ids []string, notify func(float64, string)) awaitOutcome {
	awaited := map[string]bool{}
	for _, id := range ids {
		awaited[id] = true
	}
	// Seed announced with everything already terminal so only completions
	// that happen DURING this await are narrated (exactly once each).
	announced := map[string]bool{}
	for _, tk := range d.reg.Snapshot() {
		if !awaited[tk.ID] && isTerminal(tk.State) {
			announced[tk.ID] = true
		}
	}
	lastLine := ""
	t := time.NewTicker(awaitTick)
	defer t.Stop()
	for {
		done := 0
		for _, id := range ids {
			// A missing id (impossible for freshly spawned tasks, but the
			// registry is the source of truth) counts terminal rather than
			// spinning forever.
			if tk, ok := d.reg.Get(id); !ok || isTerminal(tk.State) {
				done++
			}
		}
		if done == len(ids) {
			results := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				tk, ok := d.reg.Get(id)
				if !ok {
					continue
				}
				results = append(results, collectOne(d.reg, tk)) // claims terminal tasks
			}
			return awaitOutcome{Results: results}
		}
		if line := d.awaitLine(ids, awaited, announced, done); line != lastLine && notify != nil {
			lastLine = line
			notify(float64(done), line)
		}
		select {
		case <-ctx.Done():
			return awaitOutcome{Detached: true}
		case <-t.C:
		}
	}
}

// awaitLine renders one compact progress line: done count, one heartbeat per
// awaited task, one-time notices for unrelated tasks that finished during
// the await, and the ollama watcher note when present. Board access is
// nil-safe (mechanical layer never requires it).
func (d *conductorDeps) awaitLine(ids []string, awaited, announced map[string]bool, done int) string {
	states := map[string]activity.AgentState{}
	note := ""
	if d.board != nil {
		for _, s := range d.board.Snapshot() {
			states[s.Label] = s
		}
		note = d.board.WatcherNote()
	}
	now := time.Now()
	parts := []string{fmt.Sprintf("%d/%d done", done, len(ids))}
	for _, id := range ids {
		if tk, ok := d.reg.Get(id); ok {
			parts = append(parts, taskHeartbeat(tk, states, now))
		}
	}
	for _, tk := range d.reg.Snapshot() {
		if !awaited[tk.ID] && isTerminal(tk.State) && !announced[tk.ID] {
			announced[tk.ID] = true
			parts = append(parts, fmt.Sprintf("%s %s — collect", tk.ID, tk.State))
		}
	}
	if note != "" {
		parts = append(parts, "watch: "+note)
	}
	return strings.Join(parts, " · ")
}

// taskHeartbeat renders one awaited task's live state in the same vocabulary
// as activity.Render (▸ / ⚠ / ✓), sourced from the board entry keyed by the
// task's project-qualified thread label.
func taskHeartbeat(tk bgTask, states map[string]activity.AgentState, now time.Time) string {
	switch tk.State {
	case taskQueued:
		behind := "at cap"
		if tk.QueuedBehind != "" {
			behind = "behind " + tk.QueuedBehind
		}
		return fmt.Sprintf("%s %s queued %s (%s)", tk.ID, tk.Spec.CLI, behind, elapsedShort(now.Sub(tk.Created)))
	case taskRunning:
		s, ok := states[agent.BoardLabel(tk.Spec.ProjectID, tk.Spec.Thread)]
		if !ok || s.Last == "" {
			return fmt.Sprintf("%s %s ▸ running (%s)", tk.ID, tk.Spec.CLI, elapsedShort(now.Sub(tk.Started)))
		}
		idle := now.Sub(s.LastAt)
		if idle > activity.DefaultStall {
			return fmt.Sprintf("%s %s ⚠ idle %s (last: %s)", tk.ID, tk.Spec.CLI, elapsedShort(idle), s.Last)
		}
		return fmt.Sprintf("%s %s ▸ %s (%s)", tk.ID, tk.Spec.CLI, s.Last, elapsedShort(idle))
	default:
		return fmt.Sprintf("%s %s ✓ %s", tk.ID, tk.Spec.CLI, tk.State)
	}
}

// isTerminal reports whether a task state is final.
func isTerminal(state string) bool {
	return state == taskDone || state == taskError || state == taskOrphaned
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/styx/ -run 'TestTaskHeartbeat|TestAwaitTasks' -v -timeout 30s`
Expected: PASS (all four).

- [ ] **Step 5: Update ARCHITECTURE.md (same commit)**

In `docs/ARCHITECTURE.md`, at the end of `### Background task registry (cmd/styx/mcp_tasks.go)` add:

```
**Awaiter (`cmd/styx/mcp_await.go`).** Awaited dispatches are observed
background tasks: `awaitTasks` polls the registry every second until every
awaited id is terminal, streaming one compact progress line per change
(per-task heartbeats from the activity board in Render vocabulary, one-time
"tN done — collect" notices for unrelated completions, the ollama watcher
note when present) through the call's MCP progress emitter. Terminal awaited
tasks are claimed — their results return inline. Context cancellation
(host Esc → notifications/cancelled, or server EOF drain) detaches: nothing
is claimed and the tasks keep running as collectible background work.
```

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go vet ./...
git add cmd/styx/mcp_await.go cmd/styx/mcp_await_test.go docs/ARCHITECTURE.md
git commit -m "feat(mcp): awaiter — observe registry tasks, stream board-derived progress

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Mechanical pulse — mirror stays live without ollama

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (`newConductorDeps` + new methods)
- Modify: `cmd/styx/mcp_conductor_test.go` (new test)
- Modify: `docs/ARCHITECTURE.md` (section `## Activity (internal/activity)`, ~line 1634)

**Interfaces:**
- Consumes: `d.mirror`, `d.mirrorPath`, `d.board`, `d.reg`, `activity.WriteMirror`, `activity.ReadMirror`.
- Produces: `var pulseTick = 1 * time.Second`; `func (d *conductorDeps) pulse(ctx context.Context)`; `func (d *conductorDeps) anyLive() bool`. Wired in `newConductorDeps` — nothing else calls these.

- [ ] **Step 1: Write the failing test**

Append to `cmd/styx/mcp_conductor_test.go` (add `"github.com/ishaanbatra/styx/internal/activity"` to imports):

```go
// TestPulseKeepsMirrorFreshAndFlushesFinal: the mechanical pulse must mirror
// live board state with NO ollama involved (Task-9 closure), write one final
// frame on the live→idle transition, and then go quiet.
func TestPulseKeepsMirrorFreshAndFlushesFinal(t *testing.T) {
	oldTick := pulseTick
	pulseTick = 5 * time.Millisecond
	defer func() { pulseTick = oldTick }()

	path := filepath.Join(t.TempDir(), "mirror.json")
	board := activity.NewBoard()
	d := &conductorDeps{
		board:      board,
		reg:        newTaskRegistry(context.Background(), 4, board),
		mirror:     activity.MirrorThrottle(board, path, time.Millisecond),
		mirrorPath: path,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.pulse(ctx)

	board.Record("p1/claude", "reading files")
	waitFor(t, "live state mirrored mid-run", func() bool {
		states, _, err := activity.ReadMirror(path)
		return err == nil && len(states) == 1 && !states[0].Done && states[0].Last == "reading files"
	})

	board.Done("p1/claude", 3*time.Second)
	waitFor(t, "final flush shows done", func() bool {
		states, _, err := activity.ReadMirror(path)
		return err == nil && len(states) == 1 && states[0].Done
	})

	// Idle: no further writes.
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("pulse must not rewrite the mirror while everything is idle")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestPulseKeepsMirrorFresh -v -timeout 30s`
Expected: compile error `undefined: pulseTick` / `d.pulse undefined`.

- [ ] **Step 3: Implement the pulse**

In `cmd/styx/mcp_conductor.go`, add below `removeMirror`:

```go
// pulseTick is the mechanical mirror cadence; MirrorThrottle (2s) dedupes the
// actual disk writes. A var so tests can shrink it.
var pulseTick = 1 * time.Second

// pulse drives the disk mirror while agents or tasks are live, plus one
// unthrottled flush on the live→idle transition so `styx watch` shows final
// ✓ done states instead of a stale last action. This is the mechanical layer
// closing Task 9's deferred limitation (mirror frozen mid-run for background
// tasks): it runs whenever the server runs — no ollama dependency, no config
// gate — and dies with ctx (the server's root context; no daemons).
func (d *conductorDeps) pulse(ctx context.Context) {
	t := time.NewTicker(pulseTick)
	defer t.Stop()
	wasLive := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			live := d.anyLive()
			switch {
			case live:
				d.mirrorNow()
			case wasLive:
				// Bypass the throttle for the final frame: the debounce could
				// swallow it, and nothing else writes once everything is idle.
				if err := activity.WriteMirror(d.mirrorPath, d.board.Snapshot(), d.board.WatcherNote()); err != nil {
					logStatus("watch mirror: %v", err)
				}
			}
			wasLive = live
		}
	}
}

// anyLive reports whether the board has an unfinished agent or the registry
// a queued/running task.
func (d *conductorDeps) anyLive() bool {
	for _, s := range d.board.Snapshot() {
		if !s.Done {
			return true
		}
	}
	for _, tk := range d.reg.Snapshot() {
		if tk.State == taskQueued || tk.State == taskRunning {
			return true
		}
	}
	return false
}
```

In `newConductorDeps`, immediately after the mirror-setup `if/else` block (after `d.mirror = activity.MirrorThrottle(...)` gets assigned), start the pulse when the mirror exists:

```go
	// Mechanical pulse: keeps the disk mirror live for background AND awaited
	// work with no ollama dependency (awaited-dispatch spec). Only wired when
	// the mirror itself is (same best-effort posture).
	if d.mirror != nil {
		go d.pulse(rootCtx)
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/styx/ -run TestPulseKeepsMirrorFresh -v -timeout 30s`
Expected: PASS.

- [ ] **Step 5: Update ARCHITECTURE.md (same commit)**

In `docs/ARCHITECTURE.md`, section `## Activity (internal/activity)`, find the paragraph describing the conductor's throttled disk mirror / Task 9 limitation (it notes the mirror is written only at background-task start/end) and replace that limitation sentence with:

```
A mechanical pulse goroutine in the conductor (`conductorDeps.pulse`, 1s tick)
refreshes the throttled mirror whenever any agent or task is live and writes
one unthrottled final frame on the live→idle transition — `styx watch` is
live mid-run for background and awaited dispatches alike, with no ollama
dependency. (This closes the Task-9 deferred limitation from the
2026-07-07 observability plan.)
```

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go vet ./...
git add cmd/styx/mcp_conductor.go cmd/styx/mcp_conductor_test.go docs/ARCHITECTURE.md
git commit -m "feat(mcp): mechanical pulse keeps watch mirror live mid-run (closes Task-9 gap)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: `dispatch` awaits by default (spawn + observe, queue instead of busy-error)

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (dispatch handler, ~lines 508–577; tool description)
- Modify: `cmd/styx/mcp_tasks.go` (delete `Busy`)
- Modify: `cmd/styx/mcp_tasks_test.go` (delete the `Busy` test)
- Modify: `cmd/styx/mcp_conductor_test.go` (fixtures + replace `TestDispatchSyncBusyThreadGuard`; new detach test)
- Modify: `docs/ARCHITECTURE.md` (section `## Conductor MCP tools (cmd/styx/mcp_conductor.go)`, ~line 1251)

**Interfaces:**
- Consumes: `awaitTasks` / `awaitOutcome` / `awaitTick` (Task 2), `mcpserver.ProgressFn`, `taskRegistry.Spawn`, `finishDispatch`, `dispatchMeta`.
- Produces: awaited `dispatch` result map — on success the `collectOne` shape `{task_id, status:"done", thread, cli, text, tokens_in, tokens_out, model, duration_s}` (superset of the old sync shape); on detach `{detached:true, task_id, note}`; on task error the tool error string produced by `finishDispatch` (`"dispatch <cli>: ..."`). Outcome rows for awaited dispatches now carry `TaskID` (non-empty) with `Background=false`.

- [ ] **Step 1: Write/adjust the failing tests**

In `cmd/styx/mcp_conductor_test.go`:

1. DELETE `TestDispatchSyncBusyThreadGuard` entirely (queueing replaces the guard).
2. In `TestDispatchHappyPath` and `TestDispatchThreadAppendsOutcomeRow`, add to the `conductorDeps` literal:

```go
		reg: newTaskRegistry(context.Background(), 4, nil),
```

3. In `TestDispatchHappyPath`, after the existing token assertions, add:

```go
	if res["status"] != "done" || res["task_id"] == "" {
		t.Fatalf("awaited dispatch must return a claimed done task, got %v", res)
	}
```

4. In `TestDispatchThreadAppendsOutcomeRow`, after the `o.ErrorKind` check, add:

```go
	if o.TaskID == "" {
		t.Fatal("awaited dispatch outcome must carry its task id")
	}
```

5. Add the two new tests (both use `fastAwait` from Task 2's test file — same package):

```go
// TestDispatchAwaitedQueuesBehindBusyThread: a dispatch to a thread with a
// live background task no longer errors — it queues behind it (ordering
// rules) and completes once the blocker releases.
func TestDispatchAwaitedQueuesBehindBusyThread(t *testing.T) {
	fastAwait(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "queued then ran")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{}, reg: reg,
	}
	// t1: blocking background task holding the thread.
	run1, started1, release1 := blockingRun(nil)
	reg.Spawn(taskSpec{ProjectID: "proj1", Thread: "claude", CLI: "claude", Risk: "edit"}, run1)
	<-started1

	var handler func(context.Context, json.RawMessage) (any, error)
	for _, tool := range conductorTools(d) {
		if tool.Name == "dispatch" {
			handler = tool.Handler
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"project": "proj1", "thread": "claude", "cli": "claude", "message": "hi", "risk": "read",
	})
	type ret struct {
		res any
		err error
	}
	ch := make(chan ret, 1)
	go func() {
		res, err := handler(context.Background(), raw)
		ch <- ret{res, err}
	}()
	waitFor(t, "dispatch queued behind t1", func() bool {
		tk, ok := reg.Get("t2")
		return ok && tk.State == taskQueued && tk.QueuedBehind == "t1"
	})
	close(release1)
	got := <-ch
	if got.err != nil {
		t.Fatalf("queued dispatch must complete after blocker releases: %v", got.err)
	}
	b, _ := json.Marshal(got.res)
	var res map[string]any
	json.Unmarshal(b, &res)
	if res["text"] != "queued then ran" || res["status"] != "done" {
		t.Fatalf("awaited result mismatch: %v", res)
	}
}

// TestDispatchAwaitedDetachesOnCancel: cancelling the call context mid-await
// (host Esc) returns a detach notice; the task keeps running unclaimed.
func TestDispatchAwaitedDetachesOnCancel(t *testing.T) {
	fastAwait(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "slow answer")
	t.Setenv("FAKEAGENT_SLEEP", "1")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{}, reg: reg,
	}
	var handler func(context.Context, json.RawMessage) (any, error)
	for _, tool := range conductorTools(d) {
		if tool.Name == "dispatch" {
			handler = tool.Handler
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"project": "proj1", "cli": "claude", "message": "hi", "risk": "read",
	})
	ctx, cancel := context.WithCancel(context.Background())
	type ret struct {
		res any
		err error
	}
	ch := make(chan ret, 1)
	go func() {
		res, err := handler(ctx, raw)
		ch <- ret{res, err}
	}()
	waitFor(t, "task running", func() bool { return state(reg, "t1") == taskRunning })
	cancel()
	got := <-ch
	if got.err != nil {
		t.Fatalf("detach must not be an error: %v", got.err)
	}
	b, _ := json.Marshal(got.res)
	var res map[string]any
	json.Unmarshal(b, &res)
	if res["detached"] != true || res["task_id"] != "t1" {
		t.Fatalf("want detach notice for t1, got %v", res)
	}
	if tk, _ := reg.Get("t1"); tk.Claimed {
		t.Fatal("detached task must stay unclaimed and collectible")
	}
}
```

In `cmd/styx/mcp_tasks_test.go`: DELETE the test function covering `Busy` (the one containing `nilReg.Busy(...)` and `r.Busy(...)` assertions, around lines 145–171).

- [ ] **Step 2: Run tests to verify the new ones fail**

Run: `go test ./cmd/styx/ -run 'TestDispatchAwaited|TestDispatchHappyPath|TestDispatchThreadAppendsOutcomeRow' -v -timeout 60s`
Expected: `TestDispatchAwaitedQueuesBehindBusyThread` FAILS with the old `thread "claude" is busy with background task t1` error; `TestDispatchAwaitedDetachesOnCancel` FAILS (old sync path returns the dispatch error from the cancelled subprocess, not a detach map); the happy-path/outcome additions FAIL (`status`/`task_id` absent).

- [ ] **Step 3: Rewrite the dispatch handler's thread branch**

In `cmd/styx/mcp_conductor.go`, in the `dispatch` Handler, replace everything from `if in.Background {` down to the final `return d.finishDispatch(ctx, meta, res, err)` (inclusive) with:

```go
				spec := agent.DispatchSpec{
					Thread: in.Thread, CLI: in.CLI, Model: model,
					Message: in.Message, ExtraRoots: in.ExtraRoots,
					ReadOnly: in.Risk == "read",
				}
				if in.Background {
					runFn := func(bctx context.Context, id string) (map[string]any, error) {
						// bctx is the server's root context: the task survives
						// this tool call returning and dies with the server.
						// The mechanical pulse mirrors mid-run; these brackets
						// keep the start/end frames prompt.
						bmeta := meta
						bmeta.Background = true
						bmeta.TaskID = id
						d.mirrorNow()
						res, derr := m.mgr.Dispatch(bctx, spec, nil)
						d.mirrorNow()
						return d.finishDispatch(bctx, bmeta, res, derr)
					}
					id, state := d.reg.Spawn(taskSpec{
						Project: in.Project, ProjectID: m.mgr.ProjectID, Thread: thread,
						CLI: in.CLI, Model: model, Risk: in.Risk,
					}, runFn)
					return map[string]any{"task_id": id, "thread": thread, "cli": in.CLI, "status": state}, nil
				}
				// Awaited (the default): spawn through the same registry as
				// background work — the ordering rules queue a thread collision
				// instead of erroring — and observe until terminal, streaming
				// board-derived progress. Cancellation (host Esc) detaches: the
				// task keeps running and stays collectible; work is never lost
				// (spec: awaited = observed background).
				if d.reg == nil {
					return nil, fmt.Errorf("dispatch unavailable (no task registry)")
				}
				runFn := func(bctx context.Context, id string) (map[string]any, error) {
					ameta := meta
					ameta.TaskID = id
					d.mirrorNow()
					res, derr := m.mgr.Dispatch(bctx, spec, nil)
					d.mirrorNow()
					return d.finishDispatch(bctx, ameta, res, derr)
				}
				id, _ := d.reg.Spawn(taskSpec{
					Project: in.Project, ProjectID: m.mgr.ProjectID, Thread: thread,
					CLI: in.CLI, Model: model, Risk: in.Risk,
				}, runFn)
				notify, _ := mcpserver.ProgressFn(ctx)
				out := d.awaitTasks(ctx, []string{id}, notify)
				if out.Detached {
					return map[string]any{"detached": true, "task_id": id,
						"note": "await interrupted; the task keeps running — collect fetches its result"}, nil
				}
				r := out.Results[0]
				if r["status"] != taskDone {
					return nil, fmt.Errorf("%v", r["error"])
				}
				return r, nil
```

This DELETES: the `d.reg.Busy(...)` busy-error block, the `notify/events/onEvent` block (coarse "streaming (N events)" progress), the direct `m.mgr.Dispatch(ctx, ...)` call, and the trailing `d.mirrorNow()`.

- [ ] **Step 4: Delete the now-unused `Busy`**

Confirm the only caller is gone: `grep -rn "\.Busy(" cmd/ internal/` → only `mcp_tasks_test.go` remains (deleted in Step 1). Remove the `Busy` method from `cmd/styx/mcp_tasks.go` (the block starting `// Busy reports the live (queued or running) task...` through its closing brace).

- [ ] **Step 5: Update the `dispatch` tool description**

In the same tool literal, replace the `Description:` with:

```go
			Description: "Send work to a persistent agent thread (claude/codex/agy) or a one-shot local ollama task. " +
				"By default this WAITS: live progress streams while the agent works and the result returns inline — " +
				"use it for anything the user is waiting on. background:true detaches instead (returns a task_id; " +
				"collect fetches the result) — only when the user explicitly wants to keep working meanwhile. " +
				"For several agents at once use dispatch_parallel. risk: read|edit|ship. ship requires the confirm_token handshake.",
```

- [ ] **Step 6: Run the package tests**

Run: `go test ./cmd/styx/ -v -timeout 600s`
Expected: ALL PASS — including the adjusted happy-path/outcome tests, both new awaited tests, and the untouched background roundtrip (`TestBackgroundDispatchRoundtrip`), ship-gate, validation, and collect tests.

- [ ] **Step 7: Update ARCHITECTURE.md (same commit)**

In `docs/ARCHITECTURE.md`, section `## Conductor MCP tools (cmd/styx/mcp_conductor.go)`, update the `dispatch` tool's description to state: awaited by default (spawns a registry task and observes it via the awaiter — progress streams, result returns inline, thread collisions queue instead of erroring, cancellation detaches); `background: true` returns a task handle immediately as before; outcome rows for awaited dispatches carry the task id with `background=false`. Remove/adjust any sentence claiming sync dispatches bypass the registry or error on busy threads.

- [ ] **Step 8: Commit**

```bash
gofmt -w . && go vet ./...
git add cmd/styx/mcp_conductor.go cmd/styx/mcp_tasks.go cmd/styx/mcp_conductor_test.go cmd/styx/mcp_tasks_test.go docs/ARCHITECTURE.md
git commit -m "feat(mcp): dispatch awaits by default — spawn+observe, queue instead of busy-error, detach on cancel

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: `dispatch_parallel` — awaited N-agent fan-out

**Files:**
- Modify: `cmd/styx/mcp_conductor.go` (new tool in `conductorTools`)
- Modify: `cmd/styx/mcp_conductor_test.go`
- Modify: `cmd/styx/mcp.go` (startup `logStatus` tool list — add `dispatch_parallel`)
- Modify: `docs/ARCHITECTURE.md` (section `## Conductor MCP tools`)

**Interfaces:**
- Consumes: `awaitTasks`, `spawnBudgetCheck`, `managerFor`, `dispatchMeta`/`finishDispatch`, `dispatchArgs` (reused per array element; `background`/`confirm_token` ignored), `mcpserver.ProgressFn`.
- Produces: tool `dispatch_parallel` with result `{results: [per-task maps in input order], detached: bool}` (+ `task_ids`, `note` when detached). Per-task failure entries: `{status:"error", error, cli, thread}`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/styx/mcp_conductor_test.go`:

```go
// TestDispatchParallelCombinesResults: two read-risk tasks on different
// threads run concurrently and both results return inline, in input order.
func TestDispatchParallelCombinesResults(t *testing.T) {
	fastAwait(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "parallel answer")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{},
		reg:      newTaskRegistry(context.Background(), 4, nil),
	}
	res, err := callTool(t, d, "dispatch_parallel", map[string]any{
		"tasks": []map[string]any{
			{"project": "proj1", "thread": "scan-a", "cli": "claude", "message": "scan A", "risk": "read"},
			{"project": "proj1", "thread": "scan-b", "cli": "claude", "message": "scan B", "risk": "read"},
		},
	})
	if err != nil {
		t.Fatalf("dispatch_parallel: %v", err)
	}
	if res["detached"] != false {
		t.Fatalf("completed fan-out must not be detached: %v", res)
	}
	results, _ := res["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %v", res["results"])
	}
	for i, r := range results {
		m, _ := r.(map[string]any)
		if m["status"] != "done" || m["text"] != "parallel answer" {
			t.Fatalf("result %d mismatch: %v", i, m)
		}
	}
	// Both awaited tasks were claimed: nothing pending for collect.
	all, err := callTool(t, d, "collect", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := all["results"].([]any); len(got) != 0 {
		t.Fatalf("awaited fan-out results must be claimed, got %v", all["results"])
	}
}

// TestDispatchParallelPerTaskFailures: a bad task fails as a per-task entry;
// valid siblings still run. Whole-call errors are reserved for bad args.
func TestDispatchParallelPerTaskFailures(t *testing.T) {
	fastAwait(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "survivor")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{},
		reg:      newTaskRegistry(context.Background(), 4, nil),
	}
	res, err := callTool(t, d, "dispatch_parallel", map[string]any{
		"tasks": []map[string]any{
			{"cli": "ollama", "message": "not allowed here", "risk": "read"},
			{"cli": "claude", "message": "ship it", "risk": "ship"},
			{"project": "proj1", "thread": "ok", "cli": "claude", "message": "fine", "risk": "read"},
		},
	})
	if err != nil {
		t.Fatalf("per-task failures must not fail the call: %v", err)
	}
	results, _ := res["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %v", res["results"])
	}
	r0, _ := results[0].(map[string]any)
	r1, _ := results[1].(map[string]any)
	r2, _ := results[2].(map[string]any)
	if r0["status"] != "error" || !strings.Contains(r0["error"].(string), "cli") {
		t.Fatalf("ollama task must fail per-task: %v", r0)
	}
	if r1["status"] != "error" || !strings.Contains(r1["error"].(string), "risk") {
		t.Fatalf("ship task must fail per-task: %v", r1)
	}
	if r2["status"] != "done" || r2["text"] != "survivor" {
		t.Fatalf("valid sibling must complete: %v", r2)
	}

	// Bad args (no tasks) IS a whole-call error.
	if _, err := callTool(t, d, "dispatch_parallel", map[string]any{"tasks": []map[string]any{}}); err == nil {
		t.Fatal("empty tasks must be a call error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run TestDispatchParallel -v -timeout 60s`
Expected: FAIL with `tool "dispatch_parallel" not registered`.

- [ ] **Step 3: Implement the tool**

In `cmd/styx/mcp_conductor.go`, append to the slice returned by `conductorTools` (after the `collect` tool):

```go
		{
			Name: "dispatch_parallel",
			Description: "Run several agent dispatches in parallel and WAIT for all of them: live combined " +
				"progress streams while they work and every result returns inline, in input order. This is the " +
				"default for multi-agent work the user is waiting on. read|edit risk only (no ship, no ollama); " +
				"thread/project collisions queue per the ordering rules. Cancelling detaches: tasks keep running, " +
				"collect fetches their results.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tasks": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"project":     map[string]any{"type": "string", "description": "registered project alias; empty = the project styx was launched in"},
								"thread":      map[string]any{"type": "string", "description": "thread name; empty = cli name"},
								"cli":         map[string]any{"type": "string", "enum": []string{"claude", "codex", "agy"}},
								"message":     map[string]any{"type": "string"},
								"model":       map[string]any{"type": "string", "description": "tier (opus|sonnet|haiku) or raw model id; empty = channel default"},
								"risk":        map[string]any{"type": "string", "enum": []string{"read", "edit"}},
								"extra_roots": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
							},
							"required": []string{"cli", "message", "risk"},
						},
					},
				},
				"required": []string{"tasks"},
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in struct {
					Tasks []dispatchArgs `json:"tasks"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("dispatch_parallel args: %w", err)
				}
				if len(in.Tasks) == 0 {
					return nil, fmt.Errorf("tasks is required (at least one)")
				}
				if d.reg == nil {
					return nil, fmt.Errorf("dispatch_parallel unavailable (no task registry)")
				}
				results := make([]map[string]any, len(in.Tasks))
				var ids []string
				idIndex := map[string]int{}
				for i, tin := range in.Tasks {
					fail := func(err error) {
						results[i] = map[string]any{"status": taskError, "error": err.Error(),
							"cli": tin.CLI, "thread": tin.Thread}
					}
					switch tin.CLI {
					case "claude", "codex", "agy":
					default:
						fail(fmt.Errorf("unknown cli %q (claude|codex|agy — ollama one-shots and ship risk are single-dispatch only)", tin.CLI))
						continue
					}
					if tin.Message == "" {
						fail(fmt.Errorf("message is required"))
						continue
					}
					if tin.Risk != "read" && tin.Risk != "edit" {
						fail(fmt.Errorf("risk must be read|edit in a parallel dispatch, got %q", tin.Risk))
						continue
					}
					if err := d.spawnBudgetCheck(ctx, tin.CLI); err != nil {
						fail(err)
						continue
					}
					m, err := d.managerFor(tin.Project)
					if err != nil {
						fail(err)
						continue
					}
					model := tin.Model
					if resolved, ok := d.a.routing.Tiers[model]; ok {
						model = resolved
					}
					thread := tin.Thread
					if thread == "" {
						thread = tin.CLI
					}
					meta := dispatchMeta{
						ProjectID: m.mgr.ProjectID, Thread: thread, CLI: tin.CLI,
						Model: model, Risk: tin.Risk, Signals: dispatchSignals(tin.Message),
						Start: time.Now(),
					}
					spec := agent.DispatchSpec{
						Thread: tin.Thread, CLI: tin.CLI, Model: model,
						Message: tin.Message, ExtraRoots: tin.ExtraRoots,
						ReadOnly: tin.Risk == "read",
					}
					mgr := m.mgr
					runFn := func(bctx context.Context, id string) (map[string]any, error) {
						ameta := meta
						ameta.TaskID = id
						d.mirrorNow()
						res, derr := mgr.Dispatch(bctx, spec, nil)
						d.mirrorNow()
						return d.finishDispatch(bctx, ameta, res, derr)
					}
					id, _ := d.reg.Spawn(taskSpec{
						Project: tin.Project, ProjectID: m.mgr.ProjectID, Thread: thread,
						CLI: tin.CLI, Model: model, Risk: tin.Risk,
					}, runFn)
					ids = append(ids, id)
					idIndex[id] = i
				}
				if len(ids) == 0 {
					return map[string]any{"results": results, "detached": false}, nil
				}
				notify, _ := mcpserver.ProgressFn(ctx)
				out := d.awaitTasks(ctx, ids, notify)
				if out.Detached {
					return map[string]any{"detached": true, "task_ids": ids, "results": results,
						"note": "await interrupted; tasks keep running — collect fetches their results"}, nil
				}
				for j, id := range ids {
					results[idIndex[id]] = out.Results[j]
				}
				return map[string]any{"results": results, "detached": false}, nil
			},
		},
```

In `cmd/styx/mcp.go`, extend the startup `logStatus("mcp server ready on stdio (...)")` tool list to include `dispatch_parallel` after `dispatch`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/styx/ -run TestDispatchParallel -v -timeout 60s`
Expected: PASS (both).

- [ ] **Step 5: Update ARCHITECTURE.md (same commit)**

In `docs/ARCHITECTURE.md`, `## Conductor MCP tools`, add `dispatch_parallel` to the tool table/list with one row/paragraph:

```
`dispatch_parallel` — awaited N-agent fan-out: an array of {cli, message,
risk, thread?, project?, model?, extra_roots?} specs, each spawned as a
registry task (read risk runs concurrently; ordering rules queue
collisions), observed by the same awaiter as `dispatch`. Per-task failures
(validation, budget, agent error) are per-entry results — the call errors
only on malformed arguments. Cancellation detaches all spawned tasks.
read|edit only; ship and ollama stay single-dispatch.
```

- [ ] **Step 6: Commit**

```bash
gofmt -w . && go vet ./...
git add cmd/styx/mcp_conductor.go cmd/styx/mcp_conductor_test.go cmd/styx/mcp.go docs/ARCHITECTURE.md
git commit -m "feat(mcp): dispatch_parallel — awaited N-agent fan-out with per-task results

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Documentation sweep + full verification

**Files:**
- Modify: `docs/ARCHITECTURE.md` (coherence pass over the four sections touched in Tasks 1–5; frontmatter `last_verified`)
- Modify: `README.md` (conductor/MCP section)
- Modify: `docs/superpowers/specs/2026-07-07-styx-dispatch-observability-design.md` (forward pointer)

**Interfaces:**
- Consumes: everything shipped in Tasks 1–5.
- Produces: docs only — no code.

- [ ] **Step 1: Coherence pass over ARCHITECTURE.md**

Re-read the four updated sections (`## MCP server`, `## Conductor MCP tools`, `### Background task registry`, `## Activity`) end to end. Fix contradictions with the shipped code — in particular any residual text claiming: background tasks emit no mid-run mirror writes; sync dispatch streams "streaming (N events)" progress; the server is serial; `Busy` guards sync dispatches. Set frontmatter `last_verified:` to today's date.

- [ ] **Step 2: README conductor section**

In `README.md`, find the MCP/conductor section (the one describing `styx mcp` and the conductor tools) and add:

```
Dispatches wait by default: while an agent works, live progress (tool-by-tool
activity plus the local-ollama watcher's narration) streams into the MCP
host's UI as progress notifications — no tokens, no polling — and the
findings return inline the moment the work finishes. `dispatch_parallel`
does the same for several agents at once. `background: true` detaches
instead; `styx watch` in a second terminal is live either way. Interrupting
an awaited call (Esc) detaches it — the agents keep working and `collect`
fetches their results. For very long dispatches, raise your MCP client's
tool-call timeout (Claude Code: `MCP_TOOL_TIMEOUT`).
```

Bump README's `last_verified` frontmatter date if the file has one.

- [ ] **Step 3: Forward pointer in the 2026-07-07 observability spec**

In `docs/superpowers/specs/2026-07-07-styx-dispatch-observability-design.md`, directly under the `# Live Dispatch Observability for Styx` heading, add:

```
> **Update (2026-07-08):** the Task-9 deferred limitation (disk mirror frozen
> between a background task's start and end) is closed by the awaited-dispatch
> design — see `2026-07-08-styx-awaited-dispatch-design.md`: a mechanical
> pulse in the conductor refreshes the mirror every second while anything is
> live, and dispatches now await by default with inline results.
```

- [ ] **Step 4: Full verification**

```bash
gofmt -w . && go vet ./...
go test ./... -timeout 900s
go build -o ./bin/styx ./cmd/styx
```
Expected: vet clean, ALL tests pass, build succeeds.

Manual smoke (optional but recommended, needs the claude CLI installed): from the repo root run `go run ./cmd/styx mcp` in a pipe and drive one awaited dispatch by hand, or attach the rebuilt server to a Claude Code session and dispatch a small read-risk task — confirm progress lines appear under the running tool call and the result arrives without prompting.

- [ ] **Step 5: Commit**

```bash
git add docs/ARCHITECTURE.md README.md docs/superpowers/specs/2026-07-07-styx-dispatch-observability-design.md
git commit -m "docs: awaited dispatch — architecture, README, and observability-spec pointer

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```
