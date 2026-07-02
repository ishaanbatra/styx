package main

import (
	"os"
	"path/filepath"
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
