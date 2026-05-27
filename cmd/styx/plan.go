package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func cmdPlan(a *app, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx plan <description>")
	}
	desc := strings.Join(args, " ")
	proj, err := project.Current()
	if err != nil {
		return err
	}
	subDirResearch := proj.ResearchDir
	if subDirResearch == "" {
		subDirResearch = "styx/research"
	}
	briefPath, err := brief.LoadLatest(filepath.Join(proj.Path, subDirResearch))
	if err != nil {
		return err
	}
	var briefBody string
	if briefPath != "" {
		b, err := os.ReadFile(briefPath)
		if err != nil {
			return err
		}
		briefBody = string(b)
	}

	prompt := fmt.Sprintf(`Read the research brief below, then create a detailed implementation plan for: %s

The plan MUST include:
1. Files to modify (explicit paths, with reason for each)
2. Data models (schemas, types, API shapes)
3. Edge cases and failure modes (what can go wrong, how each is handled)
4. Testing strategy (unit, integration, what's mocked vs real)

If the brief is empty, proceed with the description alone but note that assumption explicitly.

---
RESEARCH BRIEF:
%s
---
`, desc, briefBody)

	sigs := signals.Extract("plan", args, proj)
	resp, picked, err := sendWithFallback(a, context.Background(),
		router.Request{Verb: "plan", Args: args, Signals: sigs},
		channel.Request{Prompt: prompt, WorkingDir: proj.Path})
	if err != nil {
		return err
	}

	subDirPlans := proj.PlansDir
	if subDirPlans == "" {
		subDirPlans = "styx/plans"
	}
	out, err := brief.WritePlan(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      subDirPlans,
		Query:       desc,
		Body:        resp.Text,
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(proj.Path, out)
	fmt.Printf("✓ Plan saved: %s\n", rel)
	fmt.Fprintf(os.Stderr, "[styx] channel=%s:%s\n", picked.Channel, picked.Model)
	return nil
}
