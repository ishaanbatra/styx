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
	"sync"
)

// protocolVersion is the MCP revision this server advertises.
const protocolVersion = "2024-11-05"

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

func (s *Server) write(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(v)
}

// progressKey carries the per-call progress emitter through handler context.
type progressKey struct{}

// ProgressFn returns the progress emitter installed for this tool call, if
// the client requested progress (params._meta.progressToken). Handlers call
// it to narrate long-running work; it is nil-safe via the ok bool.
func ProgressFn(ctx context.Context) (func(progress float64, message string), bool) {
	fn, ok := ctx.Value(progressKey{}).(func(float64, string))
	return fn, ok
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
	Meta      struct {
		ProgressToken json.RawMessage `json:"progressToken"`
	} `json:"_meta"`
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
	if tool.Serial {
		s.serialMu.Lock()
		defer s.serialMu.Unlock()
	}
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
