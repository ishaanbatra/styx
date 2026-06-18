// Package codex implements the Channel interface against the local `codex`
// CLI (OpenAI Codex, signed-in via ChatGPT account).
package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

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
		return channel.Response{}, classifyExecError(err)
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
		return channel.Response{}, classifyExecError(err)
	}
	return channel.Response{}, nil
}

func estimateTokens(s string) int { return len(s) / 4 }

func classifyExecError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("codex CLI not found on PATH: %w", err)}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok {
			if status.Signal() == syscall.SIGKILL || status.Signal() == syscall.SIGTERM {
				return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
			}
		}
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("codex exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
	}
	return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
}
