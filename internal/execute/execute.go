// Package execute handles non-interactive Claude-driven code application,
// test running, fix-loops, and shipping (commit/push/PR).
package execute

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/progress"
)

// Options configure an Apply call.
type Options struct {
	PlanContent string          // markdown plan text
	ProjectPath string          // working dir for the implementer
	Model       string          // optional model id; empty = channel default
	Channel     channel.Channel // implementer; nil = built-in claude CLI (streams live stderr)
}

// Apply invokes claude --dangerously-skip-permissions -p with a structured
// "implement this plan" prompt. Returns Claude's stdout text.
// prog narrates the operation; pass nil (or progress.Quiet()) to suppress output.
func Apply(ctx context.Context, o Options, prog *progress.Tracker) (string, error) {
	if prog == nil {
		prog = progress.Quiet()
	}
	if o.PlanContent == "" {
		return "", fmt.Errorf("PlanContent is empty")
	}
	prompt := buildPrompt(o.PlanContent)
	// When a channel is injected (e.g. the router picked codex for the
	// `implement` verb), route the apply through it. nil keeps the built-in
	// claude path below, which streams claude's stderr live.
	if o.Channel != nil {
		return applyViaChannel(ctx, o, prompt, prog)
	}
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
	// Stream stderr live so the user sees progress during long claude runs.
	// Buffer stderr too so we can include it in the error message on non-zero exit.
	var stdout, stderrBuf bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderrBuf)

	s := prog.Stage("Applying plan via claude")
	s.Info("plan size: %d bytes", len(o.PlanContent))
	// Pause the spinner: claude streams its own stderr below; don't animate over it.
	s.Pause()
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errAs(err, &ee) {
			s.Fail(fmt.Errorf("claude exited %d: %s", ee.ExitCode(), strings.TrimSpace(stderrBuf.String())))
			return "", fmt.Errorf("claude exited %d: %s", ee.ExitCode(), strings.TrimSpace(stderrBuf.String()))
		}
		s.Fail(err)
		return "", err
	}
	s.Done("done")
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// applyViaChannel runs the plan through an injected channel (e.g. codex) with
// Write enabled so it can edit files and run commands autonomously. Unlike the
// built-in claude path it captures output rather than streaming it live.
func applyViaChannel(ctx context.Context, o Options, prompt string, prog *progress.Tracker) (string, error) {
	s := prog.Stage(fmt.Sprintf("Applying plan via %s", o.Channel.Name()))
	s.Info("plan size: %d bytes", len(o.PlanContent))
	resp, err := o.Channel.Send(ctx, channel.Request{
		Model:      o.Model,
		Prompt:     prompt,
		WorkingDir: o.ProjectPath,
		Write:      true,
	})
	if err != nil {
		s.Fail(err)
		return "", err
	}
	s.Done("done")
	return strings.TrimRight(resp.Text, "\n"), nil
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
