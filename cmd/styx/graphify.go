package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/ishaanbatra/styx/internal/graph"
	"github.com/ishaanbatra/styx/internal/project"
)

// cmdGraphify is the manual/scripted entry point for graphify graph builds.
// `styx graphify <target>` builds synchronously (the conductor launch path
// does the same work in the background); `styx graphify ls` reports freshness.
func cmdGraphify(a *app, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: styx graphify <project-alias-or-path> [--force] | styx graphify ls")
	}
	if args[0] == "ls" {
		return cmdGraphifyLs()
	}
	force := false
	target := args[0]
	for _, arg := range args[1:] {
		if arg == "--force" {
			force = true
		}
	}

	bin, ok := graph.Available()
	if !ok {
		return errors.New("graphify CLI not available — install with `uv tool install graphifyy` (or unset STYX_GRAPHIFY)")
	}
	proj, err := resolveGlobalTarget(target)
	if err != nil {
		return err
	}
	if proj.ID == "" {
		return fmt.Errorf("%s is not a registered project — run `styx project add` first", proj.Path)
	}
	if !force {
		stale, reason := graph.IsStale(proj)
		if !stale {
			fmt.Printf("[styx] graph for %s is fresh; pass --force to rebuild\n", proj.Name)
			return nil
		}
		logStatus("graph is stale (%s); rebuilding...", reason)
	} else {
		logStatus("forcing graph rebuild for %s", proj.Name)
	}

	st := a.progress.Stage("Building knowledge graph for " + proj.Name)
	ctx, cancel := context.WithTimeout(context.Background(), graph.BuildTimeout)
	defer cancel()
	if err := graph.Build(ctx, proj, bin); err != nil {
		st.Fail(err)
		return fmt.Errorf("graph build: %w", err)
	}
	st.Done("done")
	fmt.Printf("[styx] built knowledge graph for %s -> %s\n", proj.Name, graph.GraphPath(proj))
	return nil
}

func cmdGraphifyLs() error {
	if _, ok := graph.Available(); !ok {
		logStatus("graphify CLI not available — install with `uv tool install graphifyy`")
	}
	projs, err := project.List()
	if err != nil {
		return err
	}
	for _, p := range projs {
		state := "fresh"
		if stale, reason := graph.IsStale(p); stale {
			state = "stale: " + reason
		}
		fmt.Printf("%-20s %-40s %s\n", p.Name, state, p.Path)
	}
	return nil
}
