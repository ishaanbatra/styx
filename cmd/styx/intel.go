package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/intel"
	"github.com/ishaanbatra/styx/internal/project"
)

// agyAdapter bridges the project's agy channel into the intel.AgyClient interface.
type agyAdapter struct {
	ch channel.Channel
}

func (a *agyAdapter) Send(prompt, workingDir string) (string, error) {
	resp, err := a.ch.Send(context.Background(), channel.Request{
		Model:      "default",
		Prompt:     prompt,
		WorkingDir: workingDir,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func cmdIntel(a *app, args []string) error {
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

	proj, err := resolveProjectArg(target)
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
	idx, err := intel.Build(proj, &agyAdapter{ch: ag})
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

func resolveProjectArg(arg string) (project.Project, error) {
	// First try alias.
	if p, err := project.Resolve(arg); err == nil {
		return p, nil
	}
	// Treat as path.
	abs, err := filepath.Abs(arg)
	if err != nil {
		return project.Project{}, err
	}
	// Walk up until we find a project that contains this path.
	projs, err := project.List()
	if err != nil {
		return project.Project{}, err
	}
	for _, p := range projs {
		if strings.HasPrefix(abs, p.Path) {
			return p, nil
		}
	}
	// Otherwise, try to register on-the-fly by treating arg as a git root.
	return project.CurrentFrom(abs)
}
