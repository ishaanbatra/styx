// Package launcher opens a frontier-brain host session (Claude Code first)
// with styx attached as an MCP toolbelt. Host adapters are the ONLY
// host-specific code in the conductor; everything else is portable MCP.
package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Opts is everything a host needs to start a conductor session.
type Opts struct {
	ProjectPath string
	StyxBin     string
	Guidance    string
	ExtraRepos  []string // absolute paths of additional bound repos
	ExtraArgs   []string // host CLI args appended after the standard flags (e.g. --resume <id>)
}

// Host launches one brain CLI wired to the styx MCP server.
type Host interface {
	Name() string
	Launch(ctx context.Context, o Opts) error
}

// ClaudeHost launches Claude Code.
type ClaudeHost struct {
	Bin string // "" = "claude" on PATH
}

func (h *ClaudeHost) Name() string { return "claude" }

// Launch writes an MCP config binding the "styx" server to `<StyxBin> mcp`,
// then execs claude in the project dir with stdio passthrough so the user
// drives the resulting session directly.
func (h *ClaudeHost) Launch(ctx context.Context, o Opts) error {
	stateDir, err := paths.StateDir()
	if err != nil {
		return fmt.Errorf("resolve state dir: %w", err)
	}
	if err := paths.EnsureDir(stateDir); err != nil {
		return fmt.Errorf("ensure state dir: %w", err)
	}
	cfg := map[string]any{"mcpServers": map[string]any{
		"styx": map[string]any{"command": o.StyxBin, "args": []string{"mcp"}},
	}}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mcp config: %w", err)
	}
	cfgPath := filepath.Join(stateDir, "conductor-mcp.json")
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write mcp config: %w", err)
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		return fmt.Errorf("finalize mcp config: %w", err)
	}
	bin := h.Bin
	if bin == "" {
		bin = "claude"
	}
	args := []string{"--mcp-config", cfgPath, "--append-system-prompt", o.Guidance}
	for _, r := range o.ExtraRepos {
		args = append(args, "--add-dir", r)
	}
	args = append(args, o.ExtraArgs...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = o.ProjectPath
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launch %s: %w", h.Name(), err)
	}
	return nil
}
