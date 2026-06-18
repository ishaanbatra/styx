package modelsync

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefresh_MigratesAndCaches(t *testing.T) {
	dir := t.TempDir()
	routing := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(routing, []byte("[[rule]]\nverb=\"x\"\nuse=\"codex:gpt-5.5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "models.json")

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	err := Refresh(context.Background(), Options{
		RoutingPath: routing,
		CachePath:   cache,
		Now:         now,
		Discoverers: []Discoverer{ClaudeDiscoverer{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(routing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `use="codex"`) && !strings.Contains(string(got), `use = "codex"`) {
		t.Errorf("routing not migrated:\n%s", got)
	}
	c, err := LoadCache(cache)
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshedAt.IsZero() {
		t.Error("cache timestamp not written")
	}
}
