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
			name: "assistant tool-use only (no text) ignored",
			line: `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"}]}}`,
			ok:   false,
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
