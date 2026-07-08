package main

// styx learn — the self-improvement digest verb (spec
// docs/superpowers/specs/2026-07-07-styx-self-improvement-design.md).
// Manual only: no daemons, no background learning. All learning lands as
// plain-text memories with provenance in the global memory store — never
// code changes, never routing.toml edits.

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/learn"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
)

// scorecardWindow is the trailing outcome window styx learn aggregates.
const scorecardWindow = 30 * 24 * time.Hour

func cmdLearn(a *app, args []string) error {
	var scorecardOnly, dryRun, list bool
	var forgetID int64
	var forget bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--scorecard":
			scorecardOnly = true
		case "--dry-run":
			dryRun = true
		case "--list":
			list = true
		case "--forget":
			i++
			if i >= len(args) {
				return fmt.Errorf("--forget needs a memory id (styx learn --list shows ids)")
			}
			id, err := strconv.ParseInt(args[i], 10, 64)
			if err != nil {
				return fmt.Errorf("--forget: bad id %q: %w", args[i], err)
			}
			forgetID, forget = id, true
		default:
			return fmt.Errorf("unknown flag %q (usage: styx learn [--scorecard|--dry-run|--list|--forget <id>])", args[i])
		}
	}
	ctx := context.Background()
	if scorecardOnly {
		out, err := learnScorecard(ctx, a)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	store, err := openGlobalMemory()
	if err != nil {
		return err
	}
	defer store.Close()
	switch {
	case list:
		out, err := learnList(ctx, store)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	case forget:
		out, err := learnForget(ctx, store, forgetID)
		if err != nil {
			return err
		}
		fmt.Print(out)
		return nil
	}
	return runLearn(ctx, a, store, dryRun) // Task 6 implements the digest
}

// runLearn is the digest entry point; replaced by Task 6.
func runLearn(ctx context.Context, a *app, store *memory.Store, dryRun bool) error {
	return fmt.Errorf("styx learn digest not implemented yet — use --scorecard, --list, or --forget")
}

// openGlobalMemory opens ~/.config/styx/state/memory/global.db — where
// learned memories live (the store launch guidance injection reads).
func openGlobalMemory() (*memory.Store, error) {
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, err
	}
	return memory.Open(filepath.Join(memDir, "global.db"))
}

// learnScorecard renders the deterministic cli × signal scorecard over the
// trailing 30 days of dispatch outcomes. No LLM involvement.
func learnScorecard(ctx context.Context, a *app) (string, error) {
	rows, err := a.tracker.OutcomesSince(ctx, time.Now().Add(-scorecardWindow))
	if err != nil {
		return "", fmt.Errorf("read outcomes: %w", err)
	}
	return learn.Build(rows, 30).Render(), nil
}

// learnList renders the learned set (routing + user preferences) with ids
// and provenance so --forget has addressable targets.
func learnList(ctx context.Context, store *memory.Store) (string, error) {
	var b strings.Builder
	total := 0
	for _, kind := range []memory.Kind{memory.KindRoutingPreference, memory.KindUserPreference} {
		items, err := store.TopByKind(ctx, kind, 100)
		if err != nil {
			return "", fmt.Errorf("list %s memories: %w", kind, err)
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s:\n", kind)
		for _, it := range items {
			fmt.Fprintf(&b, "  [%d] %s (source %s, %s, conf %.2f)\n",
				it.ID, it.Text, it.Source, it.CreatedAt.Format("2006-01-02"), it.Confidence)
		}
		total += len(items)
	}
	if total == 0 {
		return "no learned memories yet — dispatch some work, then run styx learn\n", nil
	}
	return b.String(), nil
}

// learnForget hard-deletes one memory by id — the reversibility guarantee.
func learnForget(ctx context.Context, store *memory.Store, id int64) (string, error) {
	if err := store.Delete(ctx, id); err != nil {
		return "", err
	}
	return fmt.Sprintf("forgot memory %d\n", id), nil
}
