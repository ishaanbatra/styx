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
			got := a.BuildArgs(tt.msg, tt.sessionID, tt.model, tt.extra, false)
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
	got := cx.BuildArgs("check this", "", "gpt-5", nil, false)
	want := []string{"--model", "gpt-5", "exec", "check this"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("codex args = %v, want %v", got, want)
	}
	if cx.SupportsResume() || cx.SupportsStream() {
		t.Error("codex adapter is plain in v1: no resume, no stream")
	}

	ag := NewAgyAdapter()
	got = ag.BuildArgs("summarize", "", "", nil, false)
	want = []string{"-p", "summarize", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agy args = %v, want %v", got, want)
	}
}

func TestClaudeArgsReadOnlyDropsSkipPermissions(t *testing.T) {
	rw := claudeArgs("s", "sonnet", "do it", nil, false)
	if !containsArg(rw, "--dangerously-skip-permissions") {
		t.Error("read-write dispatch should pre-grant permissions")
	}
	ro := claudeArgs("s", "sonnet", "explain", nil, true)
	if containsArg(ro, "--dangerously-skip-permissions") {
		t.Error("read-only dispatch must NOT pre-grant permissions")
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestCodexAdapterPlacesExtraAfterExec(t *testing.T) {
	a := NewCodexAdapter()
	got := a.BuildArgs("hello", "", "opus", []string{"--add-dir", "/a"}, false)
	// --add-dir must come AFTER exec and before the message.
	want := []string{"--model", "opus", "exec", "--add-dir", "/a", "hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}
