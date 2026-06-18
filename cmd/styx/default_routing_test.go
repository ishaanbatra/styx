package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
)

func TestDefaultRouting_NoVersionPins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(path, []byte(defaultRoutingTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := config.LoadRoutingFile(path)
	if err != nil {
		t.Fatalf("seeded routing must parse: %v", err)
	}
	if strings.Contains(defaultRoutingTOML, "codex:gpt") {
		t.Error("seeded routing still pins a codex version")
	}
	if strings.Contains(defaultRoutingTOML, "claude:opus-4") || strings.Contains(defaultRoutingTOML, "claude:sonnet-4") {
		t.Error("seeded routing still pins a claude version")
	}
	if r.Models.RefreshIntervalHours == 0 {
		t.Error("seeded routing missing [models] (defaults not applied?)")
	}
}
