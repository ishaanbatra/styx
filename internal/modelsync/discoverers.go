package modelsync

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeDiscoverer reports claude's stable class aliases (always latest).
type ClaudeDiscoverer struct{}

func (ClaudeDiscoverer) Channel() string { return "claude" }

func (ClaudeDiscoverer) Discover(context.Context) (Result, error) {
	return Result{
		Available: []string{"opus", "sonnet", "haiku", "fable"},
		Source:    "claude-alias",
	}, nil
}

// CodexDiscoverer reads codex's own current default model from its config.
type CodexDiscoverer struct {
	ConfigPath string // "" => ~/.codex/config.toml
}

func (CodexDiscoverer) Channel() string { return "codex" }

func (d CodexDiscoverer) Discover(context.Context) (Result, error) {
	path := d.ConfigPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Result{}, fmt.Errorf("locate home for codex config: %w", err)
		}
		path = filepath.Join(home, ".codex", "config.toml")
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open codex config: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			break
		}
		if v, ok := topLevelModel(line); ok {
			return Result{Current: v, Source: "codex-config"}, nil
		}
	}
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("read codex config: %w", err)
	}
	return Result{}, fmt.Errorf("no top-level model in %s", path)
}

// topLevelModel parses a `model = "x"` line, returning ("x", true) on match.
func topLevelModel(line string) (string, bool) {
	if !strings.HasPrefix(line, "model") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "model"))
	if !strings.HasPrefix(rest, "=") {
		return "", false
	}
	val := strings.TrimSpace(strings.TrimPrefix(rest, "="))
	val = strings.Trim(val, "\"'")
	if val == "" {
		return "", false
	}
	return val, true
}
