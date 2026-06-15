package agent

import (
	"reflect"
	"testing"
)

func TestClaudeAdapterBuildArgs(t *testing.T) {
	a := NewClaudeAdapter()
	tests := []struct {
		name      string
		msg       string
		sessionID string
		model     string
		extra     []string
		want      []string
	}{
		{
			name:  "fresh session",
			msg:   "hello",
			model: "sonnet",
			want: []string{"--model", "sonnet", "-p", "hello",
				"--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"},
		},
		{
			name:      "resume with extra options",
			msg:       "continue",
			sessionID: "abc",
			model:     "fable",
			extra:     []string{"--add-dir", "../other"},
			want: []string{"--resume", "abc", "--model", "fable", "--add-dir", "../other",
				"-p", "continue", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"},
		},
		{
			name: "no model",
			msg:  "hi",
			want: []string{"-p", "hi", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.BuildArgs(tt.msg, tt.sessionID, tt.model, tt.extra)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("args = %v\nwant  %v", got, tt.want)
			}
		})
	}
	if !a.SupportsResume() || !a.SupportsStream() {
		t.Error("claude adapter must support resume and stream")
	}
	if a.ContextWindow() != 200000 {
		t.Errorf("ContextWindow = %d, want 200000", a.ContextWindow())
	}
}

func TestPlainAdapters(t *testing.T) {
	cx := NewCodexAdapter()
	got := cx.BuildArgs("check this", "", "gpt-5", nil)
	want := []string{"--model", "gpt-5", "exec", "check this"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("codex args = %v, want %v", got, want)
	}
	if cx.SupportsResume() || cx.SupportsStream() {
		t.Error("codex adapter is plain in v1: no resume, no stream")
	}

	ag := NewAgyAdapter()
	got = ag.BuildArgs("summarize", "", "", nil)
	want = []string{"-p", "summarize", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agy args = %v, want %v", got, want)
	}
}
