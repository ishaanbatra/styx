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
	NoPR        bool
	NoPush      bool
	PRBody      string // optional; defaults to a simple template if empty
	Goal        string // used in default PR body
}

// ShipResult reports what happened.
type ShipResult struct {
	Pushed bool
	PRURL  string
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
	prCmd := exec.CommandContext(ctx, "gh", "pr", "create", "--fill", "--body", prBody(o))
	prCmd.Dir = o.ProjectPath
	out, err := prCmd.CombinedOutput()
	if err != nil {
		// gh missing or unauthenticated -> degrade gracefully.
		return res, nil
	}
	res.PRURL = extractPRURL(string(out))
	return res, nil
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
