package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/deadcode"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

const deadCodeArtifactDir = "styx/dead-code"

// cmdDeadCode runs one repository sweep, deterministic reference verification,
// and one Codex spot-check over a bounded sample of confirmed findings.
func cmdDeadCode(ctx context.Context, a *app, prog *progress.Tracker, args []string) error {
	if prog == nil {
		prog = a.progress
	}
	if prog == nil {
		prog = progress.Quiet()
	}
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return fmt.Errorf("resolve dead-code project: %w", err)
	}
	target, targetLabel, err := resolveDeadCodeTarget(proj.Path, args)
	if err != nil {
		return err
	}

	sigs := signals.Extract("dead-code", args, proj)
	dec, err := a.router.Route(ctx, router.Request{Verb: "dead-code", Args: []string{targetLabel}, Signals: sigs})
	if err != nil {
		return fmt.Errorf("route dead-code: %w", err)
	}
	if dec.BlockedByBudget {
		return fmt.Errorf("dead-code sweep blocked by budget or circuit state; recommended target %s once available", debugDecisionLabel(dec))
	}
	sweepChannel, ok := a.channels[dec.Channel]
	if !ok {
		return fmt.Errorf("unknown dead-code sweep channel %q", dec.Channel)
	}
	if dec.Degraded {
		logStatus("dead-code sweep degraded to %s: %s", debugDecisionLabel(dec), dec.Reason)
	}

	projectID := config.ProjectID(proj.Path)
	runID := pipeline.NewRunID("dead-code-" + targetLabel)
	sweeper := newDeadCodeChannelAdapter(a, rawChannel(sweepChannel), dec.Channel, dec.Model, dec.Effort, "dead-code", projectID, runID, proj.Path)
	var reviewer *deadCodeChannelAdapter
	if codexChannel, ok := a.channels["codex"]; ok {
		reviewer = newDeadCodeChannelAdapter(a, rawChannel(codexChannel), "codex", "", "", "dead-code.review.codex", projectID, runID, proj.Path)
	}

	// Agy has no read-only mode. Keep the guard immediately around the sweep;
	// the deterministic scan and Codex request happen only after AfterSweep.
	preTree, treeErr := gitTreeState(ctx, proj.Path)
	start := time.Now()
	rep, err := deadcode.Run(ctx, deadcode.Options{
		Input: deadcode.Input{
			Target:      targetLabel,
			ProjectPath: proj.Path,
		},
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
				logStatus("⚠ dead-code sweep modified the working tree (%s) — review and revert; treating findings with suspicion", strings.Join(dirtied, ", "))
			}
			return dirtied
		},
	})
	_ = target // target is validated up front; the relative label drives the prompt.
	if err != nil {
		return fmt.Errorf("run dead-code: %w", err)
	}

	out, err := brief.WriteReport(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      deadCodeArtifactDir,
		Query:       targetLabel,
		Body:        deadcode.RenderReport(rep),
		Now:         rep.StartedAt,
	})
	if err != nil {
		return fmt.Errorf("write dead-code report: %w", err)
	}
	rel, _ := filepath.Rel(proj.Path, out)
	confirmed, refuted := deadCodeCounts(rep.Findings)
	logStatus("✓ dead-code report saved: %s", rel)
	logStatus("  Deterministic verification: confirmed=%d refuted=%d warnings=%d", confirmed, refuted, len(rep.Warnings))

	tokensIn, tokensOut := sweeper.response.EstTokensIn, sweeper.response.EstTokensOut
	if reviewer != nil {
		tokensIn += reviewer.response.EstTokensIn
		tokensOut += reviewer.response.EstTokensOut
	}
	if a.tracker != nil {
		_ = a.tracker.RecordOutcome(ctx, budget.Outcome{
			Project: projectID, CLI: deadcode.PipelineName,
			Signals: strings.Join(sigs, ","), Risk: "read",
			DurationS: time.Since(start).Seconds(), TokensIn: tokensIn, TokensOut: tokensOut,
		})
	}
	return nil
}

func resolveDeadCodeTarget(projectPath string, args []string) (string, string, error) {
	if len(args) > 1 {
		return "", "", fmt.Errorf("usage: styx dead-code [path]")
	}
	target := projectPath
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") {
			return "", "", fmt.Errorf("unknown dead-code flag %q", args[0])
		}
		target = args[0]
		if !filepath.IsAbs(target) {
			target = filepath.Join(projectPath, target)
		}
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve dead-code target %s: %w", target, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", "", fmt.Errorf("resolve dead-code target %s: %w", target, err)
	}
	projectResolved, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve dead-code project root: %w", err)
	}
	if !pathWithin(projectResolved, resolved) {
		return "", "", fmt.Errorf("dead-code target %s is outside project %s", target, projectPath)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", fmt.Errorf("stat dead-code target %s: %w", target, err)
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("dead-code target %s is not a regular file or directory", target)
	}
	rel, err := filepath.Rel(projectResolved, resolved)
	if err != nil {
		return "", "", fmt.Errorf("make dead-code target relative: %w", err)
	}
	return resolved, filepath.ToSlash(rel), nil
}

func deadCodeCounts(findings []deadcode.VerifiedFinding) (confirmed, refuted int) {
	for _, finding := range findings {
		switch finding.Status {
		case "CONFIRMED":
			confirmed++
		case "REFUTED":
			refuted++
		}
	}
	return confirmed, refuted
}

type deadCodeChannelAdapter struct {
	ch          channel.Channel
	tracker     *budget.Tracker
	channelName string
	model       string
	effort      string
	role        string
	projectID   string
	runID       string
	projectPath string
	response    channel.Response
}

func newDeadCodeChannelAdapter(a *app, ch channel.Channel, channelName, model, effort, role, projectID, runID, projectPath string) *deadCodeChannelAdapter {
	return &deadCodeChannelAdapter{
		ch: ch, tracker: a.tracker, channelName: channelName, model: model, effort: effort,
		role: role, projectID: projectID, runID: runID, projectPath: projectPath,
	}
}

func (a *deadCodeChannelAdapter) Send(ctx context.Context, prompt string) (string, error) {
	if a == nil || a.ch == nil {
		return "", errors.New("dead-code channel is unavailable")
	}
	resp, err := a.ch.Send(ctx, channel.Request{
		Model: a.model, Effort: a.effort, Prompt: prompt, WorkingDir: a.projectPath,
	})
	a.response = resp
	if a.tracker != nil {
		_ = a.tracker.Record(ctx, budget.Event{
			Channel: a.channelName, Verb: a.role, Model: a.model,
			TokensIn: resp.EstTokensIn, TokensOut: resp.EstTokensOut,
			Success: err == nil, ErrorKind: errorKindOf(err),
			Project: a.projectID, RunID: a.runID,
		})
	}
	return resp.Text, err
}
