// Package execute handles non-interactive Claude-driven code application,
// test running, fix-loops, and shipping (commit/push/PR).
package execute

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Options configure an Apply call.
type Options struct {
	PlanContent string // markdown plan text
	ProjectPath string // working dir for claude
	Model       string // optional claude model id; empty = default
}

// Apply invokes claude --dangerously-skip-permissions -p with a structured
// "implement this plan" prompt. Returns Claude's stdout text.
func Apply(ctx context.Context, o Options) (string, error) {
	if o.PlanContent == "" {
		return "", fmt.Errorf("PlanContent is empty")
	}
	prompt := buildPrompt(o.PlanContent)
	args := []string{"--dangerously-skip-permissions", "-p", prompt}
	if o.Model != "" {
		args = append([]string{"--model", o.Model}, args...)
	}
	cmd := exec.CommandContext(ctx, "claude", args...)
	if o.ProjectPath != "" {
		if _, statErr := os.Stat(o.ProjectPath); statErr == nil {
			cmd.Dir = o.ProjectPath
		}
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errAs(err, &ee) {
			return "", fmt.Errorf("claude exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func buildPrompt(plan string) string {
	return "Please implement this plan autonomously. Your project context is in .claude/context.md. " +
		"Make all required code edits. Run any commands needed. Commit your work as you go using small, " +
		"descriptive commits. When done, report what you did.\n\n--- PLAN ---\n" + plan
}

// errAs is errors.As without importing errors (kept inline so this file is self-contained).
func errAs(err error, target **exec.ExitError) bool {
	for err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			*target = e
			return true
		}
		type wrapper interface{ Unwrap() error }
		w, ok := err.(wrapper)
		if !ok {
			return false
		}
		err = w.Unwrap()
	}
	return false
}
