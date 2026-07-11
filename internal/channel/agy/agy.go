// Package agy implements the Channel interface against Google's Antigravity CLI
// (binary name: agy). Replaces the v0.1 gemini-cli/HTTP fallback approach
// because Google is sunsetting gemini-cli on 2026-06-18.
//
// Authentication: agy handles its own OAuth via the user's $20/mo Google AI
// Pro subscription. Styx does not pass an API key.
package agy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ishaanbatra/styx/internal/channel"
)

// osStat is a package-level alias so tests can stub it if needed.
var osStat = os.Stat

// Channel is the agy implementation.
type Channel struct{}

// New returns an agy channel.
func New() *Channel { return &Channel{} }

// Name implements channel.Channel.
func (c *Channel) Name() string { return "agy" }

// BudgetState implements channel.Channel. agy does not expose a programmatic
// usage endpoint, so styx relies on its local sqlite tracker for budget enforcement.
func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

// Send implements channel.Channel.
// Always passes --dangerously-skip-permissions because styx invocations are
// always headless (the user opted into autonomous behavior by running styx).
func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return channel.Response{}, errors.New("agy channel does not support interactive mode (use claude for interactive)")
	}
	args := []string{
		"-p", req.Prompt,
		"--dangerously-skip-permissions",
	}
	if req.WorkingDir != "" {
		args = append(args, "--add-dir", req.WorkingDir)
	}
	for _, root := range req.ExtraRoots {
		if root != "" {
			args = append(args, "--add-dir", root)
		}
	}
	cmd := exec.CommandContext(ctx, "agy", args...)
	if req.WorkingDir != "" {
		if _, statErr := osStat(req.WorkingDir); statErr == nil {
			cmd.Dir = req.WorkingDir
		}
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

func estimateTokens(s string) int { return len(s) / 4 }

func classifyExecError(ctx context.Context, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("agy CLI not found on PATH (install: curl -fsSL https://antigravity.google/cli/install.sh | bash): %w", err)}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if channel.KilledBySignal(ee) {
			return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
		}
		if ctx.Err() != nil {
			// No kill signals on Windows: a dead context is the timeout
			// signature there (exec.CommandContext killed the child).
			return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: fmt.Errorf("%w (context: %v)", err, ctx.Err())}
		}
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("agy exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
	}
	return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
}
