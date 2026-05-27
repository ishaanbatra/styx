package claude

import (
	"context"
	"os"
	"path/filepath"
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
