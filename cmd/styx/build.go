package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/intel"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func cmdBuild(a *app, args []string) error {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	proj, err := resolveTarget(target)
	if err != nil {
		return err
	}

	// Ensure intel fresh.
	if stale, reason, err := intel.IsStale(proj); err != nil {
		return fmt.Errorf("check intel: %w", err)
	} else if stale {
		fmt.Fprintf(os.Stderr, "[styx] intel index stale (%s) — rebuilding...\n", reason)
		ag, ok := a.channels["agy"]
		if !ok {
			return fmt.Errorf("agy channel not registered, cannot build intel")
		}
		if _, err := intel.Build(context.Background(), proj, &agyAdapter{ch: rawChannel(ag)}, a.progress); err != nil {
			return fmt.Errorf("rebuild intel: %w", err)
		}
	}
	idx, err := intel.Load(proj)
	if err != nil {
		return fmt.Errorf("load intel: %w", err)
	}
	if written, err := intel.WriteContextMD(proj.Path, intel.ToMarkdown(idx)); err != nil {
		return fmt.Errorf("write context.md: %w", err)
	} else {
		rel, _ := filepath.Rel(proj.Path, written)
		fmt.Fprintf(os.Stderr, "[styx] context written to %s\n", rel)
	}

	sigs := signals.Extract("build", args, proj)
	dec, err := a.router.Route(context.Background(), router.Request{Verb: "build", Args: args, Signals: sigs})
	if err != nil {
		return err
	}
	ch, ok := a.channels[dec.Channel]
	if !ok {
		return fmt.Errorf("unknown channel %q for build", dec.Channel)
	}
	fmt.Fprintf(os.Stderr, "[styx] -> %s (%s:%s)\n", proj.Path, dec.Channel, dec.Model)
	_, err = ch.Send(context.Background(), channel.Request{
		Model:       dec.Model,
		Interactive: true,
		WorkingDir:  proj.Path,
	})
	return err
}
