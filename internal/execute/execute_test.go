package execute

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestApply_InvokesClaudeWithSkipPermissionsAndPrompt(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	dir := fakeClaude(t, `echo "$@" > "$STYX_ARGS_FILE"; echo "done"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("STYX_ARGS_FILE", argsFile)

	stdout, err := Apply(context.Background(), Options{
		PlanContent: "# Plan\n\nDo the thing.",
		ProjectPath: "/some/proj",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout == "" {
		t.Error("expected non-empty stdout")
	}
	captured, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--dangerously-skip-permissions", "-p", "implement this plan"}
	for _, w := range want {
		if !contains(string(captured), w) {
			t.Errorf("missing %q in claude args: %s", w, captured)
		}
	}
}

func TestApply_NonZeroExitIsError(t *testing.T) {
	dir := fakeClaude(t, `echo "boom" >&2; exit 7`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	_, err := Apply(context.Background(), Options{PlanContent: "x", ProjectPath: "/p"})
	if err == nil {
		t.Fatal("expected error on non-zero exit")
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
