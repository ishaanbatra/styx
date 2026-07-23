// Package codex implements the Channel interface against the local `codex`
// CLI (OpenAI Codex, signed-in via ChatGPT account).
package codex

import (
	"context"
	"os"
	"os/exec"
	"strings"

	"github.com/ishaanbatra/styx/internal/channel"
)

type Channel struct{}

func New() *Channel { return &Channel{} }

func (c *Channel) Name() string { return "codex" }

func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return c.sendInteractive(ctx, req)
	}
	return c.sendOneShot(ctx, req)
}

func (c *Channel) sendOneShot(ctx context.Context, req channel.Request) (channel.Response, error) {
	cmd := exec.CommandContext(ctx, "codex", codexArgs(req)...)
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

// codexArgs builds the exec argv (excluding the binary name) for req.
func codexArgs(req channel.Request) []string {
	args := []string{}
	if req.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+req.Effort)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, "exec")
	if req.Write {
		// Implement-class requests must apply edits and run commands autonomously.
		// workspace-write lets codex write within the repo without per-action prompts,
		// mirroring the claude `--dangerously-skip-permissions` implement path.
		args = append(args, "--sandbox", "workspace-write")
	}
	for _, root := range req.ExtraRoots {
		if root != "" {
			args = append(args, "--add-dir", root)
		}
	}
	args = append(args, req.Prompt)
	return args
}

func (c *Channel) sendInteractive(ctx context.Context, req channel.Request) (channel.Response, error) {
	args := []string{}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	cmd := exec.CommandContext(ctx, "codex", args...)
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

func estimateTokens(s string) int { return len(s) / 4 }

func classifyExecError(ctx context.Context, err error) error {
	return channel.ClassifyExecError(ctx, err, "codex")
}
