package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/modelsync"
)

func TestMaybeRefreshModels_OnlyWhenStale(t *testing.T) {
	dir := t.TempDir()
	routing := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(routing, []byte("[[rule]]\nverb=\"x\"\nuse=\"codex\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "models.json")

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	if err := (&modelsync.Cache{RefreshedAt: now}).Save(cache); err != nil {
		t.Fatal(err)
	}
	did, err := maybeRefreshModels(routing, cache, 24, now.Add(1*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if did {
		t.Error("should not refresh when cache is fresh")
	}

	did, err = maybeRefreshModels(routing, cache, 24, now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Error("should refresh when cache is stale")
	}
}
