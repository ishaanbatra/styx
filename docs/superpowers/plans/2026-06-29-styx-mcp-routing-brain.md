# styx MCP Routing Brain — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `styx mcp` command that runs styx as a minimal MCP (Model Context Protocol) stdio server exposing three tools — `route`, `budget_status`, `record_usage` — so OpenClaw (and any MCP host) can call styx's budget-aware routing brain.

**Architecture:** A transport-only `internal/mcpserver` package speaks newline-delimited JSON-RPC 2.0 over stdio and handles the `initialize` / `tools/list` / `tools/call` handshake; it has zero styx-domain knowledge. `cmd/styx/mcp.go` builds three tool handlers that adapt JSON arguments onto the existing `internal/router` and `internal/budget` packages, assembles them into a server, and runs it on stdin/stdout. styx's standalone behavior is untouched (its "hands" stay).

**Tech Stack:** Go 1.22, standard library only (`encoding/json`, `bufio`, `io`). No new third-party dependency — the MCP server is hand-rolled (see Global Constraints).

## Global Constraints

- Module path: `github.com/ishaanbatra/styx`. Go version: `go 1.22`.
- **No new dependency.** Implement MCP with the standard library only. This is a deliberate plan decision resolving the spec's open "Go MCP library" question: hand-rolling a ~150-line JSON-RPC stdio loop fits styx's minimal-deps ethos (only `BurntSushi/toml` + `modernc.org/sqlite` today) and avoids coupling to a churning SDK. It is **not** a provider SDK, so the CLAUDE.md "CLIs/HTTP, never SDKs" rule is not violated.
- **stdout is the protocol.** The MCP server owns stdout for JSON-RPC. Nothing else may write to stdout in the `mcp` command path. All status/narration goes to stderr via `logStatus` (already stderr-only). Never `fmt.Println` to stdout from a tool handler.
- Wrap errors with context: `fmt.Errorf("route: %w", err)`. Never swallow errors (no `x, _ :=` that drops a meaningful error).
- Table-driven tests with `t.Run` where there are multiple cases; fakes/real-with-tempdir over mocks (follow `internal/router/router_test.go` and `internal/budget/budget_test.go`).
- Before every commit: `go vet ./... && gofmt -w .` and `go test ./...` (or `make test`).
- Drift contract: the docs task updates `docs/ARCHITECTURE.md` (owns `cmd/styx/**`, `internal/**`) and `README.md` in the same commit, and bumps ARCHITECTURE.md's `last_verified` date.

**Reference signatures (verbatim from the codebase):**

```go
// internal/router/router.go
type Request struct { Verb string; Args []string; Signals []string }
type ChannelModel struct { Channel string; Model string }
type Decision struct {
	Channel string; Model string; Effort string; Fallback []ChannelModel
	RuleIdx int; Reason string
	Parallel bool; ParallelTargets []ChannelModel; SynthesizeWith ChannelModel
	Degraded bool
}
func FromConfig(routing config.Routing, b BudgetSource) *Router
func (r *Router) Route(ctx context.Context, req Request) (Decision, error)
func (r *Router) Explain(ctx context.Context, req Request) string

// internal/budget/budget.go
type Event struct {
	Channel string; Verb string; Model string
	TokensIn int; TokensOut int; Success bool
	ErrorKind string; Project string; RunID string
}
type State struct {
	Channel string; Window time.Duration; UsedPct float64; LimitHit bool; CooldownUntil time.Time
	SessionCount int; SessionLimit int; WeeklyCount int; WeeklyLimit int
	SessionPct float64; WeeklyPct float64
}
func New(path string) (*Tracker, error)
func (t *Tracker) Record(ctx context.Context, e Event) error
func (t *Tracker) State(ctx context.Context, channel string) (State, error)
func (t *Tracker) Close() error

// internal/signals/signals.go
func Extract(verb string, args []string, proj config.Project) []string

// cmd/styx/dispatch.go
type app struct { routing config.Routing; tracker *budget.Tracker; router *router.Router; channels map[string]channel.Channel; progress *progress.Tracker }
func loadApp() (*app, error)
func logStatus(format string, args ...any)
```

Note: `*budget.Tracker` satisfies `router.BudgetSource` (both define `UsedPct(ctx, channel) (float64, error)`), so tests can build a real router backed by a real tracker.

---

### Task 1: `internal/mcpserver` — JSON-RPC stdio protocol layer

A transport-only MCP server: register `Tool`s, serve `initialize` / `tools/list` / `tools/call` over stdio. No styx-domain knowledge.

**Files:**
- Create: `internal/mcpserver/server.go`
- Test: `internal/mcpserver/server_test.go`

**Interfaces:**
- Consumes: nothing (stdlib only).
- Produces:
  - `type Tool struct { Name string; Description string; InputSchema any; Handler func(ctx context.Context, args json.RawMessage) (any, error) }`
  - `func New(name, version string, tools []Tool) *Server`
  - `func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error`

- [ ] **Step 1: Write the failing tests**

Create `internal/mcpserver/server_test.go`:

```go
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func testServer() *Server {
	echo := Tool{
		Name:        "echo",
		Description: "echoes a fixed payload",
		InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	return New("styx", "test", []Tool{echo})
}

func serve(t *testing.T, s *Server, requests ...string) []string {
	t.Helper()
	in := strings.Join(requests, "\n") + "\n"
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return nonEmptyLines(out.String())
}

func TestServe_Initialize(t *testing.T) {
	lines := serve(t, testServer(),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(lines) != 1 {
		t.Fatalf("want 1 response, got %d: %v", len(lines), lines)
	}
	var resp struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct{ Name string `json:"name"` } `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 1 || resp.Result.ProtocolVersion == "" || resp.Result.ServerInfo.Name != "styx" {
		t.Fatalf("bad initialize response: %s", lines[0])
	}
}

func TestServe_NotificationGetsNoResponse(t *testing.T) {
	lines := serve(t, testServer(),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if len(lines) != 1 {
		t.Fatalf("notification must produce no response; want 1 line, got %d: %v", len(lines), lines)
	}
}

func TestServe_ToolsList(t *testing.T) {
	lines := serve(t, testServer(),
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	var resp struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				InputSchema any    `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Result.Tools) != 1 || resp.Result.Tools[0].Name != "echo" || resp.Result.Tools[0].InputSchema == nil {
		t.Fatalf("bad tools/list: %s", lines[0])
	}
}

func TestServe_ToolsCall(t *testing.T) {
	lines := serve(t, testServer(),
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{}}}`)
	if !strings.Contains(lines[0], `"isError":false`) || !strings.Contains(lines[0], `"ok": true`) {
		t.Fatalf("bad tools/call result: %s", lines[0])
	}
}

func TestServe_UnknownMethod(t *testing.T) {
	lines := serve(t, testServer(),
		`{"jsonrpc":"2.0","id":5,"method":"does/not/exist"}`)
	if !strings.Contains(lines[0], `"error"`) || !strings.Contains(lines[0], "-32601") {
		t.Fatalf("want method-not-found error: %s", lines[0])
	}
}

func TestServe_ToolHandlerErrorIsToolResult(t *testing.T) {
	s := New("styx", "test", []Tool{{
		Name: "boom", Description: "always fails", InputSchema: map[string]any{"type": "object"},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			return nil, context.DeadlineExceeded
		},
	}})
	lines := serve(t, s,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"boom","arguments":{}}}`)
	// A failing handler is a tool result with isError=true, NOT a protocol error.
	if !strings.Contains(lines[0], `"isError":true`) || strings.Contains(lines[0], `"error":{`) {
		t.Fatalf("handler error should be an isError tool result: %s", lines[0])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/mcpserver/ -v`
Expected: FAIL — `undefined: New`, `undefined: Server`, `undefined: Tool`.

- [ ] **Step 3: Write the implementation**

Create `internal/mcpserver/server.go`:

```go
// Package mcpserver implements a minimal Model Context Protocol (MCP) server
// over stdio: newline-delimited JSON-RPC 2.0 on an io.Reader/io.Writer pair.
// It is transport-only and knows nothing about styx's domain — callers register
// Tools and the server handles the initialize / tools-list / tools-call
// handshake. This keeps styx's routing brain consumable by any MCP host
// (OpenClaw first) without adding a provider SDK or a protocol dependency.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// protocolVersion is the MCP revision this server advertises.
const protocolVersion = "2024-11-05"

// Tool is a single callable exposed over MCP.
type Tool struct {
	Name        string
	Description string
	InputSchema any // serialized as a JSON Schema object in tools/list
	Handler     func(ctx context.Context, args json.RawMessage) (any, error)
}

// Server is a registry of tools served over one MCP stdio connection.
type Server struct {
	name    string
	version string
	tools   []Tool
	byName  map[string]Tool
}

// New builds a Server with the given identity and tool set.
func New(name, version string, tools []Tool) *Server {
	s := &Server{name: name, version: version, tools: tools, byName: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		s.byName[t.Name] = t
	}
	return s
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads JSON-RPC messages from in and writes responses to out until EOF.
// It returns nil on a clean EOF (the host closed the connection).
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // tolerate large tool payloads
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32700, Message: "parse error"}})
			continue
		}
		resp, isNotification := s.handle(ctx, req)
		if isNotification {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return fmt.Errorf("write response: %w", err)
		}
	}
	return scanner.Err()
}

// handle routes one request. The bool is true when req is a notification
// (no id) and therefore gets no response.
func (s *Server) handle(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	if len(req.ID) == 0 {
		return rpcResponse{}, true
	}
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
	case "tools/call":
		resp.Result, resp.Error = s.callTool(ctx, req.Params)
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	return resp, false
}

func (s *Server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": schema,
		})
	}
	return out
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// callTool runs a tool. A handler error becomes an MCP tool result with
// isError=true (so the calling model can read the message), NOT a JSON-RPC
// protocol error. Bad params / unknown tool are protocol errors.
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p callParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params: " + err.Error()}
	}
	tool, ok := s.byName[p.Name]
	if !ok {
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + p.Name}
	}
	result, err := tool.Handler(ctx, p.Arguments)
	if err != nil {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}, nil
	}
	payload, mErr := json.MarshalIndent(result, "", "  ")
	if mErr != nil {
		return nil, &rpcError{Code: -32603, Message: "marshal result: " + mErr.Error()}
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(payload)}},
		"isError": false,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/mcpserver/ -v`
Expected: PASS (all 6 tests).

- [ ] **Step 5: Vet, format, commit**

```bash
go vet ./internal/mcpserver/ && gofmt -w internal/mcpserver/
git add internal/mcpserver/server.go internal/mcpserver/server_test.go
git commit -m "feat(mcp): minimal MCP stdio JSON-RPC server (transport-only)"
```

---

### Task 2: `route` tool handler

Adapt JSON args → `router.Request` → `router.Route` + `router.Explain`, with a budget snapshot for the chosen channel.

**Files:**
- Create: `cmd/styx/mcp.go`
- Test: `cmd/styx/mcp_test.go`

**Interfaces:**
- Consumes: `router.FromConfig`, `(*router.Router).Route/Explain`, `(*budget.Tracker).State`, `signals.Extract`, `config.Project`, `config.Routing`, `config.Rule`.
- Produces (used by Tasks 3–5):
  - `type routeArgs struct { Task string; Verb string; Signals []string; Project string }`
  - `type budgetSnapshot struct { Channel string; SessionCount/SessionLimit/WeeklyCount/WeeklyLimit int; SessionPct/WeeklyPct float64; CooldownUntil string; Stale bool }`
  - `type routeResult struct { Channel, Model, Effort string; FallbackChain []string; Reasoning string; Budget budgetSnapshot; Degraded bool }`
  - `func budgetSnapshotFor(ctx context.Context, t *budget.Tracker, channel string) budgetSnapshot`
  - `func handleRoute(ctx context.Context, r *router.Router, t *budget.Tracker, a routeArgs) (routeResult, error)`

- [ ] **Step 1: Write the failing test**

Create `cmd/styx/mcp_test.go`:

```go
package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/router"
)

func testRouterAndTracker(t *testing.T) (*router.Router, *budget.Tracker) {
	t.Helper()
	tr, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	r := router.FromConfig(config.Routing{
		Rules: []config.Rule{{Verb: "build", Use: "codex:gpt-5"}},
	}, tr)
	return r, tr
}

func TestHandleRoute_ReturnsChannelModelReasoningBudget(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	res, err := handleRoute(context.Background(), r, tr, routeArgs{Task: "add dark mode", Verb: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Channel != "codex" || res.Model != "gpt-5" {
		t.Fatalf("got %s:%s, want codex:gpt-5", res.Channel, res.Model)
	}
	if res.Reasoning == "" {
		t.Error("expected non-empty reasoning")
	}
	if res.Budget.Channel != "codex" {
		t.Errorf("budget snapshot channel = %q, want codex", res.Budget.Channel)
	}
}

func TestHandleRoute_RequiresTask(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	if _, err := handleRoute(context.Background(), r, tr, routeArgs{}); err == nil {
		t.Fatal("expected error when task is empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestHandleRoute -v`
Expected: FAIL — `undefined: handleRoute`, `undefined: routeArgs`.

- [ ] **Step 3: Write the implementation**

Create `cmd/styx/mcp.go`:

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

type routeArgs struct {
	Task    string   `json:"task"`
	Verb    string   `json:"verb"`
	Signals []string `json:"signals"`
	Project string   `json:"project"`
}

type budgetSnapshot struct {
	Channel       string  `json:"channel"`
	SessionCount  int     `json:"session_count"`
	SessionLimit  int     `json:"session_limit"`
	SessionPct    float64 `json:"session_pct"`
	WeeklyCount   int     `json:"weekly_count"`
	WeeklyLimit   int     `json:"weekly_limit"`
	WeeklyPct     float64 `json:"weekly_pct"`
	CooldownUntil string  `json:"cooldown_until,omitempty"`
	Stale         bool    `json:"stale"`
}

type routeResult struct {
	Channel       string         `json:"channel"`
	Model         string         `json:"model"`
	Effort        string         `json:"effort,omitempty"`
	FallbackChain []string       `json:"fallback_chain"`
	Reasoning     string         `json:"reasoning"`
	Budget        budgetSnapshot `json:"budget"`
	Degraded      bool           `json:"degraded"`
}

// budgetSnapshotFor reads the budget State for a channel. On error it returns a
// snapshot flagged Stale=true rather than failing the whole call (degrade loud).
func budgetSnapshotFor(ctx context.Context, t *budget.Tracker, channel string) budgetSnapshot {
	st, err := t.State(ctx, channel)
	if err != nil {
		return budgetSnapshot{Channel: channel, Stale: true}
	}
	snap := budgetSnapshot{
		Channel:      channel,
		SessionCount: st.SessionCount,
		SessionLimit: st.SessionLimit,
		SessionPct:   st.SessionPct,
		WeeklyCount:  st.WeeklyCount,
		WeeklyLimit:  st.WeeklyLimit,
		WeeklyPct:    st.WeeklyPct,
	}
	if !st.CooldownUntil.IsZero() {
		snap.CooldownUntil = st.CooldownUntil.Format(time.RFC3339)
	}
	return snap
}

// handleRoute picks a channel for a task using the budget-aware router and
// returns the decision plus a budget snapshot for the chosen channel.
func handleRoute(ctx context.Context, r *router.Router, t *budget.Tracker, a routeArgs) (routeResult, error) {
	if a.Task == "" {
		return routeResult{}, fmt.Errorf("route: task is required")
	}
	verb := a.Verb
	if verb == "" {
		verb = "build"
	}
	sigs := a.Signals
	if len(sigs) == 0 {
		sigs = signals.Extract(verb, []string{a.Task}, config.Project{})
	}
	req := router.Request{Verb: verb, Args: []string{a.Task}, Signals: sigs}
	dec, err := r.Route(ctx, req)
	if err != nil {
		return routeResult{}, fmt.Errorf("route: %w", err)
	}
	chain := make([]string, 0, len(dec.Fallback))
	for _, cm := range dec.Fallback {
		chain = append(chain, cm.Channel+":"+cm.Model)
	}
	return routeResult{
		Channel:       dec.Channel,
		Model:         dec.Model,
		Effort:        dec.Effort,
		FallbackChain: chain,
		Reasoning:     r.Explain(ctx, req),
		Budget:        budgetSnapshotFor(ctx, t, dec.Channel),
		Degraded:      dec.Degraded,
	}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/styx/ -run TestHandleRoute -v`
Expected: PASS (both cases).

- [ ] **Step 5: Vet, format, commit**

```bash
go vet ./cmd/styx/ && gofmt -w cmd/styx/
git add cmd/styx/mcp.go cmd/styx/mcp_test.go
git commit -m "feat(mcp): route tool handler over router + budget snapshot"
```

---

### Task 3: `budget_status` tool handler

Return a budget snapshot per channel (one, or all four).

**Files:**
- Modify: `cmd/styx/mcp.go`
- Test: `cmd/styx/mcp_test.go`

**Interfaces:**
- Consumes: `budgetSnapshotFor` (Task 2), `(*budget.Tracker).State`.
- Produces:
  - `type budgetStatusArgs struct { Channel string }`
  - `func handleBudgetStatus(ctx context.Context, t *budget.Tracker, a budgetStatusArgs) ([]budgetSnapshot, error)`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/styx/mcp_test.go`:

```go
func TestHandleBudgetStatus_AllChannels(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	out, err := handleBudgetStatus(context.Background(), tr, budgetStatusArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 channels, got %d", len(out))
	}
}

func TestHandleBudgetStatus_SingleChannel(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	out, err := handleBudgetStatus(context.Background(), tr, budgetStatusArgs{Channel: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Channel != "claude" {
		t.Fatalf("want [claude], got %+v", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run TestHandleBudgetStatus -v`
Expected: FAIL — `undefined: handleBudgetStatus`, `undefined: budgetStatusArgs`.

- [ ] **Step 3: Write the implementation**

Append to `cmd/styx/mcp.go`:

```go
type budgetStatusArgs struct {
	Channel string `json:"channel"`
}

// defaultChannelNames is the canonical channel set (mirrors cmd/styx/budget.go).
var defaultChannelNames = []string{"claude", "codex", "agy", "ollama"}

// handleBudgetStatus reports the budget snapshot for one channel, or all four
// when Channel is empty.
func handleBudgetStatus(ctx context.Context, t *budget.Tracker, a budgetStatusArgs) ([]budgetSnapshot, error) {
	channels := defaultChannelNames
	if a.Channel != "" {
		channels = []string{a.Channel}
	}
	out := make([]budgetSnapshot, 0, len(channels))
	for _, ch := range channels {
		out = append(out, budgetSnapshotFor(ctx, t, ch))
	}
	return out, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/styx/ -run TestHandleBudgetStatus -v`
Expected: PASS.

- [ ] **Step 5: Vet, format, commit**

```bash
go vet ./cmd/styx/ && gofmt -w cmd/styx/
git add cmd/styx/mcp.go cmd/styx/mcp_test.go
git commit -m "feat(mcp): budget_status tool handler"
```

---

### Task 4: `record_usage` tool handler

Append usage rows so styx's budget stays accurate when a consumer (OpenClaw) ran the agent.

**Files:**
- Modify: `cmd/styx/mcp.go`
- Test: `cmd/styx/mcp_test.go`

**Interfaces:**
- Consumes: `budgetSnapshotFor` (Task 2), `(*budget.Tracker).Record`, `budget.Event`.
- Produces:
  - `type recordUsageArgs struct { Channel string; Messages int; TokensIn int; TokensOut int; Verb string; Model string; Success *bool; Project string; RunID string }`
  - `type recordResult struct { Recorded bool; Budget budgetSnapshot }`
  - `func handleRecordUsage(ctx context.Context, t *budget.Tracker, a recordUsageArgs) (recordResult, error)`

- [ ] **Step 1: Write the failing tests**

Append to `cmd/styx/mcp_test.go`:

```go
func TestHandleRecordUsage_AppendsAndReflects(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	ctx := context.Background()
	res, err := handleRecordUsage(ctx, tr, recordUsageArgs{Channel: "claude", Messages: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Recorded {
		t.Fatal("expected recorded=true")
	}
	st, err := tr.State(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionCount != 3 {
		t.Fatalf("tracker session count = %d, want 3", st.SessionCount)
	}
	if res.Budget.SessionCount != 3 {
		t.Fatalf("result budget session count = %d, want 3", res.Budget.SessionCount)
	}
}

func TestHandleRecordUsage_RequiresChannel(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	if _, err := handleRecordUsage(context.Background(), tr, recordUsageArgs{}); err == nil {
		t.Fatal("expected error for missing channel")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run TestHandleRecordUsage -v`
Expected: FAIL — `undefined: handleRecordUsage`, `undefined: recordUsageArgs`.

- [ ] **Step 3: Write the implementation**

Append to `cmd/styx/mcp.go`:

```go
type recordUsageArgs struct {
	Channel   string `json:"channel"`
	Messages  int    `json:"messages"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Verb      string `json:"verb"`
	Model     string `json:"model"`
	Success   *bool  `json:"success"`
	Project   string `json:"project"`
	RunID     string `json:"run_id"`
}

type recordResult struct {
	Recorded bool           `json:"recorded"`
	Budget   budgetSnapshot `json:"budget"`
}

// handleRecordUsage records usage a consumer performed against a channel. The
// budget windows count rows (one row == one message), so Messages>1 emits that
// many rows; token totals ride the first row. Defaults: Messages=1, Success=true.
func handleRecordUsage(ctx context.Context, t *budget.Tracker, a recordUsageArgs) (recordResult, error) {
	if a.Channel == "" {
		return recordResult{}, fmt.Errorf("record_usage: channel is required")
	}
	success := true
	if a.Success != nil {
		success = *a.Success
	}
	n := a.Messages
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		ev := budget.Event{
			Channel: a.Channel,
			Verb:    a.Verb,
			Model:   a.Model,
			Success: success,
			Project: a.Project,
			RunID:   a.RunID,
		}
		if i == 0 {
			ev.TokensIn = a.TokensIn
			ev.TokensOut = a.TokensOut
		}
		if err := t.Record(ctx, ev); err != nil {
			return recordResult{}, fmt.Errorf("record_usage: %w", err)
		}
	}
	return recordResult{Recorded: true, Budget: budgetSnapshotFor(ctx, t, a.Channel)}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/styx/ -run TestHandleRecordUsage -v`
Expected: PASS.

- [ ] **Step 5: Vet, format, commit**

```bash
go vet ./cmd/styx/ && gofmt -w cmd/styx/
git add cmd/styx/mcp.go cmd/styx/mcp_test.go
git commit -m "feat(mcp): record_usage tool handler keeps budget accurate"
```

---

### Task 5: `styx mcp` command + tool assembly + dispatch wiring

Assemble the three handlers into named MCP tools with JSON schemas, run the server on stdin/stdout, and register the `mcp` verb.

**Files:**
- Modify: `cmd/styx/mcp.go`
- Modify: `cmd/styx/dispatch.go` (second switch, after `loadApp()`)
- Test: `cmd/styx/mcp_test.go`

**Interfaces:**
- Consumes: `mcpserver.New`, `mcpserver.Tool`, `(*mcpserver.Server).Serve`, the three handlers, the `app` struct, `logStatus`.
- Produces:
  - `func mcpTools(a *app) []mcpserver.Tool`
  - `func cmdMCP(a *app, args []string) error`

- [ ] **Step 1: Write the failing test**

Append to `cmd/styx/mcp_test.go` (add imports `bytes`, `strings`, and `github.com/ishaanbatra/styx/internal/mcpserver` to the file's import block):

```go
func TestMCPTools_EndToEndRoute(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	a := &app{router: r, tracker: tr}
	srv := mcpserver.New("styx", "test", mcpTools(a))
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"route","arguments":{"task":"add dark mode","verb":"build"}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, `"route"`) || !strings.Contains(s, `"budget_status"`) || !strings.Contains(s, `"record_usage"`) {
		t.Fatalf("tools/list missing a tool: %s", s)
	}
	if !strings.Contains(s, "codex") {
		t.Fatalf("route call did not return codex: %s", s)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestMCPTools_EndToEndRoute -v`
Expected: FAIL — `undefined: mcpTools`.

- [ ] **Step 3: Write the implementation**

Add to the import block of `cmd/styx/mcp.go`: `"context"` (already), `"encoding/json"`, `"os"`, and `"github.com/ishaanbatra/styx/internal/mcpserver"`.

Append to `cmd/styx/mcp.go`:

```go
const mcpServerVersion = "0.1.0"

var routeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"task":    map[string]any{"type": "string", "description": "The task or goal, in natural language."},
		"verb":    map[string]any{"type": "string", "description": "Optional styx verb (plan, build, research, review). Defaults to build."},
		"signals": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional routing signal tags; auto-derived from the task if omitted."},
		"project": map[string]any{"type": "string", "description": "Optional project path for context."},
	},
	"required": []string{"task"},
}

var budgetStatusSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"channel": map[string]any{"type": "string", "description": "Optional channel name; omit for all channels."},
	},
}

var recordUsageSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"channel":    map[string]any{"type": "string"},
		"messages":   map[string]any{"type": "integer", "description": "Messages consumed (default 1)."},
		"tokens_in":  map[string]any{"type": "integer"},
		"tokens_out": map[string]any{"type": "integer"},
		"verb":       map[string]any{"type": "string"},
		"model":      map[string]any{"type": "string"},
		"success":    map[string]any{"type": "boolean", "description": "Defaults to true."},
		"project":    map[string]any{"type": "string"},
		"run_id":     map[string]any{"type": "string"},
	},
	"required": []string{"channel"},
}

// mcpTools builds the MCP tool set bound to this app's router and tracker.
func mcpTools(a *app) []mcpserver.Tool {
	return []mcpserver.Tool{
		{
			Name:        "route",
			Description: "Choose which AI coding agent (channel) should handle a task, with budget-aware fallback. Returns the chosen channel, model, fallback chain, transparent reasoning, and the current budget snapshot.",
			InputSchema: routeSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in routeArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("route: invalid arguments: %w", err)
				}
				return handleRoute(ctx, a.router, a.tracker, in)
			},
		},
		{
			Name:        "budget_status",
			Description: "Report subscription budget per channel: 5h and weekly message counts/limits, percentages, and cooldowns.",
			InputSchema: budgetStatusSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in budgetStatusArgs
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &in); err != nil {
						return nil, fmt.Errorf("budget_status: invalid arguments: %w", err)
					}
				}
				return handleBudgetStatus(ctx, a.tracker, in)
			},
		},
		{
			Name:        "record_usage",
			Description: "Record that a consumer ran a channel, so styx's budget stays accurate when something other than styx executed the agent.",
			InputSchema: recordUsageSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in recordUsageArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("record_usage: invalid arguments: %w", err)
				}
				return handleRecordUsage(ctx, a.tracker, in)
			},
		},
	}
}

// cmdMCP runs styx as an MCP stdio server. stdout carries the JSON-RPC
// protocol; status goes to stderr via logStatus. The server runs until the
// host closes stdin (EOF).
func cmdMCP(a *app, args []string) error {
	srv := mcpserver.New("styx", mcpServerVersion, mcpTools(a))
	logStatus("mcp server ready on stdio (route, budget_status, record_usage)")
	return srv.Serve(context.Background(), os.Stdin, os.Stdout)
}
```

Then modify `cmd/styx/dispatch.go`: in the **second** switch (the one after `loadApp()`, alongside `case "auto":` etc.), add:

```go
	case "mcp":
		return cmdMCP(a, args)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/styx/ -v && go build ./...`
Expected: PASS, and the build succeeds (the `mcp` verb compiles into dispatch).

- [ ] **Step 5: Vet, format, commit**

```bash
go vet ./... && gofmt -w cmd/styx/
git add cmd/styx/mcp.go cmd/styx/dispatch.go cmd/styx/mcp_test.go
git commit -m "feat(mcp): styx mcp command, tool schemas, dispatch wiring"
```

---

### Task 6: Docs + tracker (drift contract)

Document the new verb and the OpenClaw integration; update the doc-tree owners and the free-tier tracker.

**Files:**
- Create: `docs/openclaw-integration.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `README.md`
- Modify: `docs/superpowers/free-tier-tracker.md`

- [ ] **Step 1: Create the OpenClaw integration doc**

Create `docs/openclaw-integration.md`:

````markdown
# Using styx from OpenClaw

styx exposes its budget-aware routing brain as an MCP server. OpenClaw (and any
MCP host) can call it to decide which AI coding agent to use and to keep styx's
subscription budget accurate.

## Register

Add styx to `~/.openclaw/openclaw.json`:

```json
{
  "mcpServers": {
    "styx": { "command": "styx", "args": ["mcp"] }
  }
}
```

Restart the gateway. OpenClaw agents can now call:

- **`route`** — `{ "task": "add dark mode", "verb": "build" }` → chosen channel,
  model, fallback chain, reasoning, and budget snapshot.
- **`budget_status`** — `{ "channel": "claude" }` (or omit for all) → 5h / weekly
  message counts, limits, percentages, cooldowns.
- **`record_usage`** — `{ "channel": "claude", "messages": 1 }` → records what a
  run consumed.

## The `record_usage` convention (important)

When OpenClaw (not styx) executes the chosen agent, styx cannot see that usage.
**Call `record_usage` after each run** so the 5h/weekly windows stay correct. If
you don't, `route` and `budget_status` will still work but their budget numbers
go stale — `budget_status` reports `"stale": true` rather than silently guessing.

styx talks JSON-RPC over stdout; never write other output to stdout in the `mcp`
path. Status goes to stderr.
````

- [ ] **Step 2: Update ARCHITECTURE.md**

In `docs/ARCHITECTURE.md`, add a subsystem bullet (in the subsystems list) and bump `last_verified` in the frontmatter to today's date. Add:

```markdown
- **MCP server** (`internal/mcpserver/` + `cmd/styx/mcp.go`): a transport-only
  JSON-RPC-over-stdio MCP server (`styx mcp`) exposing the routing brain as three
  tools — `route`, `budget_status`, `record_usage` — for MCP hosts like OpenClaw.
  Pure stdlib, no provider SDK. stdout carries the protocol; status stays on
  stderr. `cmd/styx/mcp.go` adapts tool args onto `internal/router` and
  `internal/budget`.
```

- [ ] **Step 3: Update README.md**

In `README.md`'s verb table, add a row for `mcp` matching the table's existing columns. Row content:

- Verb: `mcp`
- Description: `Run styx as an MCP stdio server (route / budget_status / record_usage) for OpenClaw and other MCP hosts. See docs/openclaw-integration.md.`

- [ ] **Step 4: Update the free-tier tracker**

In `docs/superpowers/free-tier-tracker.md`:

- In the **Status Board**, set #2 and #6 status to ❌ with note "dropped — redundant with OpenClaw (see MCP pivot)", and add a row:
  `| 7 | styx MCP routing brain (OpenClaw consumer) | 🔵 | feature/styx-mcp-brain | spec + plan landed | — |`
- Append to the **Decision Log**:

```markdown
- [2026-06-29] (pivot) **styx becomes a standalone MCP routing brain; OpenClaw is
  a consumer, not a master.** Spike confirmed OpenClaw consumes external MCP
  servers but has no budget-aware/task-fit agent selection — exactly styx's core,
  so styx fills a real gap. styx keeps its hands (standalone execution unchanged);
  we ADD `styx mcp` (stdio: route / budget_status / record_usage). **Drops #2
  (remote trigger) and #6 (CI offload)** as redundant with OpenClaw. Defers
  #1/#3/#4 (#3/#4 still sharpen the brain). Spec:
  `specs/2026-06-29-styx-mcp-routing-brain-design.md`; plan:
  `plans/2026-06-29-styx-mcp-routing-brain.md`.
```

- [ ] **Step 5: Verify build/tests still green, then commit**

```bash
go vet ./... && gofmt -w . && go test ./...
git add docs/openclaw-integration.md docs/ARCHITECTURE.md README.md docs/superpowers/free-tier-tracker.md
git commit -m "docs(mcp): styx mcp verb, OpenClaw integration guide, tracker pivot"
```

---

## Self-Review

**Spec coverage:**
- `styx mcp` stdio server → Tasks 1 + 5. ✅
- `route` / `budget_status` / `record_usage` tools → Tasks 2 / 3 / 4. ✅
- Reuse `internal/router` + `internal/budget` (thin adapter) → Tasks 2–4 call them directly. ✅
- `record_usage` keeps budget honest; loud staleness → Task 4 + `budgetSnapshotFor` `Stale` flag + integration doc. ✅
- stdio-only, no remote/daemon → Task 5 (`os.Stdin/os.Stdout`, EOF-terminated). ✅
- No provider SDK / no new dep → Global Constraints + Task 1 (stdlib). ✅
- OpenClaw registration doc → Task 6. ✅
- Drop #2/#6; drift contract (ARCHITECTURE.md + README same commit) → Task 6. ✅
- Keep the hands (nothing removed) → no task deletes existing code; only additive files + one dispatch case. ✅
- Future `run` tool / remote transport → intentionally out of scope (spec "Future"); no task. ✅

**Placeholder scan:** No TBD/TODO; every code step shows complete code; every run step shows the command and expected result. ✅

**Type consistency:** `routeArgs`, `routeResult`, `budgetSnapshot`, `budgetStatusArgs`, `recordUsageArgs`, `recordResult` defined once (Tasks 2–4) and reused consistently; `budgetSnapshotFor`, `handleRoute`, `handleBudgetStatus`, `handleRecordUsage`, `mcpTools`, `cmdMCP`, `mcpserver.New`/`Tool`/`Serve` names match across tasks. `*budget.Tracker` is used as `router.BudgetSource` in tests (verified compatible). ✅
