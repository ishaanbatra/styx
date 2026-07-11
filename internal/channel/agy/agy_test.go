package agy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func fakeAgy(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "agy")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSend_OneShotCapturesStdout(t *testing.T) {
	dir := fakeAgy(t, `echo "agy says: $*"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "default", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" {
		t.Error("expected non-empty Text")
	}
}

func TestSend_AppendsAddDirWhenWorkingDirSet(t *testing.T) {
	// Capture the argv that agy was invoked with by having the fake script dump it.
	dir := fakeAgy(t, `echo "$@" > "$STYX_TEST_ARGS_FILE"; echo ok`)
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("STYX_TEST_ARGS_FILE", argsFile)

	c := New()
	_, err := c.Send(context.Background(), channel.Request{
		Model:      "default",
		Prompt:     "hello",
		WorkingDir: "/some/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !contains(got, "--add-dir") {
		t.Errorf("expected --add-dir in args, got: %s", got)
	}
	if !contains(got, "/some/project") {
		t.Errorf("expected /some/project in args, got: %s", got)
	}
	if !contains(got, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions in args, got: %s", got)
	}
}

func TestSend_InteractiveNotSupported(t *testing.T) {
	dir := fakeAgy(t, `echo ok`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "default", Prompt: "x", Interactive: true})
	if err == nil {
		t.Fatal("expected error for interactive mode")
	}
}

func TestSend_MissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "default", Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestName(t *testing.T) {
	c := New()
	if c.Name() != "agy" {
		t.Errorf("Name() = %q, want agy", c.Name())
	}
}

func TestSend_AppendsExtraRoots(t *testing.T) {
	dir := fakeAgy(t, `echo "$@" > "$STYX_TEST_ARGS_FILE"; echo ok`)
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("STYX_TEST_ARGS_FILE", argsFile)
	c := New()
	if _, err := c.Send(context.Background(), channel.Request{Prompt: "hi", ExtraRoots: []string{"/a", "/b"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(argsFile)
	got := string(b)
	if !contains(got, "/a") || !contains(got, "/b") {
		t.Errorf("expected both roots in args, got: %s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
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
