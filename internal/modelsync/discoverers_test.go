package modelsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeDiscoverer(t *testing.T) {
	r, err := ClaudeDiscoverer{}.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"opus", "sonnet", "haiku", "fable"}
	if len(r.Available) != len(want) {
		t.Fatalf("Available = %v, want %v", r.Available, want)
	}
	if r.Source != "claude-alias" {
		t.Errorf("Source = %q", r.Source)
	}
}

func TestCodexDiscoverer(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("model = \"gpt-5.5\"\nmodel_reasoning_effort = \"medium\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	r, err := CodexDiscoverer{ConfigPath: cfg}.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Current != "gpt-5.5" || r.Source != "codex-config" {
		t.Errorf("got %+v", r)
	}
}

func TestCodexDiscoverer_MissingFile(t *testing.T) {
	_, err := CodexDiscoverer{ConfigPath: filepath.Join(t.TempDir(), "none.toml")}.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestCodexDiscoverer_NoModelLine(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfg, []byte("model_reasoning_effort = \"medium\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := CodexDiscoverer{ConfigPath: cfg}.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error when no model line present")
	}
}
