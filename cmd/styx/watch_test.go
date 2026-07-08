package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/target"
)

// TestWatchProjectIDMatchesREPLAliasResolution is the regression test for the
// Task 9 review finding: `newREPLSession(a, repos...)` resolves an explicit
// first repo via target.Resolve(target.Spec{Alias: repos[0]}) and keys the
// disk mirror on the resulting project ID. `styx watch <alias>` must resolve
// to the identical ID or the two processes silently disagree on a mirror
// path. This asserts watchProjectID(args) with an alias arg produces exactly
// the ID that newREPLSession's own resolution call would produce — without
// needing to spin up a full app/REPL session (which requires ollama/claude
// channels) — by calling the same target.Resolve seam the REPL uses.
func TestWatchProjectIDMatchesREPLAliasResolution(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	otherDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{
		{ID: "other-repo-id", Name: "otherRepo", Path: otherDir},
	}); err != nil {
		t.Fatalf("SaveProjects: %v", err)
	}

	// Run from an unrelated cwd, mirroring the finding's scenario: `styx repl
	// otherRepo` and `styx watch otherRepo` both invoked from cwd X, where X
	// is neither otherRepo's path nor a registered project.
	cwd := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// The REPL's own resolution path (repl.go:678, when repos is non-empty).
	wantProj, err := target.Resolve(target.Spec{Alias: "otherRepo"})
	if err != nil {
		t.Fatalf("target.Resolve(REPL path): %v", err)
	}
	if wantProj.ID != "other-repo-id" {
		t.Fatalf("sanity check: resolved ID = %q, want other-repo-id", wantProj.ID)
	}

	gotID, err := watchProjectID([]string{"otherRepo"})
	if err != nil {
		t.Fatalf("watchProjectID: %v", err)
	}

	if gotID != wantProj.ID {
		t.Fatalf("watch/repl project ID mismatch: watch=%q repl=%q", gotID, wantProj.ID)
	}
}

// TestWatchProjectIDNoArgsMatchesCwdResolution proves the bare `styx watch`
// case (no positional arg) still resolves the same way as the bare REPL
// (resolveGlobalTarget("") in both newREPLSession and newConductorDeps): from
// the project's own directory.
func TestWatchProjectIDNoArgsMatchesCwdResolution(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	projDir := t.TempDir()
	if out, err := exec.Command("git", "init", projDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := config.SaveProjects([]config.Project{
		{ID: "cwd-proj-id", Name: "cwd-proj", Path: projDir},
	}); err != nil {
		t.Fatalf("SaveProjects: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	wantProj, err := resolveGlobalTarget("")
	if err != nil {
		t.Fatalf("resolveGlobalTarget(\"\"): %v", err)
	}

	gotID, err := watchProjectID(nil)
	if err != nil {
		t.Fatalf("watchProjectID(nil): %v", err)
	}

	if gotID != wantProj.ID {
		t.Fatalf("watch/cwd project ID mismatch: watch=%q cwd=%q", gotID, wantProj.ID)
	}
}

// TestWatchMirrorPathUsesStateDir sanity-checks watchMirrorPath's shape:
// <StateDir>/watch/<projectID>.json, matching the writer side in repl.go and
// mcp_conductor.go.
func TestWatchMirrorPathUsesStateDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{
		{ID: "path-proj-id", Name: "path-proj", Path: projDir},
	}); err != nil {
		t.Fatalf("SaveProjects: %v", err)
	}

	got, err := watchMirrorPath([]string{"path-proj"})
	if err != nil {
		t.Fatalf("watchMirrorPath: %v", err)
	}

	want := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "styx", "state", "watch", "path-proj-id.json")
	if got != want {
		t.Fatalf("watchMirrorPath = %q, want %q", got, want)
	}
}
