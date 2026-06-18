package modelsync

import (
	"path/filepath"
	"testing"
	"time"
)

func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	c := &Cache{RefreshedAt: now, Channels: map[string]Result{
		"codex": {Current: "gpt-5.5", Source: "codex-config"},
	}}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channels["codex"].Current != "gpt-5.5" {
		t.Errorf("round-trip lost data: %+v", got)
	}
}

func TestLoadCache_Missing(t *testing.T) {
	c, err := LoadCache(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c == nil || len(c.Channels) != 0 {
		t.Errorf("want empty cache, got %+v", c)
	}
}

func TestIsStale(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	c := &Cache{RefreshedAt: base}
	if c.IsStale(base.Add(23*time.Hour), 24*time.Hour) {
		t.Error("23h < 24h should not be stale")
	}
	if !c.IsStale(base.Add(25*time.Hour), 24*time.Hour) {
		t.Error("25h > 24h should be stale")
	}
	empty := &Cache{}
	if !empty.IsStale(base, 24*time.Hour) {
		t.Error("zero RefreshedAt should be stale")
	}
}
