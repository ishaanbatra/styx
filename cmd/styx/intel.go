package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/intel"
	"github.com/ishaanbatra/styx/internal/project"
)

// agyAdapter bridges the project's agy channel into the intel.AgyClient interface.
type agyAdapter struct {
	ch channel.Channel
}

func (a *agyAdapter) Send(ctx context.Context, prompt, workingDir string) (string, error) {
	resp, err := a.ch.Send(ctx, channel.Request{
		Model:      "default",
		Prompt:     prompt,
		WorkingDir: workingDir,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func cmdIntel(ctx context.Context, a *app, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: styx intel <project-alias-or-path> | styx intel ls")
	}
	if args[0] == "ls" {
		return cmdIntelLs()
	}
	force := false
	target := args[0]
	for _, arg := range args[1:] {
		if arg == "--force" {
			force = true
		}
	}

	proj, err := resolveGlobalTarget(target)
	if err != nil {
		return err
	}
	if !force {
		stale, reason, err := intel.IsStale(proj)
		if err != nil {
			return err
		}
		if !stale {
			fmt.Printf("[styx] intel index for %s is fresh; pass --force to rebuild\n", proj.Name)
			return nil
		}
		fmt.Printf("[styx] index is stale (%s); rebuilding...\n", reason)
	} else {
		fmt.Printf("[styx] forcing rebuild for %s\n", proj.Name)
	}

	ag, ok := a.channels["agy"]
	if !ok {
		return errors.New("agy channel not registered")
	}
	idx, err := intel.Build(ctx, proj, &agyAdapter{ch: rawChannel(ag)}, a.progress)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	fmt.Printf("[styx] built intel index for %s: %d files, %d modules, %d key symbols\n",
		proj.Name, len(idx.FileTree), len(idx.Modules), len(idx.KeySymbols))
	return nil
}

func cmdIntelLs() error {
	projs, err := project.List()
	if err != nil {
		return err
	}
	for _, p := range projs {
		stale, reason, err := intel.IsStale(p)
		state := "fresh"
		if err != nil {
			state = "error: " + err.Error()
		} else if stale {
			state = "stale: " + reason
		}
		fmt.Printf("%-20s %-10s %s\n", p.Name, state, p.Path)
	}
	return nil
}
