package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/crossrepo"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

const crossRepoArtifactDir = "styx/cross-repo"

type crossRepoInput struct {
	Roots    []string
	Question string
}

// cmdCrossRepo analyzes producer/consumer links across the active repository
// and one or more explicitly named repository roots.
func cmdCrossRepo(ctx context.Context, a *app, prog *progress.Tracker, args []string) error {
	if prog == nil {
		prog = a.progress
	}
	if prog == nil {
		prog = progress.Quiet()
	}
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return fmt.Errorf("resolve cross-repo project: %w", err)
	}
	input, err := resolveCrossRepoInput(ctx, proj.Path, args)
	if err != nil {
		return err
	}

	sigs := signals.Extract("cross-repo", args, proj)
	dec, err := a.router.Route(ctx, router.Request{Verb: "cross-repo", Args: args, Signals: sigs})
	if err != nil {
		return fmt.Errorf("route cross-repo: %w", err)
	}
	if dec.BlockedByBudget {
		return fmt.Errorf("cross-repo sweep blocked by budget or circuit state; recommended target %s once available", debugDecisionLabel(dec))
	}
	sweepChannel, ok := a.channels[dec.Channel]
	if !ok {
		return fmt.Errorf("unknown cross-repo sweep channel %q", dec.Channel)
	}
	if dec.Degraded {
		logStatus("cross-repo sweep degraded to %s: %s", debugDecisionLabel(dec), dec.Reason)
	}

	preTrees, err := snapshotCrossRepoTrees(ctx, input.Roots)
	if err != nil {
		return fmt.Errorf("snapshot every cross-repo tree before sweep: %w", err)
	}
	extraRoots := append([]string(nil), input.Roots[1:]...)
	projectID := config.ProjectID(input.Roots[0])
	runSeed := input.Question
	if runSeed == "" {
		runSeed = strings.Join(input.Roots[1:], "-")
	}
	runID := pipeline.NewRunID("cross-repo-" + runSeed)
	sweeper := newReadPathwayChannelAdapter(a, rawChannel(sweepChannel), dec.Channel, dec.Model, dec.Effort, "cross-repo", projectID, runID, input.Roots[0]).withExtraRoots(extraRoots)
	var reviewer *readPathwayChannelAdapter
	if codexChannel, ok := a.channels["codex"]; ok {
		reviewer = newReadPathwayChannelAdapter(a, rawChannel(codexChannel), "codex", "", "", "cross-repo.review.codex", projectID, runID, input.Roots[0]).withExtraRoots(extraRoots)
	}

	start := time.Now()
	rep, runErr := crossrepo.Run(ctx, crossrepo.Options{
		Roots:        input.Roots,
		Question:     input.Question,
		Sweeper:      sweeper,
		Codex:        reviewer,
		Prog:         prog,
		SweepChannel: debugDecisionLabel(dec),
		AfterSweep: func() ([]crossrepo.TreeChange, error) {
			return diffCrossRepoTrees(ctx, input.Roots, preTrees)
		},
	})
	if rep == nil {
		if runErr != nil {
			return fmt.Errorf("run cross-repo: %w", runErr)
		}
		return errors.New("run cross-repo: missing report")
	}

	query := input.Question
	if query == "" {
		query = "relationships-" + strings.Join(input.Roots[1:], "-")
	}
	out, writeErr := brief.WriteReport(brief.WriteOpts{
		ProjectPath: input.Roots[0],
		SubDir:      crossRepoArtifactDir,
		Query:       query,
		Body:        crossrepo.RenderReport(rep),
		Now:         rep.StartedAt,
	})
	if writeErr != nil {
		if runErr != nil {
			return fmt.Errorf("cross-repo safety failure (%v), then write forensic report: %w", runErr, writeErr)
		}
		return fmt.Errorf("write cross-repo report: %w", writeErr)
	}
	rel, _ := filepath.Rel(input.Roots[0], out)

	tokensIn, tokensOut := sweeper.response.EstTokensIn, sweeper.response.EstTokensOut
	if reviewer != nil {
		tokensIn += reviewer.response.EstTokensIn
		tokensOut += reviewer.response.EstTokensOut
	}
	if a.tracker != nil {
		_ = a.tracker.RecordOutcome(ctx, budget.Outcome{
			Project: projectID, CLI: crossrepo.PipelineName,
			Signals: strings.Join(sigs, ","), Risk: "read",
			DurationS: time.Since(start).Seconds(), TokensIn: tokensIn, TokensOut: tokensOut,
		})
	}
	if runErr != nil {
		logStatus("⚠⚠⚠ CROSS-REPO SAFETY ABORT: %v", runErr)
		for _, change := range rep.TreeChanges {
			logStatus("⚠ mounted root changed: %s (%s)", change.Root, strings.Join(change.Paths, ", "))
		}
		if rep.TreeGuardError != "" {
			logStatus("⚠ mounted-root guard error: %s", rep.TreeGuardError)
		}
		logStatus("forensic cross-repo report saved without success: %s", rel)
		return fmt.Errorf("cross-repo refused success because the all-roots tree guard failed: %w", runErr)
	}

	logStatus("✓ cross-repo report saved: %s", rel)
	logStatus("  Cross-repo links: valid=%d warnings=%d reviewed=%d", len(rep.Findings), len(rep.Warnings), rep.ReviewedCount)
	return nil
}

func resolveCrossRepoInput(ctx context.Context, primary string, args []string) (crossRepoInput, error) {
	separator := -1
	for i, arg := range args {
		if arg == "--" {
			if separator >= 0 {
				return crossRepoInput{}, errors.New("cross-repo accepts only one -- question separator")
			}
			separator = i
		}
	}
	rootArgs := args
	questionArgs := []string(nil)
	if separator >= 0 {
		rootArgs = args[:separator]
		questionArgs = args[separator+1:]
	}
	if len(rootArgs) == 0 {
		return crossRepoInput{}, errors.New("usage: styx cross-repo <root2> [root3...] [-- <question>]")
	}
	for _, root := range rootArgs {
		if strings.TrimSpace(root) == "" || strings.HasPrefix(root, "-") {
			return crossRepoInput{}, fmt.Errorf("invalid cross-repo root %q", root)
		}
	}

	allArgs := append([]string{primary}, rootArgs...)
	roots := make([]string, 0, len(allArgs))
	seen := make(map[string]struct{}, len(allArgs))
	for i, rootArg := range allArgs {
		root, err := resolveGitRepositoryRoot(ctx, rootArg)
		if err != nil {
			label := "primary root"
			if i > 0 {
				label = fmt.Sprintf("extra root %q", rootArg)
			}
			return crossRepoInput{}, fmt.Errorf("validate %s: %w", label, err)
		}
		if _, ok := seen[root]; ok {
			return crossRepoInput{}, fmt.Errorf("cross-repo root %s is duplicated or resolves to the primary repository", rootArg)
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	return crossRepoInput{Roots: roots, Question: strings.TrimSpace(strings.Join(questionArgs, " "))}, nil
}

func resolveGitRepositoryRoot(ctx context.Context, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("make path absolute: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat path: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	if reason := sensitiveMountReason(resolved); reason != "" {
		return "", fmt.Errorf("refusing sensitive mount %s: %s", resolved, reason)
	}

	gitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", "rev-parse", "--show-toplevel")
	cmd.Dir = resolved
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("path is not a git repository: %w", err)
	}
	top := strings.TrimSpace(string(out))
	topResolved, err := filepath.EvalSymlinks(top)
	if err != nil {
		return "", fmt.Errorf("resolve git repository root: %w", err)
	}
	if filepath.Clean(topResolved) != filepath.Clean(resolved) {
		return "", fmt.Errorf("path must name the git repository root exactly (root is %s)", topResolved)
	}
	return filepath.Clean(resolved), nil
}

func sensitiveMountReason(root string) string {
	clean := filepath.Clean(root)
	slashPath := strings.ToLower(filepath.ToSlash(clean))
	parts := strings.Split(slashPath, "/")
	credentialParts := map[string]string{
		".aws":            "AWS credential directories are never mounted",
		".azure":          "Azure credential directories are never mounted",
		".docker":         "Docker credential directories are never mounted",
		".gnupg":          "GnuPG credential directories are never mounted",
		".kube":           "Kubernetes credential directories are never mounted",
		".password-store": "password-store directories are never mounted",
		".ssh":            "SSH credential directories are never mounted",
	}
	for _, part := range parts {
		if part == ".git" {
			return "git metadata directories are never mounted"
		}
		if reason := credentialParts[part]; reason != "" {
			return reason
		}
	}
	for _, suffix := range []string{
		"/.config/gcloud", "/.config/gh", "/.config/glab", "/.config/styx",
		"/.local/share/keyrings", "/library/keychains",
	} {
		if strings.Contains(slashPath+"/", suffix+"/") {
			return fmt.Sprintf("credential location %s is never mounted", strings.TrimPrefix(suffix, "/"))
		}
	}
	rawHome, err := os.UserHomeDir()
	if err != nil || rawHome == "" {
		return ""
	}
	rawHome = filepath.Clean(rawHome)
	home, err := filepath.EvalSymlinks(rawHome)
	if err != nil {
		home = rawHome
	}
	// On macOS /var resolves to /private/var. Normalize a root below HOME with
	// the same prefix substitution even when its final components do not exist.
	if rel, relErr := filepath.Rel(rawHome, clean); relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		clean = filepath.Join(home, rel)
	}
	if clean == home {
		return "the home directory may expose credentials outside the named repositories"
	}
	return ""
}

func snapshotCrossRepoTrees(ctx context.Context, roots []string) (map[string]string, error) {
	states := make(map[string]string, len(roots))
	for _, root := range roots {
		state, err := gitTreeState(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s: %w", root, err)
		}
		states[root] = state
	}
	return states, nil
}

func diffCrossRepoTrees(ctx context.Context, roots []string, before map[string]string) ([]crossrepo.TreeChange, error) {
	changes := make([]crossrepo.TreeChange, 0)
	for _, root := range roots {
		pre, ok := before[root]
		if !ok {
			return nil, fmt.Errorf("missing pre-sweep snapshot for %s", root)
		}
		post, err := gitTreeState(ctx, root)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s after sweep: %w", root, err)
		}
		if dirtied := treeStateDiff(pre, post); len(dirtied) > 0 {
			changes = append(changes, crossrepo.TreeChange{Root: root, Paths: dirtied})
		}
	}
	return changes, nil
}
