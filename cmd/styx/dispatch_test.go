package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/memory"
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
	did, err := maybeRefreshModels(routing, cache, 24, now.Add(1*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if did {
		t.Error("should not refresh when cache is fresh")
	}

	did, err = maybeRefreshModels(routing, cache, 24, now.Add(48*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Error("should refresh when cache is stale")
	}
}

// TestMaybeRefreshModels_RecordsCorrection proves the de-pin migration is
// recorded as a routing-preference memory when a correction store is supplied
// (the wiring that was previously inert because no call site passed a store).
func TestMaybeRefreshModels_RecordsCorrection(t *testing.T) {
	dir := t.TempDir()
	routing := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(routing, []byte("[[rule]]\nverb=\"x\"\nuse=\"claude:opus-4-7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "models.json")

	store, err := memory.Open(filepath.Join(dir, "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// nil embedder: the correction is recorded with no vector (no ollama needed).
	opener := func() (*memory.Store, memory.Embedder, func()) {
		return store, nil, func() {}
	}
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	did, err := maybeRefreshModels(routing, cache, 24, now, opener)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("expected a refresh on an empty cache")
	}

	items, err := store.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.Kind == memory.KindRoutingPreference && strings.Contains(it.Text, "de-pinned") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a routing-preference correction memory, got %+v", items)
	}
}
