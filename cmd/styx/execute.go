package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ishaanbatra/styx/internal/execute"
	"github.com/ishaanbatra/styx/internal/project"
)

func cmdExecuteVerb(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx execute <plan-file>")
	}
	planFile := args[0]
	b, err := os.ReadFile(planFile)
	if err != nil {
		return fmt.Errorf("read plan file: %w", err)
	}
	proj, err := project.Current()
	if err != nil {
		return err
	}
	a, err := loadApp()
	if err != nil {
		return err
	}
	defer a.tracker.Close()
	// Route through the implement verb so codex applies well-scoped plans and
	// claude handles complex ones; signals are derived from the plan content.
	opts := implementOptions(a, string(b), string(b), proj.Path)
	out, err := execute.Apply(context.Background(), opts, newProgress())
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
