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
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
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
	var resp struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &resp); err != nil {
		t.Fatalf("unmarshal response: %v (%q)", err, lines[0])
	}
	if resp.Result.IsError {
		t.Fatalf("isError should be false: %s", lines[0])
	}
	if len(resp.Result.Content) == 0 || resp.Result.Content[0].Type != "text" {
		t.Fatalf("expected a text content block: %s", lines[0])
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(resp.Result.Content[0].Text), &payload); err != nil {
		t.Fatalf("content text should be JSON: %v (%q)", err, resp.Result.Content[0].Text)
	}
	if !payload.OK {
		t.Fatalf("expected ok=true in tool result, got: %s", resp.Result.Content[0].Text)
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
