// Package mlx implements the Channel interface by running the local
// `mlx_lm.generate` CLI as a load-generate-exit subprocess.
package mlx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ishaanbatra/styx/internal/channel"
)

const (
	// DefaultModel is used when a routing target omits an MLX model.
	DefaultModel = "mlx-community/Qwen2.5-Coder-7B-Instruct-4bit"

	defaultMaxTokens = 1024
)

// Channel is the MLX subprocess implementation.
type Channel struct {
	bin    string
	stderr io.Writer
}

// New returns an MLX channel that finds `mlx_lm.generate` on PATH.
func New() *Channel { return &Channel{} }

// Name implements channel.Channel.
func (c *Channel) Name() string { return "mlx" }

// BudgetState implements channel.Channel. MLX is local-only and unlimited.
func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

// Send implements channel.Channel.
func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return channel.Response{}, unsupported("interactive mode")
	}
	if req.Write {
		return channel.Response{}, unsupported("write mode")
	}

	model := req.Model
	if model == "" {
		model = DefaultModel
	}
	args := []string{"--model", model}
	if req.System != "" {
		args = append(args, "--system-prompt", req.System)
	}
	args = append(args,
		"--prompt", "-",
		"--max-tokens", fmt.Sprint(defaultMaxTokens),
		"--verbose", "false",
	)

	cmd := exec.CommandContext(ctx, c.binary(), args...)
	cmd.Stdin = strings.NewReader(req.Prompt)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.MultiWriter(c.stderrWriter(), &stderr)
	if err := cmd.Run(); err != nil {
		// Command.Output normally populates ExitError.Stderr. Because MLX stderr
		// is streamed live for Hugging Face download progress, preserve the
		// captured copy explicitly for the shared classifier's error message.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = append([]byte(nil), stderr.Bytes()...)
		}
		return channel.Response{}, channel.ClassifyExecError(ctx, err, "mlx")
	}

	text := parseOutput(stdout.String())
	return channel.Response{
		Text:         text,
		EstTokensIn:  estimateTokens(req.Prompt + req.System),
		EstTokensOut: estimateTokens(text),
	}, nil
}

func (c *Channel) binary() string {
	if c.bin != "" {
		return c.bin
	}
	return "mlx_lm.generate"
}

func (c *Channel) stderrWriter() io.Writer {
	if c.stderr != nil {
		return c.stderr
	}
	return os.Stderr
}

func unsupported(mode string) error {
	return &channel.ClassifiedError{
		Kind: channel.ErrKindOther,
		Err:  fmt.Errorf("mlx channel does not support %s", mode),
	}
}

// parseOutput removes mlx-lm's optional verbose framing while preserving the
// generated text. Calls pass --verbose false, but older/cached CLI surfaces may
// still emit separators and timing statistics.
func parseOutput(raw string) string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	clean := make([]string, 0, len(lines))
	for _, line := range lines {
		if isSeparator(line) {
			continue
		}
		clean = append(clean, strings.TrimSuffix(line, "\r"))
	}
	for len(clean) > 0 && strings.TrimSpace(clean[len(clean)-1]) == "" {
		clean = clean[:len(clean)-1]
	}
	for len(clean) > 0 && isGenerationStat(clean[len(clean)-1]) {
		clean = clean[:len(clean)-1]
		for len(clean) > 0 && strings.TrimSpace(clean[len(clean)-1]) == "" {
			clean = clean[:len(clean)-1]
		}
	}
	return strings.TrimSpace(strings.Join(clean, "\n"))
}

func isSeparator(line string) bool {
	line = strings.TrimSpace(line)
	return len(line) >= 10 && strings.Trim(line, "=") == ""
}

func isGenerationStat(line string) bool {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "Prompt:"):
		return strings.Contains(line, "token")
	case strings.HasPrefix(line, "Generation:"):
		return strings.Contains(line, "token") || strings.Contains(line, "sec")
	case strings.HasPrefix(line, "Peak memory:"):
		return true
	default:
		return false
	}
}

func estimateTokens(s string) int { return len(s) / 4 }
