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
	"github.com/ishaanbatra/styx/internal/intel"
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

	// Ensure intel index fresh.
	if stale, reason, err := intel.IsStale(proj); err != nil {
		return fmt.Errorf("check intel: %w", err)
	} else if stale {
		fmt.Fprintf(os.Stderr, "[styx] intel index stale (%s) — rebuilding...\n", reason)
		ag, ok := a.channels["agy"]
		if !ok {
			return fmt.Errorf("agy channel not registered, cannot build intel")
		}
		if _, err := intel.Build(proj, &agyAdapter{ch: ag}); err != nil {
			return fmt.Errorf("rebuild intel: %w", err)
		}
	}

	idx, err := intel.Load(proj)
	if err != nil {
		return fmt.Errorf("load intel: %w", err)
	}
	contextMD := intel.ToMarkdown(idx)

	// Materialize to .claude/context.md (or context.styx.md if user-authored exists).
	written, err := intel.WriteContextMD(proj.Path, contextMD)
	if err != nil {
		return fmt.Errorf("write context.md: %w", err)
	}
	relWritten, _ := filepath.Rel(proj.Path, written)
	fmt.Fprintf(os.Stderr, "[styx] context written to %s\n", relWritten)

	// Load latest brief if any.
	subDirResearch := proj.ResearchDir
	if subDirResearch == "" {
		subDirResearch = "styx/research"
	}
	briefPath, _ := brief.LoadLatest(filepath.Join(proj.Path, subDirResearch))
	briefBody := ""
	if briefPath != "" {
		if b, err := os.ReadFile(briefPath); err == nil {
			briefBody = string(b)
		}
	}

	// Build the structured plan prompt with intel + brief inlined.
	prompt := fmt.Sprintf(`Create a detailed implementation plan for: %s

You have full project context already loaded from .claude/context.md. Build on that.

The plan MUST include:
1. Files to create/modify (explicit paths, with reason for each)
2. Data models (schemas, types, API shapes)
3. Edge cases and failure modes
4. Testing strategy (unit, integration)
5. Exact code where useful

Use the research brief below if relevant. If the brief is empty, note that assumption.

--- PROJECT CONTEXT (mirror of .claude/context.md) ---
%s

--- RESEARCH BRIEF ---
%s
`, desc, contextMD, briefBody)

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

// Note: agyAdapter is defined in cmd/styx/intel.go and reused here.
