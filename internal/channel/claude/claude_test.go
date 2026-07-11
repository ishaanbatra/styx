package claude

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

// fakeCLI writes a stub `claude` script to a tmp dir and returns its parent dir
// (suitable for prepending to PATH).
func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSend_OneShotCapturesStdout(t *testing.T) {
	dir := fakeCLI(t, `printf 'planned: %s\n' "$3"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "sonnet-4-6", Prompt: "do thing"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" {
		t.Error("expected non-empty Text")
	}
}

func TestSend_WriteEnabledPassesSkipPermissions(t *testing.T) {
	dir := fakeCLI(t, `echo "$@"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "sonnet-4-6", Prompt: "implement", Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "--dangerously-skip-permissions") {
		t.Errorf("Write request args = %q, want --dangerously-skip-permissions", resp.Text)
	}
}

func TestSend_NoSkipPermissionsByDefault(t *testing.T) {
	dir := fakeCLI(t, `echo "$@"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "sonnet-4-6", Prompt: "summarize"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Text, "dangerously-skip-permissions") {
		t.Errorf("default request must not skip permissions, args = %q", resp.Text)
	}
}

func TestClaudeArgs_Effort(t *testing.T) {
	got := claudeArgs(channel.Request{Prompt: "hi", Model: "opus", Effort: "ultracode"})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--effort ultracode") {
		t.Errorf("missing --effort ultracode in %v", got)
	}
	if !strings.Contains(joined, "--model opus") {
		t.Errorf("missing --model opus in %v", got)
	}
}

func TestSend_NonZeroExitIsError(t *testing.T) {
	dir := fakeCLI(t, `echo "boom" >&2; exit 2`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "sonnet-4-6", Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSend_MissingBinaryIsClassified(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "sonnet-4-6", Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestClaudeArgs_ExtraRoots(t *testing.T) {
	got := claudeArgs(channel.Request{Prompt: "hi", ExtraRoots: []string{"/a", "/b"}})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--add-dir /a") || !strings.Contains(joined, "--add-dir /b") {
		t.Errorf("missing --add-dir roots in %q", joined)
	}
}

func TestClassifyExecError_DeadContextIsTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := exec.Command("false").Run() // produce a real *exec.ExitError
	got := classifyExecError(ctx, err)
	var ce *channel.ClassifiedError
	if !errors.As(got, &ce) {
		t.Fatalf("expected ClassifiedError, got %v", got)
	}
	if ce.Kind != channel.ErrKindTimeout {
		t.Errorf("dead context must classify as timeout, got kind %q", ce.Kind)
	}
}

func TestClassifyExecError_LiveContextNonzeroExitIsOther(t *testing.T) {
	err := exec.Command("false").Run()
	got := classifyExecError(context.Background(), err)
	var ce *channel.ClassifiedError
	if !errors.As(got, &ce) {
		t.Fatalf("expected ClassifiedError, got %v", got)
	}
	if ce.Kind != channel.ErrKindOther {
		t.Errorf("plain exit must classify as other, got kind %q", ce.Kind)
	}
}
