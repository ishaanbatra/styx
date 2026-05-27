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

func TestDetectTestCommand(t *testing.T) {
	cases := []struct {
		name      string
		framework string
		wantCmd   []string
	}{
		{"pytest", "pytest", []string{"pytest"}},
		{"jest", "jest", []string{"npm", "test"}},
		{"vitest", "vitest", []string{"npm", "test"}},
		{"go test", "go test", []string{"go", "test", "./..."}},
		{"cargo test", "cargo test", []string{"cargo", "test"}},
		{"unknown framework yields nil", "homegrown", nil},
		{"empty framework yields nil", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DetectTestCommand(c.framework)
			if len(got) != len(c.wantCmd) {
				t.Fatalf("got %v, want %v", got, c.wantCmd)
			}
			for i, w := range c.wantCmd {
				if got[i] != w {
					t.Errorf("got[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestRunTests_PassingCommand(t *testing.T) {
	res, err := RunTests(context.Background(), t.TempDir(), []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Errorf("expected pass, got fail. output: %s", res.Output)
	}
}

func TestRunTests_FailingCommand(t *testing.T) {
	res, err := RunTests(context.Background(), t.TempDir(), []string{"false"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Error("expected fail, got pass")
	}
}

func TestRunTests_NilCommandSkips(t *testing.T) {
	res, err := RunTests(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Error("expected Skipped=true for nil command")
	}
}

func TestFixLoop_StopsWhenFixerSays_FIXED(t *testing.T) {
	attempts := 0
	verify := func(ctx context.Context) (bool, string) {
		attempts++
		// Pass on the second attempt.
		return attempts >= 2, "test output line " + itoaShim(attempts)
	}
	fix := func(ctx context.Context, problem string, attempt int) error {
		return nil
	}
	res, err := FixLoop(context.Background(), FixLoopOptions{
		MaxAttempts: 5,
		Verify:      verify,
		Fix:         fix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fixed {
		t.Errorf("expected Fixed=true, got: %+v", res)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", res.Attempts)
	}
}

func TestFixLoop_ExhaustsAttempts(t *testing.T) {
	verify := func(ctx context.Context) (bool, string) { return false, "still broken" }
	fix := func(ctx context.Context, problem string, attempt int) error { return nil }
	res, err := FixLoop(context.Background(), FixLoopOptions{
		MaxAttempts: 3,
		Verify:      verify,
		Fix:         fix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Fixed {
		t.Error("expected Fixed=false after exhaustion")
	}
	if res.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", res.Attempts)
	}
}

func itoaShim(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestShip_PushOnly(t *testing.T) {
	// Fake git so 'push' succeeds without touching a real remote.
	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "git"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gitDir+":"+os.Getenv("PATH"))
	res, err := Ship(context.Background(), ShipOptions{
		ProjectPath: t.TempDir(),
		Branch:      "feat/x",
		NoPR:        true,
		NoPush:      false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed {
		t.Error("expected Pushed=true")
	}
	if res.PRURL != "" {
		t.Errorf("expected no PR URL, got %q", res.PRURL)
	}
}

func TestShip_NoPushSkipsPushAndPR(t *testing.T) {
	res, err := Ship(context.Background(), ShipOptions{
		ProjectPath: t.TempDir(),
		Branch:      "feat/y",
		NoPush:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed {
		t.Error("expected Pushed=false with NoPush=true")
	}
}
