package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
)

func gitInitScan(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v (%s)", dir, err, out)
	}
}

func TestScanFindsReposPrunesVendored(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	gitInitScan(t, filepath.Join(root, "api"))
	gitInitScan(t, filepath.Join(root, "web"))
	// A nested repo inside a found repo must NOT be descended into.
	gitInitScan(t, filepath.Join(root, "api", "subrepo"))
	// A repo inside node_modules must be pruned.
	gitInitScan(t, filepath.Join(root, "web", "node_modules", "pkg"))

	if err := cmdProjectScan([]string{root}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	projs, err := config.LoadProjects()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range projs {
		got[p.Path] = true
	}
	if !got[filepath.Join(root, "api")] || !got[filepath.Join(root, "web")] {
		t.Errorf("expected api and web registered, got %v", got)
	}
	if got[filepath.Join(root, "api", "subrepo")] {
		t.Errorf("descended into nested repo")
	}
	if got[filepath.Join(root, "web", "node_modules", "pkg")] {
		t.Errorf("did not prune node_modules")
	}
}
