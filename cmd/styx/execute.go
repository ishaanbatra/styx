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
	out, err := execute.Apply(context.Background(), execute.Options{
		PlanContent: string(b),
		ProjectPath: proj.Path,
	}, newProgress())
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}
