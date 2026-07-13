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
	RouteGate   string   // block | audit | off — installs the styx hook that enforces dispatch-over-inline routing
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
	if err := writeAtomic(cfgPath, raw); err != nil {
		return fmt.Errorf("write mcp config: %w", err)
	}
	settingsPath, err := writeConductorSettings(stateDir, o.StyxBin, o.RouteGate)
	if err != nil {
		return fmt.Errorf("write conductor settings: %w", err)
	}
	bin := h.Bin
	if bin == "" {
		bin = "claude"
	}
	args := claudeArgs(cfgPath, settingsPath, o)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = o.ProjectPath
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launch %s: %w", h.Name(), err)
	}
	return nil
}

// claudeArgs assembles the claude CLI argv. --settings always points at the
// styx-owned conductor settings file, which is scoped to sessions styx launches
// and leaves the user's normal Claude Code usage untouched. We deliberately do
// NOT pass --strict-mcp-config: the user's other MCP servers stay available and
// the styx hook's matcher catches MCP web tools by name instead.
func claudeArgs(cfgPath, settingsPath string, o Opts) []string {
	args := []string{"--mcp-config", cfgPath, "--settings", settingsPath}
	args = append(args, "--append-system-prompt", o.Guidance)
	for _, r := range o.ExtraRepos {
		args = append(args, "--add-dir", r)
	}
	return append(args, o.ExtraArgs...)
}

// writeConductorSettings writes the styx-owned Claude Code settings file for a
// conductor session and returns its path. The file always disables Claude
// Code's built-in co-author attribution; mode controls only which routing hooks
// are present. It lives in styx's state dir (not the user's repo .claude/), so
// it is scoped to sessions styx launches and never touches the user's normal
// Claude Code usage.
func writeConductorSettings(stateDir, styxBin, mode string) (string, error) {
	settings := buildConductorSettings(styxBin, mode)
	raw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal settings: %w", err)
	}
	path := filepath.Join(stateDir, "conductor-settings.json")
	if err := writeAtomic(path, raw); err != nil {
		return "", err
	}
	return path, nil
}

// buildConductorSettings returns the Claude Code settings object for a route
// gate mode. Every mode disables Claude Code's built-in co-author attribution.
// "off" installs no hooks; "audit" installs only the PostToolUse recorder;
// anything else (i.e. "block") also installs the PreToolUse deny — a
// fail-closed default so an unrecognized mode still enforces rather than
// silently disabling the gate. The matcher is a coarse funnel; `styx hook`
// makes the fine-grained decision.
func buildConductorSettings(styxBin, mode string) map[string]any {
	settings := map[string]any{"includeCoAuthoredBy": false}
	if mode == "off" {
		return settings
	}
	post := hookMatcher("Read|Grep|Bash|WebSearch|WebFetch|Task|mcp__", styxBin, "posttooluse")
	hooks := map[string]any{"PostToolUse": []any{post}}
	if mode != "audit" {
		pre := hookMatcher("WebSearch|WebFetch|Task|Bash|mcp__", styxBin, "pretooluse")
		hooks["PreToolUse"] = []any{pre}
	}
	settings["hooks"] = hooks
	return settings
}

// hookMatcher builds one Claude Code hook matcher entry that pipes matched
// tool calls to `styx hook <event>`. Exec form (command + args, no shell):
// on native Windows shell-form hooks run under Git Bash or a PowerShell
// fallback with incompatible quoting rules, and exec form sidesteps shell
// parsing entirely on every platform.
func hookMatcher(matcher, styxBin, event string) map[string]any {
	return map[string]any{
		"matcher": matcher,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": styxBin,
			"args":    []string{"hook", event},
		}},
	}
}

// writeAtomic writes data via the repo-wide tmp+rename convention.
func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
