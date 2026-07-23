// Package claude implements the Channel interface against the local `claude`
// CLI (Claude Code). It supports one-shot (-p) and interactive modes.
package claude

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/ishaanbatra/styx/internal/channel"
)

// Channel is the Claude implementation.
type Channel struct {
	bin string // override-able for tests; "" means look up "claude" on PATH
}

// New returns a Claude channel that finds `claude` on PATH.
func New() *Channel { return &Channel{} }

// Name implements channel.Channel.
func (c *Channel) Name() string { return "claude" }

// BudgetState implements channel.Channel. Without a stable `claude --usage`
// surface, we report unknown (zero) and rely on the local sqlite tracker for
// budget enforcement.
func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

// Send implements channel.Channel.
func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return c.sendInteractive(ctx, req)
	}
	return c.sendOneShot(ctx, req)
}

func (c *Channel) sendOneShot(ctx context.Context, req channel.Request) (channel.Response, error) {
	cmd := exec.CommandContext(ctx, c.binary(), claudeArgs(req)...)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	out, err := cmd.Output()
	if err != nil {
		return channel.Response{}, classifyExecError(ctx, err)
	}
	text := strings.TrimRight(string(out), "\n")
	return channel.Response{
		Text:         text,
		EstTokensIn:  estimateTokens(req.Prompt + req.System),
		EstTokensOut: estimateTokens(text),
	}, nil
}

func claudeArgs(req channel.Request) []string {
	args := []string{"-p", req.Prompt}
	if req.Write {
		// Implement-class requests apply edits / run commands autonomously,
		// mirroring execute.Apply's built-in claude path.
		args = append([]string{"--dangerously-skip-permissions"}, args...)
	}
	if req.Model != "" {
		args = append([]string{"--model", req.Model}, args...)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	for _, root := range req.ExtraRoots {
		if root != "" {
			args = append(args, "--add-dir", root)
		}
	}
	return args
}

func (c *Channel) sendInteractive(ctx context.Context, req channel.Request) (channel.Response, error) {
	args := []string{}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	cmd := exec.CommandContext(ctx, c.binary(), args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	if err := cmd.Run(); err != nil {
		return channel.Response{}, classifyExecError(ctx, err)
	}
	return channel.Response{}, nil
}

func (c *Channel) binary() string {
	if c.bin != "" {
		return c.bin
	}
	return "claude"
}

func estimateTokens(s string) int { return len(s) / 4 }

func classifyExecError(ctx context.Context, err error) error {
	return channel.ClassifyExecError(ctx, err, "claude")
}
