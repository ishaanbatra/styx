package agent

import "testing"

func TestParseClaudeEvent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Event
		ok   bool
	}{
		{
			name: "init",
			line: `{"type":"system","subtype":"init","session_id":"abc-123"}`,
			want: Event{Type: EventInit, SessionID: "abc-123"},
			ok:   true,
		},
		{
			name: "assistant text",
			line: `{"type":"assistant","message":{"content":[{"type":"text","text":"working on it"}]}}`,
			want: Event{Type: EventText, Text: "working on it"},
			ok:   true,
		},
		{
			name: "result with usage",
			line: `{"type":"result","subtype":"success","result":"done","session_id":"abc-123","is_error":false,"usage":{"input_tokens":5,"cache_creation_input_tokens":2000,"cache_read_input_tokens":80000,"output_tokens":350}}`,
			want: Event{Type: EventResult, Text: "done", SessionID: "abc-123", InputTokens: 82005, OutputTokens: 350},
			ok:   true,
		},
		{
			name: "error result",
			line: `{"type":"result","subtype":"error_during_execution","result":"boom","is_error":true,"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: Event{Type: EventResult, Text: "boom", InputTokens: 1, OutputTokens: 1, IsError: true},
			ok:   true,
		},
		{
			name: "other system event ignored",
			line: `{"type":"system","subtype":"hook_started"}`,
			ok:   false,
		},
		{
			name: "garbage ignored",
			line: `not json`,
			ok:   false,
		},
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
		{
			name: "claude tool_use with no input surfaces target-less EventTool",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"TodoWrite"}]}}`,
			want: Event{Type: EventTool, Tool: "TodoWrite", Text: ""},
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ParseClaudeEvent([]byte(tt.line))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Errorf("event = %+v, want %+v", got, tt.want)
			}
		})
	}
}

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
		{"command_execution without command field surfaces as tool",
			`{"type":"item.completed","item":{"type":"command_execution"}}`,
			Event{Type: EventTool, Tool: "Bash"}, true},
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
			want: Event{Type: EventTool, Tool: "Bash", Text: "go build ./..."},
			ok:   true,
		},
		{
			name: "file_change item surfaces first changed path",
			line: `{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"internal/activity/watcher.go","kind":"update"}]}}`,
			want: Event{Type: EventTool, Tool: "Edit", Text: "internal/activity/watcher.go"},
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

func TestToolEventSummariesCarryVerbAndTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parse func([]byte) (Event, bool)
		line  string
		want  string
	}{
		{
			name: "codex file change", parse: ParseCodexEvent,
			line: `{"type":"item.completed","item":{"type":"file_change","path":"cmd/styx/mcp.go"}}`,
			want: "Edit: cmd/styx/mcp.go",
		},
		{
			name: "codex command", parse: ParseCodexEvent,
			line: `{"type":"item.completed","item":{"type":"command_execution","command":"go test ./...\nignored"}}`,
			want: "Bash: go test ./...",
		},
		{
			name: "claude file tool", parse: ParseClaudeEvent,
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"internal/agent/event.go"}}]}}`,
			want: "Edit: internal/agent/event.go",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, ok := tc.parse([]byte(tc.line))
			if !ok {
				t.Fatal("event was not parsed")
			}
			if got := summarize(event); got != tc.want {
				t.Fatalf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}
