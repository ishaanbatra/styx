package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
}

func TestCurrent_WalksUpToGitRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	gitInit(t, repo)
	writeFiles(t, repo, "pyproject.toml")
	sub := filepath.Join(repo, "src", "deep", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("HOME", t.TempDir())

	p, err := CurrentFrom(sub)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != repo {
		t.Errorf("Path = %q, want %q", p.Path, repo)
	}
	if p.Language != "python" {
		t.Errorf("Language = %q, want python", p.Language)
	}
	if p.Name != "repo" {
		t.Errorf("Name = %q, want repo", p.Name)
	}
}

func TestCurrent_AutoRegistersOnFirstUse(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "newproj")
	gitInit(t, repo)
	writeFiles(t, repo, "go.mod")

	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	p, err := CurrentFrom(repo)
	if err != nil {
		t.Fatal(err)
	}
	if p.DebugDir != "styx/debug" {
		t.Errorf("DebugDir = %q, want styx/debug", p.DebugDir)
	}
	// After CurrentFrom, projects.toml should exist with the new entry.
	registry := filepath.Join(cfgDir, "styx", "projects.toml")
	b, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("registry not written: %v", err)
	}
	if !contains(string(b), "newproj") {
		t.Errorf("registry missing project name; got:\n%s", string(b))
	}
	if !contains(string(b), "go") {
		t.Errorf("registry missing language tag; got:\n%s", string(b))
	}
}

func TestCurrent_NameCollisionAppendsSuffix(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	repo1 := filepath.Join(root1, "shared-name")
	repo2 := filepath.Join(root2, "shared-name")
	gitInit(t, repo1)
	gitInit(t, repo2)

	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	p1, err := CurrentFrom(repo1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := CurrentFrom(repo2)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Name == p2.Name {
		t.Errorf("expected distinct names, got %q == %q", p1.Name, p2.Name)
	}
	if p2.Name != "shared-name-2" {
		t.Errorf("expected shared-name-2, got %q", p2.Name)
	}
}

func TestResolve_LooksUpByAlias(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	root := t.TempDir()
	repo := filepath.Join(root, "alpha")
	gitInit(t, repo)
	if _, err := CurrentFrom(repo); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != repo {
		t.Errorf("Path = %q, want %q", got.Path, repo)
	}
}

func TestForget(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	root := t.TempDir()
	repo := filepath.Join(root, "willforget")
	gitInit(t, repo)
	if _, err := CurrentFrom(repo); err != nil {
		t.Fatal(err)
	}
	if err := Forget("willforget"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("willforget"); err == nil {
		t.Error("expected error resolving forgotten project")
	}
}

func TestFindGitRootAcceptsGitFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findGitRoot(root)
	if err != nil {
		t.Fatalf("findGitRoot: %v", err)
	}
	if got != root {
		t.Errorf("findGitRoot = %q, want %q", got, root)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
