package modelsync

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Cache is the persisted result of the last model refresh.
type Cache struct {
	RefreshedAt time.Time         `json:"refreshed_at"`
	Channels    map[string]Result `json:"channels"`
}

// LoadCache reads the cache; a missing file yields an empty (stale) cache.
func LoadCache(path string) (*Cache, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Cache{Channels: map[string]Result{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read model cache: %w", err)
	}
	var c Cache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse model cache: %w", err)
	}
	if c.Channels == nil {
		c.Channels = map[string]Result{}
	}
	return &c, nil
}

// Save writes the cache atomically (tmp + rename).
func (c *Cache) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal model cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write model cache tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename model cache: %w", err)
	}
	return nil
}

// IsStale reports whether the cache is older than interval as of now.
func (c *Cache) IsStale(now time.Time, interval time.Duration) bool {
	if c.RefreshedAt.IsZero() {
		return true
	}
	return now.Sub(c.RefreshedAt) > interval
}
