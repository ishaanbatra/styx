package codex

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "codex")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSend_OneShotCapturesStdout(t *testing.T) {
	dir := fakeCLI(t, `echo "codex says hi"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "gpt-5", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "codex says hi" {
		t.Errorf("Text = %q, want codex says hi", resp.Text)
	}
}

func TestSend_WriteEnabledPassesSandboxFlag(t *testing.T) {
	// Fake codex echoes its args so we can assert on the flags passed.
	dir := fakeCLI(t, `echo "$@"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "gpt-5", Prompt: "implement the plan", Write: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "--sandbox workspace-write") {
		t.Errorf("Write request args = %q, want --sandbox workspace-write", resp.Text)
	}
}

func TestSend_ReadOnlyByDefault(t *testing.T) {
	dir := fakeCLI(t, `echo "$@"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "gpt-5", Prompt: "summarize"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Text, "workspace-write") {
		t.Errorf("default request must not enable writes, args = %q", resp.Text)
	}
}

func TestCodexArgs_EffortNoModel(t *testing.T) {
	got := codexArgs(channel.Request{Prompt: "hi", Effort: "high"})
	want := []string{"-c", "model_reasoning_effort=high", "exec", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestCodexArgs_NoModelByDefault(t *testing.T) {
	got := codexArgs(channel.Request{Prompt: "hi"})
	for _, a := range got {
		if a == "--model" {
			t.Errorf("unexpected --model in %v", got)
		}
	}
}

func TestSend_MissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "gpt-5", Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCodexArgs_ExtraRoots(t *testing.T) {
	got := codexArgs(channel.Request{Prompt: "hi", ExtraRoots: []string{"/a", "/b"}})
	want := []string{"exec", "--add-dir", "/a", "--add-dir", "/b", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}
