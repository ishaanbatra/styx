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
}

func TestClaudeContextWindow(t *testing.T) {
	a := NewClaudeAdapter()
	if a.ContextWindow() != 1000000 {
		t.Errorf("ContextWindow = %d, want 1000000 (opus/sonnet/fable are 1M-class)", a.ContextWindow())
	}
	t.Setenv("CLAUDE_CODE_DISABLE_1M_CONTEXT", "1")
	if a.ContextWindow() != 200000 {
		t.Errorf("with 1M disabled, ContextWindow = %d, want 200000", a.ContextWindow())
	}
	b := &ClaudeAdapter{Window: 12345}
	if b.ContextWindow() != 12345 {
		t.Errorf("explicit Window override must win, got %d", b.ContextWindow())
	}
}

func TestAgyPlainAdapter(t *testing.T) {
	ag := NewAgyAdapter()
	got := ag.BuildArgs("summarize", "", "", nil, false)
	want := []string{"-p", "summarize", "--dangerously-skip-permissions"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agy args = %v, want %v", got, want)
	}
	if ag.SupportsResume() || ag.SupportsStream() {
		t.Error("agy adapter is plain: no resume, no stream")
	}
}

func TestCodexAdapterArgs(t *testing.T) {
	a := NewCodexAdapter()
	if !a.SupportsResume() || !a.SupportsStream() {
		t.Fatal("codex adapter must be resume- and stream-capable")
	}
	if a.ContextWindow() != 400000 {
		t.Fatalf("codex window = %d, want 400000 (GPT-5.5 in Codex)", a.ContextWindow())
	}

	fresh := a.BuildArgs("fix it", "", "gpt-5.5", nil, false)
	want := []string{"--model", "gpt-5.5", "exec", "--json", "--sandbox", "workspace-write", "fix it"}
	if !reflect.DeepEqual(fresh, want) {
		t.Fatalf("fresh args = %v, want %v", fresh, want)
	}

	resumed := a.BuildArgs("continue", "th-9", "", []string{"--add-dir", "/x"}, true)
	want = []string{"exec", "resume", "th-9", "--json", "--add-dir", "/x", "continue"}
	if !reflect.DeepEqual(resumed, want) {
		t.Fatalf("resume args = %v, want %v", resumed, want)
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
