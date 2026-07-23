package execute

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ishaanbatra/styx/internal/attribution"
)

// ShipOptions configure a Ship call.
type ShipOptions struct {
	ProjectPath string
	Branch      string
	BaseBranch  string
	NoPR        bool
	NoPush      bool
	PRTitle     string
	PRBody      string // optional; defaults to a simple template if empty
	Draft       bool
	Labels      []string
	Goal        string // used in default PR body
}

// ShipResult reports what happened.
type ShipResult struct {
	Pushed         bool
	PRURL          string
	MetadataErrors []string
}

// Ship pushes the branch and (unless --no-pr) opens a PR via gh.
func Ship(ctx context.Context, o ShipOptions) (ShipResult, error) {
	res := ShipResult{}
	if o.NoPush {
		return res, nil
	}
	pushCmd := exec.CommandContext(ctx, "git", "push", "-u", "origin", o.Branch)
	pushCmd.Dir = o.ProjectPath
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return res, fmt.Errorf("git push: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	res.Pushed = true
	if o.NoPR {
		return res, nil
	}
	title := strings.TrimSpace(o.PRTitle)
	if title == "" {
		title = strings.TrimSpace(o.Goal)
	}
	if title == "" {
		title = "Update project"
	}
	args := prCreateArgs(o, title)
	prCmd := exec.CommandContext(ctx, "gh", args...)
	prCmd.Dir = o.ProjectPath
	out, err := prCmd.CombinedOutput()
	if err != nil {
		// A resumed ship may find that the PR was created by the prior attempt.
		// Recover that URL before preserving the existing graceful gh failure.
		viewCmd := exec.CommandContext(ctx, "gh", "pr", "view", o.Branch, "--json", "url", "--jq", ".url")
		viewCmd.Dir = o.ProjectPath
		viewOut, viewErr := viewCmd.CombinedOutput()
		if viewErr != nil {
			return res, nil
		}
		res.PRURL = extractPRURL(string(viewOut))
	} else {
		res.PRURL = extractPRURL(string(out))
	}
	if res.PRURL == "" || len(o.Labels) == 0 {
		return res, nil
	}
	labelArgs := []string{"pr", "edit", res.PRURL}
	for _, label := range o.Labels {
		if label = strings.TrimSpace(label); label != "" {
			labelArgs = append(labelArgs, "--add-label", label)
		}
	}
	if len(labelArgs) == 3 {
		return res, nil
	}
	labelCmd := exec.CommandContext(ctx, "gh", labelArgs...)
	labelCmd.Dir = o.ProjectPath
	if labelOut, labelErr := labelCmd.CombinedOutput(); labelErr != nil {
		res.MetadataErrors = append(res.MetadataErrors,
			fmt.Sprintf("apply PR labels: %v (%s)", labelErr, strings.TrimSpace(string(labelOut))))
	}
	return res, nil
}

func prCreateArgs(o ShipOptions, title string) []string {
	args := []string{"pr", "create", "--title", title, "--body", prBody(o)}
	if base := strings.TrimSpace(o.BaseBranch); base != "" {
		args = append(args, "--base", base)
	}
	if o.Draft {
		args = append(args, "--draft")
	}
	return args
}

// prBody builds the PR body: the caller's PRBody (or a goal-line default)
// with the styx attribution footer as its own final paragraph.
func prBody(o ShipOptions) string {
	body := o.PRBody
	if body == "" {
		body = "Goal: " + o.Goal
	}
	return strings.TrimRight(body, "\n") + "\n\n" + attribution.PRFooter
}

func extractPRURL(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") {
			return line
		}
	}
	return ""
}
