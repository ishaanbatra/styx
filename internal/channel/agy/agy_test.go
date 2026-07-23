package agy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func TestSend_ForwardsExplicitModel(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  string
	}{
		{
			name:  "pinned model",
			model: "Gemini 3.1 Pro (High)",
			want:  "-p hi --dangerously-skip-permissions --model Gemini 3.1 Pro (High)",
		},
		{name: "default sentinel", model: "default", want: "-p hi --dangerously-skip-permissions"},
		{name: "empty model", model: "", want: "-p hi --dangerously-skip-permissions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeSrc, err := filepath.Abs("../../../testdata/fakeagent")
			if err != nil {
				t.Fatal(err)
			}
			fake, err := os.ReadFile(fakeSrc)
			if err != nil {
				t.Fatal(err)
			}
			binDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(binDir, "agy"), fake, 0o755); err != nil {
				t.Fatal(err)
			}
			argsLog := filepath.Join(t.TempDir(), "args.log")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)

			if _, err := New().Send(context.Background(), channel.Request{Model: tt.model, Prompt: "hi"}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(argsLog)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(got)) != tt.want {
				t.Errorf("agy args = %q, want %q", strings.TrimSpace(string(got)), tt.want)
			}
		})
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

func TestClassifyExecError(t *testing.T) {
	liveCtx := context.Background()
	expiredCtx, cancel := context.WithCancel(context.Background())
	cancel()
	plainExit := exec.Command("false").Run()
	var signalKill error
	if runtime.GOOS != "windows" {
		signalKill = exec.Command("sh", "-c", "kill -9 $$").Run()
	}

	tests := []struct {
		name     string
		ctx      context.Context
		err      error
		want     channel.ErrorKindLabel
		unixOnly bool
	}{
		{name: "live context nonzero exit", ctx: liveCtx, err: plainExit, want: channel.ErrKindOther},
		{name: "expired context nonzero exit", ctx: expiredCtx, err: plainExit, want: channel.ErrKindTimeout},
		{name: "SIGKILL live context", ctx: liveCtx, err: signalKill, want: channel.ErrKindKilled, unixOnly: true},
		{name: "SIGKILL expired context", ctx: expiredCtx, err: signalKill, want: channel.ErrKindTimeout, unixOnly: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.unixOnly && runtime.GOOS == "windows" {
				t.Skip("POSIX signals are unavailable on Windows")
			}
			got := classifyExecError(tt.ctx, tt.err)
			var ce *channel.ClassifiedError
			if !errors.As(got, &ce) {
				t.Fatalf("expected ClassifiedError, got %v", got)
			}
			if ce.Kind != tt.want {
				t.Errorf("kind = %q, want %q", ce.Kind, tt.want)
			}
		})
	}
}
