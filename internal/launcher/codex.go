package launcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CodexHost launches the interactive Codex CLI.
type CodexHost struct {
	Bin string // "" = "codex" on PATH
}

func (h *CodexHost) Name() string { return "codex" }

// ResumeArgs maps a styx resume request onto the Codex CLI's resume
// subcommand. The adapter places these arguments before global config flags.
func (h *CodexHost) ResumeArgs(sessionID string) []string {
	if sessionID == "" {
		return []string{"resume", "--last"}
	}
	return []string{"resume", sessionID}
}

// Launch execs codex in the project dir with per-invocation MCP and guidance
// configuration. It never mutates the user's Codex config.
func (h *CodexHost) Launch(ctx context.Context, o Opts) error {
	bin := h.Bin
	if bin == "" {
		bin = "codex"
	}
	cmd := exec.CommandContext(ctx, bin, codexArgs(o)...)
	cmd.Dir = o.ProjectPath
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launch %s: %w", h.Name(), err)
	}
	return nil
}

// codexArgs assembles the Codex CLI argv. Resume must be subcommand-first;
// all configuration remains per-invocation and is accepted by resume.
func codexArgs(o Opts) []string {
	args := append([]string{}, o.ResumeArgs...)
	args = append(args,
		"-c", "mcp_servers.styx.command="+tomlBasicString(o.StyxBin),
		"-c", `mcp_servers.styx.args=["mcp"]`,
		"-c", "developer_instructions="+tomlBasicString(o.Guidance),
	)
	for _, r := range o.ExtraRepos {
		args = append(args, "--add-dir", r)
	}
	return args
}

// tomlBasicString encodes a value as a TOML basic quoted string. Codex parses
// -c values as TOML, so guidance must never be passed as an ambiguous raw value.
func tomlBasicString(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
