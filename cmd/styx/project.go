package main

import (
	"fmt"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/project"
)

func cmdProject(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx project ls|add|rm|rename")
	}
	switch args[0] {
	case "ls":
		projs, err := project.List()
		if err != nil {
			return err
		}
		for _, p := range projs {
			fmt.Printf("%-20s %s\n", p.Name, p.Path)
		}
		return nil
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: styx project add <name> <path>")
		}
		abs, err := filepath.Abs(args[2])
		if err != nil {
			return fmt.Errorf("resolve path %q: %w", args[2], err)
		}
		return project.Register(config.Project{
			ID:       config.ProjectID(abs),
			Name:     args[1],
			Path:     abs,
			Language: project.SniffLanguage(abs),
		})
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: styx project rm <name>")
		}
		return project.Forget(args[1])
	case "rename":
		if len(args) < 3 {
			return fmt.Errorf("usage: styx project rename <old> <new>")
		}
		p, err := project.Resolve(args[1])
		if err != nil {
			return err
		}
		if err := project.Forget(args[1]); err != nil {
			return err
		}
		p.Name = args[2]
		return project.Register(p)
	}
	return fmt.Errorf("unknown project subcommand %q", args[0])
}
