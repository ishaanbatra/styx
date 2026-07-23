package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ishaanbatra/styx/internal/execute"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/prdraft"
	"github.com/ishaanbatra/styx/internal/project"
)

type shipCLIArgs struct {
	NoPR       bool
	NoPush     bool
	BaseBranch string
	Goal       string
}

func parseShipArgs(args []string) (shipCLIArgs, error) {
	var parsed shipCLIArgs
	goal := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--no-pr":
			parsed.NoPR = true
		case "--no-push":
			parsed.NoPush = true
		case "--base":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "--") {
				return shipCLIArgs{}, fmt.Errorf("--base requires a branch")
			}
			i++
			parsed.BaseBranch = args[i]
		default:
			if strings.HasPrefix(arg, "--base=") {
				return shipCLIArgs{}, fmt.Errorf("--base requires a separate branch value: use --base <branch>")
			}
			goal = append(goal, arg)
		}
	}
	parsed.Goal = strings.Join(goal, " ")
	return parsed, nil
}

// cmdShip publishes the current branch's already-committed work. Unlike auto,
// it does not create or modify commits and has no resumable pipeline state.
func cmdShip(ctx context.Context, a *app, args []string) error {
	parsed, err := parseShipArgs(args)
	if err != nil {
		return fmt.Errorf("parse ship arguments: %w", err)
	}
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return fmt.Errorf("resolve ship project: %w", err)
	}
	branch, err := shipPreflight(ctx, proj, parsed.BaseBranch)
	if err != nil {
		return err
	}

	opts := execute.ShipOptions{
		ProjectPath: proj.Path,
		Branch:      branch,
		BaseBranch:  parsed.BaseBranch,
		NoPR:        parsed.NoPR,
		NoPush:      parsed.NoPush,
		Goal:        parsed.Goal,
	}
	// Match auto's publication semantics: either skip flag keeps this path
	// completely model-free, including deterministic drafting setup.
	if !parsed.NoPR && !parsed.NoPush {
		state := &pipeline.State{Goal: parsed.Goal, Branch: branch}
		draft := draftPullRequestWithBase(ctx, a, proj, state, parsed.BaseBranch)
		opts.PRTitle, opts.PRBody = draft.Title, draft.Body
		opts.Draft, opts.Labels = draft.Draft, draft.Labels
	}

	logStatus("shipping committed work from %s", branch)
	res, err := execute.Ship(ctx, opts)
	if err != nil {
		return fmt.Errorf("ship branch %s: %w", branch, err)
	}
	for _, metadataErr := range res.MetadataErrors {
		logStatus("PR metadata warning: %s", metadataErr)
	}
	switch {
	case res.PRURL != "":
		fmt.Fprintln(os.Stdout, res.PRURL)
	case res.Pushed:
		fmt.Fprintf(os.Stdout, "Pushed %s; PR creation skipped.\n", branch)
	default:
		fmt.Fprintf(os.Stdout, "Committed work on %s is ready; push skipped.\n", branch)
	}
	return nil
}

func shipPreflight(ctx context.Context, proj project.Project, requestedBase string) (string, error) {
	branch, err := shipGitOutput(ctx, proj.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve current git branch: %w", err)
	}
	branch = strings.TrimSpace(branch)
	base := prdraft.DefaultBranch(ctx, proj.Path)
	if requestedBase != "" {
		if _, err := shipGitOutput(ctx, proj.Path, "rev-parse", "--verify", requestedBase); err != nil {
			return "", fmt.Errorf("resolve base branch %q: %w", requestedBase, err)
		}
		base = requestedBase
	}
	if branch == base {
		if requestedBase == "" {
			return "", fmt.Errorf("refusing to ship the default branch %q; create a feature branch first", base)
		}
		return "", fmt.Errorf("refusing to ship the base branch %q; create a branch with commits to publish first", base)
	}

	status, err := shipGitOutput(ctx, proj.Path, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return "", fmt.Errorf("check worktree status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return "", fmt.Errorf("worktree is dirty; commit first; styx ship publishes committed work only")
	}

	aheadText, err := shipGitOutput(ctx, proj.Path, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return "", fmt.Errorf("count commits ahead of %s: %w", base, err)
	}
	ahead, err := strconv.Atoi(strings.TrimSpace(aheadText))
	if err != nil {
		return "", fmt.Errorf("parse commits ahead of %s from %q: %w", base, strings.TrimSpace(aheadText), err)
	}
	if ahead == 0 {
		return "", fmt.Errorf("branch %q has no commits ahead of base branch %q; nothing to publish", branch, base)
	}
	return branch, nil
}

func shipGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
