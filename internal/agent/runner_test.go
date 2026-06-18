package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeBin(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunnerSendStreamsAndCapturesSession(t *testing.T) {
	t.Setenv("FAKEAGENT_SESSION", "sess-42")
	t.Setenv("FAKEAGENT_TEXT", "did the thing")
	t.Setenv("FAKEAGENT_IN", "1234")
	t.Setenv("FAKEAGENT_OUT", "56")

	th := &Thread{Name: "claude", CLI: "claude"}
	var events []Event
	r := &Runner{
		Adapter: &ClaudeAdapter{BinPath: fakeBin(t)},
		Thread:  th,
		OnEvent: func(e Event) { events = append(events, e) },
		Timeout: 10 * time.Second,
	}
	res, err := r.Send(context.Background(), "do the thing", "sonnet", nil, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Text != "did the thing" || res.InputTokens != 1234 || res.OutputTokens != 56 {
		t.Errorf("result = %+v", res)
	}
	if th.SessionID != "sess-42" {
		t.Errorf("SessionID = %q, want sess-42", th.SessionID)
	}
	if th.ContextTokens != 1234+56 || th.Turns != 1 {
		t.Errorf("thread meter = tokens %d turns %d", th.ContextTokens, th.Turns)
	}
	if len(events) < 3 {
		t.Fatalf("got %d events, want >= 3 (init, text, result)", len(events))
	}
	if events[0].Type != EventInit || events[len(events)-1].Type != EventResult {
		t.Errorf("event order: first=%s last=%s", events[0].Type, events[len(events)-1].Type)
	}
}

func TestRunnerSendPassesResumeArg(t *testing.T) {
	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKEAGENT_ARGS_LOG", log)

	th := &Thread{Name: "claude", CLI: "claude", SessionID: "sess-7"}
	r := &Runner{Adapter: &ClaudeAdapter{BinPath: fakeBin(t)}, Thread: th, Timeout: 10 * time.Second}
	if _, err := r.Send(context.Background(), "continue", "haiku", nil, false); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "--resume sess-7") {
		t.Errorf("args log missing resume: %s", b)
	}
}

func TestRunnerSendFailsOnResumeError(t *testing.T) {
	t.Setenv("FAKEAGENT_FAIL_RESUME", "1")
	th := &Thread{Name: "claude", CLI: "claude", SessionID: "gone"}
	r := &Runner{Adapter: &ClaudeAdapter{BinPath: fakeBin(t)}, Thread: th, Timeout: 10 * time.Second}
	if _, err := r.Send(context.Background(), "continue", "", nil, false); err == nil {
		t.Fatal("want error when resume fails, got nil")
	}
}

func TestRunnerPlainAdapter(t *testing.T) {
	// Plain adapters capture whole stdout as the result (no stream parsing).
	// echo prints its args, simulating a plain CLI.
	th := &Thread{Name: "codex", CLI: "codex"}
	r := &Runner{
		Adapter: &PlainAdapter{
			CLIName: "codex",
			BinPath: "echo",
			Window:  200000,
			ArgsFn:  func(msg, model string, extra []string) []string { return []string{msg} },
		},
		Thread:  th,
		Timeout: 10 * time.Second,
	}
	res, err := r.Send(context.Background(), "hello plain", "", nil, false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if res.Text != "hello plain" {
		t.Errorf("text = %q", res.Text)
	}
	if th.Turns != 1 {
		t.Errorf("turns = %d", th.Turns)
	}
}
