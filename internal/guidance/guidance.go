// Package guidance owns the conductor's routing guidance: the data-driven
// replacement for the v0.2 brain's compiled-in preamble. Global file seeded
// at ~/.config/styx/guidance.md (user-editable, never overwritten), optional
// per-repo override at <repo>/styx/guidance.md appended on load.
package guidance

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/paths"
)

func guidanceFile() (string, error) { return paths.GuidancePath() }

// Load returns the effective guidance for a project, seeding the global
// file on first use.
func Load(projectPath string) (string, error) {
	p, err := guidanceFile()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(p); os.IsNotExist(err) {
		if err := writeAtomic(p, []byte(Seed)); err != nil {
			return "", fmt.Errorf("seed guidance: %w", err)
		}
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf("read guidance: %w", err)
	}
	out := string(b)
	if o, err := os.ReadFile(filepath.Join(projectPath, "styx", "guidance.md")); err == nil {
		out += "\n\n## Project guidance (styx/guidance.md)\n\n" + string(o)
	}
	return out, nil
}

// writeAtomic follows the repo-wide tmp+rename convention.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Seed is the default guidance installed on first run.
const Seed = `# Styx conductor guidance

You are orchestrating development through styx. You have MCP tools:
dispatch, thread_status, budget_status, recall, memory_save, get_intel,
refresh_intel, pipeline_run, route, channel_health.

## Channel best purposes (dispatch cli=...)
- codex — PRIMARY IMPLEMENTER for well-scoped work from a clear plan/spec:
  named failing tests, specced features, mechanical renames, writing tests
  for a named target, algorithmic one-shots, sandboxed script runs.
- claude — ambiguous or architectural implementation, refactors/redesigns,
  debugging with repo context, planning, reviewing a plan/design/PR/module.
- agy — summarizing or explaining VERY LARGE files/packages/diffs (the
  size signal wins; normal-sized explain goes to claude).
- ollama — trivial local one-shots ONLY: commit messages, boilerplate,
  stubs, trivial classification. Never real implementation.
Model tiers for claude dispatches: opus = judgment-heavy/complex; sonnet =
implementation, refactors, review; haiku = trivial.

## Working style
- Check budget_status before large fan-outs; prefer cheaper channels as a
  cap approaches, but performance beats efficiency when they conflict.
- Write plans and briefs to styx/plans/ before dispatching multi-step
  work; write per-thread handoffs to styx/handoffs/<thread>.md. Dispatch
  messages should reference those files, not restate them.
- Threads persist across turns: check thread_status before creating new
  threads; reuse a thread that already has the context.
- Consult recall before re-deriving project facts; memory_save durable
  decisions (kind=decision) and user preferences as you learn them.

## Ship policy
dispatch with risk=ship and pipeline_run auto return a confirmation token.
Relay the token to the user VERBATIM and resubmit only after the user
types it back. Never invent, guess, or self-supply a token.
`
