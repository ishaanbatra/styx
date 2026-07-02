package guidance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // match paths_test.go convention

	t.Run("seeds default on first load", func(t *testing.T) {
		got, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !strings.Contains(got, "codex") || !strings.Contains(got, "ship") {
			t.Fatalf("seed missing routing/ship content:\n%s", got)
		}
	})

	t.Run("user edits survive", func(t *testing.T) {
		Load(t.TempDir()) // ensure seeded
		p, _ := guidanceFile()
		if err := os.WriteFile(p, []byte("MY RULES"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := Load(t.TempDir())
		if got != "MY RULES" {
			t.Fatalf("must not overwrite user file, got %q", got)
		}
	})

	t.Run("per-repo override appended", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, "styx"), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(repo, "styx", "guidance.md"), []byte("REPO RULES"), 0o644)
		got, _ := Load(repo)
		if !strings.Contains(got, "REPO RULES") || !strings.Contains(got, "## Project guidance") {
			t.Fatalf("override not appended:\n%s", got)
		}
	})
}
