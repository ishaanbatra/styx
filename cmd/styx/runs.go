package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/project"
)

func cmdRuns(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx runs ls | styx runs show <run-id> | styx runs unlock")
	}
	switch args[0] {
	case "ls":
		return cmdRunsLs()
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: styx runs show <run-id>")
		}
		return cmdRunsShow(args[1])
	case "unlock":
		return cmdRunsUnlock()
	}
	return fmt.Errorf("unknown runs subcommand %q", args[0])
}

func cmdRunsUnlock() error {
	proj, err := project.Current()
	if err != nil {
		return err
	}
	holder, _ := pipeline.ReadLockHolder(proj.Path)
	if holder == "" {
		fmt.Println("(no lock held)")
		return nil
	}
	if err := pipeline.ReleaseLock(proj.Path); err != nil {
		return err
	}
	fmt.Printf("Released lock previously held by %s\n", holder)
	return nil
}

func cmdRunsLs() error {
	proj, err := project.Current()
	if err != nil {
		return err
	}
	runsDir := filepath.Join(proj.Path, ".styx", "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("(no runs)")
			return nil
		}
		return err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		s, err := pipeline.LoadState(filepath.Join(runsDir, id))
		if err != nil {
			fmt.Printf("%-40s ERROR %v\n", id, err)
			continue
		}
		fmt.Printf("%-40s %-10s stage %d/%d\n", id, s.Status, s.CurrentStage, len(s.Stages))
	}
	return nil
}

func cmdRunsShow(runID string) error {
	proj, err := project.Current()
	if err != nil {
		return err
	}
	dir := pipeline.RunDir(proj.Path, runID)
	s, err := pipeline.LoadState(dir)
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	fmt.Println(string(b))
	return nil
}
