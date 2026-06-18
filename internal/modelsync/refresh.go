package modelsync

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ishaanbatra/styx/internal/memory"
)

const discoverTimeout = 5 * time.Second

// Options configures a Refresh run. All paths are explicit for testability.
type Options struct {
	RoutingPath string
	CachePath   string
	Now         time.Time
	Discoverers []Discoverer
	Store       *memory.Store
	Embed       memory.Embedder
	Log         func(format string, args ...any)
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

// Refresh discovers each channel's current model, migrates legacy
// version-pinned routing tokens, records corrections, and writes the cache.
// Best-effort: a failing discoverer is skipped, not fatal.
func Refresh(ctx context.Context, opts Options) error {
	results := map[string]Result{}
	var claudeAliases []string
	for _, d := range opts.Discoverers {
		dctx, cancel := context.WithTimeout(ctx, discoverTimeout)
		r, err := d.Discover(dctx)
		cancel()
		if err != nil {
			opts.logf("model discovery: %s skipped: %v", d.Channel(), err)
			continue
		}
		results[d.Channel()] = r
		if d.Channel() == "claude" {
			claudeAliases = r.Available
		}
	}

	if claudeAliases != nil {
		src, err := os.ReadFile(opts.RoutingPath)
		if err != nil {
			return fmt.Errorf("read routing for migration: %w", err)
		}
		out, changes := MigrateText(string(src), claudeAliases)
		if len(changes) > 0 {
			tmp := opts.RoutingPath + ".tmp"
			if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
				return fmt.Errorf("write routing tmp: %w", err)
			}
			if err := os.Rename(tmp, opts.RoutingPath); err != nil {
				return fmt.Errorf("rename routing: %w", err)
			}
			for _, c := range changes {
				opts.logf("routing: de-pinned %s -> %s (defer to latest)", c.Old, c.New)
				recordCorrection(ctx, opts, c)
			}
		}
	}

	cache := &Cache{RefreshedAt: opts.Now, Channels: results}
	if err := cache.Save(opts.CachePath); err != nil {
		return fmt.Errorf("save model cache: %w", err)
	}
	return nil
}

func recordCorrection(ctx context.Context, opts Options, c Change) {
	if opts.Store == nil {
		return
	}
	text := fmt.Sprintf("routing: de-pinned %s -> %s (defer to latest)", c.Old, c.New)
	var vec []float32
	if opts.Embed != nil {
		v, err := opts.Embed.Embed(ctx, text)
		if err != nil {
			opts.logf("embed routing correction: %v", err)
		} else {
			vec = v
		}
	}
	_, err := opts.Store.Add(ctx, memory.Item{
		Kind:       memory.KindRoutingPreference,
		Text:       text,
		Source:     "modelsync",
		Project:    "",
		Confidence: 0.9,
		Embedding:  vec,
	})
	if err != nil {
		opts.logf("record routing correction: %v", err)
	}
}
