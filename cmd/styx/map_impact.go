package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/mapimpact"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

const mapImpactArtifactDir = "styx/map-impact"

// cmdMapImpact runs one repository impact sweep and one bounded Codex
// spot-check over the dependency edges claimed by agy.
func cmdMapImpact(ctx context.Context, a *app, prog *progress.Tracker, args []string) error {
	if prog == nil {
		prog = a.progress
	}
	if prog == nil {
		prog = progress.Quiet()
	}
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return fmt.Errorf("resolve map-impact project: %w", err)
	}
	input, err := resolveMapImpactInput(ctx, proj.Path, args)
	if err != nil {
		return err
	}

	sigs := signals.Extract("map-impact", args, proj)
	dec, err := a.router.Route(ctx, router.Request{Verb: "map-impact", Args: []string{input.Value}, Signals: sigs})
	if err != nil {
		return fmt.Errorf("route map-impact: %w", err)
	}
	if dec.BlockedByBudget {
		return fmt.Errorf("map-impact sweep blocked by budget or circuit state; recommended target %s once available", debugDecisionLabel(dec))
	}
	sweepChannel, ok := a.channels[dec.Channel]
	if !ok {
		return fmt.Errorf("unknown map-impact sweep channel %q", dec.Channel)
	}
	if dec.Degraded {
		logStatus("map-impact sweep degraded to %s: %s", debugDecisionLabel(dec), dec.Reason)
	}

	projectID := config.ProjectID(proj.Path)
	runID := pipeline.NewRunID("map-impact-" + input.Value)
	sweeper := newReadPathwayChannelAdapter(a, rawChannel(sweepChannel), dec.Channel, dec.Model, dec.Effort, "map-impact", projectID, runID, proj.Path)
	var reviewer *readPathwayChannelAdapter
	if codexChannel, ok := a.channels["codex"]; ok {
		reviewer = newReadPathwayChannelAdapter(a, rawChannel(codexChannel), "codex", "", "", "map-impact.review.codex", projectID, runID, proj.Path)
	}

	// Agy has no read-only mode. Keep the guard immediately around its sweep;
	// Codex runs only after AfterSweep has compared the working tree.
	preTree, treeErr := gitTreeState(ctx, proj.Path)
	start := time.Now()
	rep, err := mapimpact.Run(ctx, mapimpact.Options{
		Input:        input,
		Sweeper:      sweeper,
		Codex:        reviewer,
		Prog:         prog,
		SweepChannel: debugDecisionLabel(dec),
		AfterSweep: func() []string {
			if treeErr != nil {
				return nil
			}
			postTree, err := gitTreeState(ctx, proj.Path)
			if err != nil {
				return nil
			}
			dirtied := treeStateDiff(preTree, postTree)
			if len(dirtied) > 0 {
				logStatus("⚠ map-impact sweep modified the working tree (%s) — review and revert; treating findings with suspicion", strings.Join(dirtied, ", "))
			}
			return dirtied
		},
	})
	if err != nil {
		return fmt.Errorf("run map-impact: %w", err)
	}

	out, err := brief.WriteReport(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      mapImpactArtifactDir,
		Query:       input.Value,
		Body:        mapimpact.RenderReport(rep),
		Now:         rep.StartedAt,
	})
	if err != nil {
		return fmt.Errorf("write map-impact report: %w", err)
	}
	rel, _ := filepath.Rel(proj.Path, out)
	logStatus("✓ map-impact report saved: %s", rel)
	logStatus("  Dependency edges: valid=%d warnings=%d reviewed=%d", len(rep.Findings), len(rep.Warnings), rep.ReviewedCount)

	tokensIn, tokensOut := sweeper.response.EstTokensIn, sweeper.response.EstTokensOut
	if reviewer != nil {
		tokensIn += reviewer.response.EstTokensIn
		tokensOut += reviewer.response.EstTokensOut
	}
	if a.tracker != nil {
		_ = a.tracker.RecordOutcome(ctx, budget.Outcome{
			Project: projectID, CLI: mapimpact.PipelineName,
			Signals: strings.Join(sigs, ","), Risk: "read",
			DurationS: time.Since(start).Seconds(), TokensIn: tokensIn, TokensOut: tokensOut,
		})
	}
	return nil
}

func resolveMapImpactInput(ctx context.Context, projectPath string, args []string) (mapimpact.Input, error) {
	if len(args) != 1 {
		return mapimpact.Input{}, fmt.Errorf("usage: styx map-impact <symbol|file|diff-spec>")
	}
	value := strings.TrimSpace(args[0])
	if value == "" {
		return mapimpact.Input{}, fmt.Errorf("map-impact input must not be empty")
	}
	if strings.HasPrefix(value, "-") {
		return mapimpact.Input{}, fmt.Errorf("unknown map-impact flag %q", value)
	}

	if fileInput, ok, err := resolveMapImpactFile(projectPath, value); err != nil {
		return mapimpact.Input{}, err
	} else if ok {
		return mapimpact.Input{Kind: "file", Value: fileInput}, nil
	}
	if isGitDiffSpec(ctx, projectPath, value) {
		return mapimpact.Input{Kind: "diff", Value: value}, nil
	}
	return mapimpact.Input{Kind: "symbol", Value: value}, nil
}

func resolveMapImpactFile(projectPath, value string) (string, bool, error) {
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(projectPath, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat map-impact input %s: %w", value, err)
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("map-impact file input %s is not a regular file", value)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve map-impact file %s: %w", value, err)
	}
	projectResolved, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return "", false, fmt.Errorf("resolve map-impact project root: %w", err)
	}
	if !pathWithin(projectResolved, resolved) {
		return "", false, fmt.Errorf("map-impact file %s is outside project %s", value, projectPath)
	}
	rel, err := filepath.Rel(projectResolved, resolved)
	if err != nil {
		return "", false, fmt.Errorf("make map-impact file relative: %w", err)
	}
	return filepath.ToSlash(rel), true, nil
}

func isGitDiffSpec(ctx context.Context, projectPath, value string) bool {
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-ext-diff", "--name-only", value, "--")
	cmd.Dir = projectPath
	return cmd.Run() == nil
}
