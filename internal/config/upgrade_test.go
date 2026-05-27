package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteRoutingGeminiToAgy(t *testing.T) {
	src := `[budget]
claude.cap_pct = 80
gemini_free.cap_pct = 70

[[rule]]
verb = "research"
use  = "gemini:flash"
fallback = ["gemini:pro", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
`
	got, n := RewriteRoutingGeminiToAgy(src)
	if n != 2 {
		t.Errorf("expected 2 substitutions (gemini:flash + gemini:pro), got %d", n)
	}
	if strings.Contains(got, "gemini:flash") {
		t.Error("gemini:flash still present after rewrite")
	}
	if strings.Contains(got, "gemini:pro") {
		t.Error("gemini:pro still present after rewrite")
	}
	if !strings.Contains(got, "agy:default") {
		t.Error("agy:default not present after rewrite")
	}
	if !strings.Contains(got, "migrated from gemini-cli to agy in v0.2") {
		t.Error("expected migration comment in output")
	}
}

func TestRewriteRoutingNoOp(t *testing.T) {
	src := `[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
`
	got, n := RewriteRoutingGeminiToAgy(src)
	if n != 0 {
		t.Errorf("expected 0 substitutions, got %d", n)
	}
	if got != src {
		t.Error("no-op rewrite should return original")
	}
}

func TestUpgrade_BackupsAndRewrites(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	routingDir := filepath.Join(dir, "styx")
	if err := os.MkdirAll(routingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	routingPath := filepath.Join(routingDir, "routing.toml")
	original := `[[rule]]
verb = "research"
use  = "gemini:flash"
`
	if err := os.WriteFile(routingPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := UpgradeRoutingFile(routingPath)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 substitution, got %d", n)
	}
	// Backup exists
	backup := filepath.Join(routingDir, "routing.v0.1.toml.bak")
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("backup not created: %v", err)
	}
	// New file has agy
	b, _ := os.ReadFile(routingPath)
	if !strings.Contains(string(b), "agy:default") {
		t.Errorf("post-upgrade file missing agy:default: %s", b)
	}
}
