package codex

import (
	"context"
	"os"
	"path/filepath"
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

func TestSend_MissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "gpt-5", Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}
