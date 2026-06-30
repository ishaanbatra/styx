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
