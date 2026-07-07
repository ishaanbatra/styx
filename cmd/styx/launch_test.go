package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
)

// fakeClaudeOnPath drops a stub `claude` script that records its argv to
// argsFile, and puts its dir first on PATH for the duration of the test.
func fakeClaudeOnPath(t *testing.T) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	return argsFile
}

// setupDispatchEnv seeds a full config dir (routing.toml, state dir) via the
// real ensureFirstRun path and registers one project, mirroring how `styx`
// actually boots before dispatch() runs.
func setupDispatchEnv(t *testing.T) (projectName string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := ensureFirstRun(); err != nil {
		t.Fatalf("ensureFirstRun: %v", err)
	}
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{
		{ID: "wired-proj", Name: "wired-proj", Path: projDir},
	}); err != nil {
		t.Fatal(err)
	}
	return "wired-proj"
}

// TestCmdLaunchNoReposDoesNotPanic is a regression test: cmdLaunch used to
// slice `repos[1:]` unconditionally to find "extra repos", which panics
// ("slice bounds out of range [1:0]") whenever repos is empty — exactly the
// bare-`styx` case main.go calls (cmdLaunch(a) with zero args). It must
// instead resolve the focus project via cwd (like newREPLSession does) and
// launch cleanly with no extra repos.
func TestCmdLaunchNoReposDoesNotPanic(t *testing.T) {
	setupDispatchEnv(t)
	fakeClaudeOnPath(t)

	a, err := loadApp()
	if err != nil {
		t.Fatalf("loadApp: %v", err)
	}
	defer a.tracker.Close()

	if err := cmdLaunch(a); err != nil {
		t.Fatalf("cmdLaunch(a) with no repos: %v", err)
	}
}

// TestCmdLaunchNonGitCwdLaunchesInPlainDir proves bare `styx` outside any git
// repository still launches the conductor, treating the cwd itself as the
// focus directory (no project registration, no error). Explicit targets keep
// their strict resolution; only the implicit-cwd fallback is relaxed.
func TestCmdLaunchNonGitCwdLaunchesInPlainDir(t *testing.T) {
	setupDispatchEnv(t)
	argsFile := fakeClaudeOnPath(t)

	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)

	p, err := resolveLaunchTarget(nil)
	if err != nil {
		t.Fatalf("resolveLaunchTarget in non-git cwd: %v", err)
	}
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(p.Path)
	if gotDir != wantDir {
		t.Fatalf("focus path = %q, want cwd %q", p.Path, dir)
	}

	a, err := loadApp()
	if err != nil {
		t.Fatalf("loadApp: %v", err)
	}
	defer a.tracker.Close()
	if err := cmdLaunch(a); err != nil {
		t.Fatalf("cmdLaunch in non-git cwd: %v", err)
	}
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatalf("expected fake claude to run, args file missing: %v", err)
	}
}

// TestDispatchLaunchVerbRoutesToCmdLaunch proves `styx launch <repo>` reaches
// cmdLaunch (which execs the "claude" host) rather than the classic REPL.
func TestDispatchLaunchVerbRoutesToCmdLaunch(t *testing.T) {
	name := setupDispatchEnv(t)
	argsFile := fakeClaudeOnPath(t)

	if err := dispatch("launch", []string{name}); err != nil {
		t.Fatalf("dispatch(launch): %v", err)
	}
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatalf("expected fake claude to run, args file missing: %v", err)
	}
}

// TestDispatchResolvableRepoTokenRoutesToCmdLaunch proves the bare
// `styx <repo>` default path launches the conductor (not the classic REPL)
// when every positional token resolves as a registered project.
func TestDispatchResolvableRepoTokenRoutesToCmdLaunch(t *testing.T) {
	name := setupDispatchEnv(t)
	argsFile := fakeClaudeOnPath(t)

	if err := dispatch(name, nil); err != nil {
		t.Fatalf("dispatch(%s): %v", name, err)
	}
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatalf("expected fake claude to run, args file missing: %v", err)
	}
}

// readFakeClaudeArgs returns the argv the fake claude stub recorded, one arg
// per line, failing the test if the stub never ran.
func readFakeClaudeArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("expected fake claude to run, args file missing: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

// TestDispatchResumeWithSessionID proves `styx resume <id>` relaunches the
// conductor with the full toolbelt (--mcp-config + --append-system-prompt)
// and appends --resume <id> so Claude Code restores that session.
func TestDispatchResumeWithSessionID(t *testing.T) {
	setupDispatchEnv(t)
	argsFile := fakeClaudeOnPath(t)

	if err := dispatch("resume", []string{"abc123"}); err != nil {
		t.Fatalf("dispatch(resume abc123): %v", err)
	}
	args := readFakeClaudeArgs(t, argsFile)
	at := -1
	for i, a := range args {
		if a == "--resume" {
			at = i
		}
	}
	if at == -1 || at+1 >= len(args) || args[at+1] != "abc123" {
		t.Fatalf("want --resume abc123 in argv, got %v", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--mcp-config") || !strings.Contains(joined, "--append-system-prompt") {
		t.Fatalf("resume must keep the styx toolbelt flags, got %v", args)
	}
}

// TestDispatchResumeNoSessionID proves bare `styx resume` passes --continue
// (Claude Code resumes the most recent session for the directory) while still
// wiring the styx MCP server and guidance.
func TestDispatchResumeNoSessionID(t *testing.T) {
	setupDispatchEnv(t)
	argsFile := fakeClaudeOnPath(t)

	if err := dispatch("resume", nil); err != nil {
		t.Fatalf("dispatch(resume): %v", err)
	}
	args := readFakeClaudeArgs(t, argsFile)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--continue") {
		t.Fatalf("want --continue in argv, got %v", args)
	}
	if strings.Contains(joined, "--resume") {
		t.Fatalf("bare resume must not pass --resume, got %v", args)
	}
	if !strings.Contains(joined, "--mcp-config") || !strings.Contains(joined, "--append-system-prompt") {
		t.Fatalf("resume must keep the styx toolbelt flags, got %v", args)
	}
}

// TestDispatchReplVerbRoutesToClassicREPL proves `styx repl <repo>` still
// reaches cmdREPL, not cmdLaunch: it must NOT invoke the claude host binary.
// Stdin is redirected to an already-closed pipe so the REPL's first
// ReadString hits EOF immediately and the session exits cleanly instead of
// blocking on real terminal input.
func TestDispatchReplVerbRoutesToClassicREPL(t *testing.T) {
	name := setupDispatchEnv(t)
	argsFile := fakeClaudeOnPath(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()

	if err := dispatch("repl", []string{name}); err != nil {
		t.Fatalf("dispatch(repl): %v", err)
	}
	if _, err := os.Stat(argsFile); err == nil {
		t.Fatalf("repl verb must not invoke the claude host binary")
	}
}

// TestEnsureInteractiveTTY proves the conductor refuses to launch when stdin
// isn't a terminal (exec'ing claude on a pipe dies with a confusing
// "--print requires input" error) and passes cleanly when it is.
func TestEnsureInteractiveTTY(t *testing.T) {
	orig := stdinIsTTY
	defer func() { stdinIsTTY = orig }()

	stdinIsTTY = func() bool { return false }
	err := ensureInteractiveTTY()
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("non-TTY stdin must refuse conductor launch, got %v", err)
	}

	stdinIsTTY = func() bool { return true }
	if err := ensureInteractiveTTY(); err != nil {
		t.Fatalf("TTY stdin must pass, got %v", err)
	}
}

// TestConductorGuidanceNamesFocusProject proves the assembled guidance names
// the focus project's registry alias (so the conductor brain knows what to
// pass as `project` on dispatch/thread_status/memory_save) and still folds in
// the extra-repo note and learned routing preferences when present.
func TestConductorGuidanceNamesFocusProject(t *testing.T) {
	got := conductorGuidance("BASE", "styx", "", "")
	if !strings.Contains(got, "`styx`") || !strings.Contains(got, "project") {
		t.Fatalf("guidance must name the focus project alias, got:\n%s", got)
	}
	if !strings.HasPrefix(got, "BASE") {
		t.Fatal("base guidance must come first")
	}
	withExtras := conductorGuidance("BASE", "styx", "- ai-ta: /x (extra)\n", "- prefer codex\n")
	for _, want := range []string{"Bound repos beyond styx", "Routing preferences", "prefer codex"} {
		if !strings.Contains(withExtras, want) {
			t.Fatalf("missing %q in:\n%s", want, withExtras)
		}
	}
}
