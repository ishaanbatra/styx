package intel

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/progress"
)

func gitInit(t *testing.T, dir string) string {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	return dir
}

func gitCommit(t *testing.T, dir, msg string) string {
	t.Helper()
	c := exec.Command("git", "add", "-A")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v (%s)", err, out)
	}
	c = exec.Command("git", "commit", "-q", "-m", msg, "--allow-empty")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v (%s)", err, out)
	}
	c = exec.Command("git", "rev-parse", "HEAD")
	c.Dir = dir
	out, _ := c.Output()
	return strings.TrimSpace(string(out))
}

func TestBuild_AndLoad_RoundTrip(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	repo := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)
	writeFile(t, repo, "go.mod", "module proj\n")
	writeFile(t, repo, "main.go", "package main\nfunc main() {}\n")
	gitCommit(t, repo, "init")

	proj := config.Project{Name: "proj", Path: repo, Language: "go"}

	idx, err := Build(context.Background(), proj, fakeAgyEcho{}, progress.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	if idx.Project != "proj" {
		t.Errorf("Project = %q, want proj", idx.Project)
	}
	if idx.Language != "go" {
		t.Errorf("Language = %q, want go", idx.Language)
	}
	if idx.Conventions.TestFramework != "go test" {
		t.Errorf("TestFramework = %q, want 'go test'", idx.Conventions.TestFramework)
	}
	if len(idx.FileTree) == 0 {
		t.Error("FileTree empty")
	}

	got, err := Load(proj)
	if err != nil {
		t.Fatal(err)
	}
	if got.Project != idx.Project {
		t.Errorf("round-trip: %q vs %q", got.Project, idx.Project)
	}
}

func TestIsStale_NewlyBuilt(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	repo := filepath.Join(t.TempDir(), "p2")
	os.MkdirAll(repo, 0o755)
	gitInit(t, repo)
	writeFile(t, repo, "x.txt", "y")
	gitCommit(t, repo, "init")

	proj := config.Project{Name: "p2", Path: repo}
	if _, err := Build(context.Background(), proj, fakeAgyEcho{}, progress.Quiet()); err != nil {
		t.Fatal(err)
	}
	stale, reason, err := IsStale(proj)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Errorf("freshly built index should not be stale (reason=%q)", reason)
	}
}

func TestIsStale_CommitsSinceBuild(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	repo := filepath.Join(t.TempDir(), "p3")
	os.MkdirAll(repo, 0o755)
	gitInit(t, repo)
	writeFile(t, repo, "x.txt", "y")
	gitCommit(t, repo, "init")

	proj := config.Project{Name: "p3", Path: repo}
	if _, err := Build(context.Background(), proj, fakeAgyEcho{}, progress.Quiet()); err != nil {
		t.Fatal(err)
	}
	// Make 6 commits (cap is 5).
	for i := 0; i < 6; i++ {
		writeFile(t, repo, "x.txt", "y"+itoa(i))
		gitCommit(t, repo, "c"+itoa(i))
	}
	stale, reason, err := IsStale(proj)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Errorf("should be stale after >5 commits, got fresh (reason=%q)", reason)
	}
}

func TestIsStale_OldByAge(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	repo := filepath.Join(t.TempDir(), "p4")
	os.MkdirAll(repo, 0o755)
	gitInit(t, repo)
	writeFile(t, repo, "x.txt", "y")
	gitCommit(t, repo, "init")

	proj := config.Project{Name: "p4", Path: repo}
	idx, err := Build(context.Background(), proj, fakeAgyEcho{}, progress.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	// Forcibly age the BuiltAt by rewriting + saving.
	idx.BuiltAt = time.Now().Add(-30 * 24 * time.Hour)
	if err := Save(proj, idx); err != nil {
		t.Fatal(err)
	}
	stale, _, err := IsStale(proj)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Errorf("expected stale after 30 days, got fresh")
	}
}

func TestBuild_EmitsProgress(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	repo := filepath.Join(t.TempDir(), "prog-proj")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)
	// Create a top-level module directory so buildModuleSummaries has something to narrate.
	if err := os.MkdirAll(filepath.Join(repo, "mymodule"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "mymodule/doc.go", "package mymodule\n")
	writeFile(t, repo, "main.go", "package main\nfunc main() {}\n")
	gitCommit(t, repo, "init")

	proj := config.Project{Name: "prog-proj", Path: repo, Language: "go"}

	var buf bytes.Buffer
	tracker := progress.New(&buf, false, false)

	_, err := Build(context.Background(), proj, fakeAgyEcho{}, tracker)
	if err != nil {
		t.Fatal(err)
	}

	out := buf.String()
	for _, want := range []string{
		"Walking files",
		"Sniffing conventions",
		"Summarizing module",
		"key symbols",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("progress output missing %q; full output:\n%s", want, out)
		}
	}
}

func TestIndexDirKeyedByID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := indexDir(config.Project{ID: "abc123def456", Name: "renamed-since", Path: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(d) != "abc123def456" {
		t.Errorf("indexDir = %q, want .../abc123def456", d)
	}
}

// fakeAgyEcho is an AgyClient stub that returns canned answers for module/key-symbol prompts.
type fakeAgyEcho struct{}

func (fakeAgyEcho) Send(ctx context.Context, prompt string, workingDir string) (string, error) {
	if strings.Contains(prompt, "key symbols") {
		return "ChatService (app/services/chat.py) - central streaming chat orchestrator", nil
	}
	return "Module summary: " + filepath.Base(workingDir), nil
}
