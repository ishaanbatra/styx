package main

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPreToolDecision(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		command  string // for Bash
		wantDeny bool
	}{
		{"websearch denied", "WebSearch", "", true},
		{"webfetch denied", "WebFetch", "", true},
		{"task subagent denied", "Task", "", true},
		{"mcp exa web search denied", "mcp__exa__web_search_exa", "", true},
		{"mcp exa web fetch denied", "mcp__exa__web_fetch_exa", "", true},
		{"mcp context7 docs allowed", "mcp__context7__query-docs", "", false},
		{"mcp gmail allowed", "mcp__claude_ai_Gmail__get_message", "", false},
		{"mcp calendar allowed", "mcp__claude_ai_Google_Calendar__list_events", "", false},
		{"bash curl remote denied", "Bash", "curl -s https://pkg.go.dev/x | sed s/a/b/", true},
		{"bash wget remote denied", "Bash", "wget https://example.com/report.html", true},
		{"bash curl localhost allowed", "Bash", "curl -s http://localhost:8080/health", false},
		{"bash curl 127.0.0.1 allowed", "Bash", "curl http://127.0.0.1:3000", false},
		{"bash go build allowed", "Bash", "go build ./...", false},
		{"bash git status allowed", "Bash", "git status", false},
		{"bash git clone https allowed", "Bash", "git clone https://github.com/x/y", false},
		{"read allowed", "Read", "", false},
		{"grep allowed", "Grep", "", false},
		{"edit allowed", "Edit", "", false},
		{"unknown tool allowed", "SomethingElse", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := hookInput{ToolName: tt.tool}
			if tt.command != "" {
				in.ToolInput = json.RawMessage(mustJSON(t, map[string]string{"command": tt.command}))
			}
			deny, reason := preToolDecision(in)
			if deny != tt.wantDeny {
				t.Fatalf("preToolDecision(%s) deny = %v, want %v", tt.tool, deny, tt.wantDeny)
			}
			if deny && reason == "" {
				t.Fatalf("deny with empty reason")
			}
		})
	}
}

func TestPreToolDecisionMalformedInputAllows(t *testing.T) {
	// A Bash tool_input that isn't valid JSON must never deny (fail-open).
	in := hookInput{ToolName: "Bash", ToolInput: json.RawMessage(`{not json`)}
	if deny, _ := preToolDecision(in); deny {
		t.Fatalf("malformed Bash input denied; want allow")
	}
	// An entirely empty input is allowed.
	if deny, _ := preToolDecision(hookInput{}); deny {
		t.Fatalf("empty input denied; want allow")
	}
}

func TestAppendInlineActivity(t *testing.T) {
	dir := t.TempDir()
	in := hookInput{ToolName: "Read", CWD: "/repo", SessionID: "sess-1"}
	if err := appendInlineActivity(dir, in); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := appendInlineActivity(dir, hookInput{ToolName: "Grep", SessionID: "sess-1"}); err != nil {
		t.Fatalf("append 2: %v", err)
	}

	path := filepath.Join(dir, "inline-activity.jsonl")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer f.Close()
	var lines []map[string]string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec map[string]string
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("line not valid JSON: %v", err)
		}
		lines = append(lines, rec)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (one per event)", len(lines))
	}
	if lines[0]["tool"] != "Read" || lines[0]["session_id"] != "sess-1" || lines[0]["cwd"] != "/repo" {
		t.Fatalf("line 0 = %v, missing fields", lines[0])
	}
	if lines[0]["ts"] == "" {
		t.Fatalf("line 0 missing timestamp")
	}
}

func TestPostToolNudge(t *testing.T) {
	// High-signal inline tools get a nudge; quiet reads do not.
	if _, ok := postToolNudge(hookInput{ToolName: "WebSearch"}); !ok {
		t.Errorf("WebSearch should nudge")
	}
	if _, ok := postToolNudge(hookInput{ToolName: "mcp__exa__web_search_exa"}); !ok {
		t.Errorf("mcp web tool should nudge")
	}
	if _, ok := postToolNudge(hookInput{ToolName: "Read"}); ok {
		t.Errorf("Read should not nudge")
	}
	if _, ok := postToolNudge(hookInput{ToolName: "mcp__context7__query-docs"}); ok {
		t.Errorf("context7 should not nudge")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
