package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/project"
)

var scanPrune = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	".git":         true,
	".venv":        true,
	"venv":         true,
	"dist":         true,
	"build":        true,
}

// cmdProjectScan walks down from root (default ~) up to --depth levels, finds
// git roots, prunes vendored/build dirs, does not descend into a repo once
// found, and registers any new ones via project.CurrentFrom.
func cmdProjectScan(args []string) error {
	root := ""
	depth := 4
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--depth":
			if i+1 >= len(args) {
				return fmt.Errorf("usage: styx project scan [root] [--depth N]")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("parse --depth %q: %w", args[i+1], err)
			}
			depth = n
			i++
		case strings.HasPrefix(args[i], "--depth="):
			raw := strings.TrimPrefix(args[i], "--depth=")
			n, err := strconv.Atoi(raw)
			if err != nil {
				return fmt.Errorf("parse --depth %q: %w", raw, err)
			}
			depth = n
		default:
			if root != "" {
				return fmt.Errorf("usage: styx project scan [root] [--depth N]")
			}
			root = args[i]
		}
	}
	if depth < 0 {
		return fmt.Errorf("--depth must be >= 0")
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		root = home
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve scan root %q: %w", root, err)
	}

	before, err := config.LoadProjects()
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}
	known := map[string]bool{}
	for _, p := range before {
		known[p.Path] = true
	}

	found := 0
	err = scanWalk(abs, abs, depth, func(repo string) error {
		if known[repo] {
			return nil
		}
		p, err := project.CurrentFrom(repo)
		if err != nil {
			return fmt.Errorf("register %s: %w", repo, err)
		}
		known[repo] = true
		found++
		logStatus("registered %s (%s) at %s", p.Name, p.Language, p.Path)
		return nil
	})
	if err != nil {
		return err
	}
	logStatus("scan complete: %d new project(s) registered", found)
	return nil
}

// scanWalk descends from dir, invoking onRepo for each git root and not
// descending into a repo once found. It is bounded by maxDepth levels below base.
func scanWalk(base, dir string, maxDepth int, onRepo func(repo string) error) error {
	if scanDepth(base, dir) > maxDepth {
		return nil
	}
	gitPath := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitPath); err == nil {
		return onRepo(dir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", gitPath, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() || scanPrune[e.Name()] {
			continue
		}
		if err := scanWalk(base, filepath.Join(dir, e.Name()), maxDepth, onRepo); err != nil {
			return err
		}
	}
	return nil
}

func scanDepth(base, dir string) int {
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}
