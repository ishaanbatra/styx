package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/project"
)

func cmdCheck(args []string) error {
	projs, err := project.List()
	if err != nil {
		return err
	}
	for _, p := range projs {
		fmt.Printf("── %s ──\n", p.Name)
		if _, err := os.Stat(filepath.Join(p.Path, ".git")); err == nil {
			branchCmd := exec.Command("git", "branch", "--show-current")
			branchCmd.Dir = p.Path
			branch, _ := branchCmd.Output()
			fmt.Printf("  branch: %s", branch)
			statusCmd := exec.Command("git", "status", "--short")
			statusCmd.Dir = p.Path
			st, _ := statusCmd.Output()
			s := strings.TrimSpace(string(st))
			if s == "" {
				fmt.Println("  status: clean")
			} else {
				fmt.Println("  status:")
				for _, line := range strings.Split(s, "\n") {
					fmt.Println("    " + line)
				}
			}
		} else {
			fmt.Printf("  (not a git repo: %s)\n", p.Path)
		}
		researchDir := p.ResearchDir
		if researchDir == "" {
			researchDir = "styx/research"
		}
		latest, _ := brief.LoadLatest(filepath.Join(p.Path, researchDir))
		if latest != "" {
			rel, _ := filepath.Rel(p.Path, latest)
			fmt.Printf("  latest brief: %s\n", rel)
		}
		fmt.Println()
	}
	return nil
}
