// Package graph keeps per-project graphify knowledge graphs fresh. It wraps
// the external `graphify` CLI (tree-sitter code knowledge graphs, installed
// separately via `uv tool install graphifyy`). Builds run `graphify . --update`
// inside the repo; artifacts stay in the repo's graphify-out/ directory (the
// CLI's only output location, and where graphify's own Claude Code skill and
// hooks expect them). Styx records only build metadata under
// ~/.config/styx/state/graph/<project-id>/ to decide when a rebuild is due:
// a graph is stale when the repo's git HEAD has moved since the last build or
// the artifact is missing. Rebuilds are cheap (graphify --update is an
// incremental, SHA256-cached AST pass), which is why staleness is HEAD-exact
// rather than intel's tolerant 5-commit/7-day rule.
package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/paths"
)

const SchemaVersion = 1

// BuildTimeout bounds one graphify build; also the lock-expiry horizon.
const BuildTimeout = 10 * time.Minute

// Meta is the persisted record of the last successful build.
type Meta struct {
	SchemaVersion int       `json:"schema_version"`
	BuiltAt       time.Time `json:"built_at"`
	GitHead       string    `json:"git_head"`
}

// StateDir returns ~/.config/styx/state/graph/<project-id>/.
func StateDir(proj config.Project) (string, error) {
	s, err := paths.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolve state dir: %w", err)
	}
	return filepath.Join(s, "graph", proj.ID), nil
}

func metaPath(proj config.Project) (string, error) {
	d, err := StateDir(proj)
	if err != nil {
		return "", fmt.Errorf("resolve graph state dir: %w", err)
	}
	return filepath.Join(d, "meta.json"), nil
}

// GraphPath returns the repo-local artifact graphify writes and consumers read.
func GraphPath(proj config.Project) string {
	return filepath.Join(proj.Path, "graphify-out", "graph.json")
}

// SaveMeta atomically writes the build record.
func SaveMeta(proj config.Project, m *Meta) error {
	p, err := metaPath(proj)
	if err != nil {
		return fmt.Errorf("resolve graph meta path: %w", err)
	}
	if err := paths.EnsureDir(filepath.Dir(p)); err != nil {
		return fmt.Errorf("ensure graph state dir: %w", err)
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal graph meta: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write graph meta: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("commit graph meta: %w", err)
	}
	return nil
}

// LoadMeta reads the build record.
func LoadMeta(proj config.Project) (*Meta, error) {
	p, err := metaPath(proj)
	if err != nil {
		return nil, fmt.Errorf("resolve graph meta path: %w", err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		// Deliberately unwrapped: IsStale distinguishes "no meta yet" via
		// os.IsNotExist, which does not see through fmt.Errorf wrapping.
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse graph meta: %w", err)
	}
	return &m, nil
}

// IsStale reports whether proj needs a graph (re)build and why. Projects
// without a registry ID (e.g. the conductor's plain-directory focus) are never
// stale: graph state is keyed by ID, so there is nowhere to record a build.
func IsStale(proj config.Project) (bool, string) {
	if proj.ID == "" {
		return false, ""
	}
	m, err := LoadMeta(proj)
	if err != nil {
		if os.IsNotExist(err) {
			return true, "no graph built yet"
		}
		return true, "meta load failed: " + err.Error()
	}
	if _, err := os.Stat(GraphPath(proj)); err != nil {
		return true, "graph artifact missing (graphify-out/graph.json)"
	}
	if head := gitHead(proj.Path); head != m.GitHead {
		return true, "git HEAD moved since last build"
	}
	return false, ""
}

// gitHead returns the repo's current HEAD sha, or "" outside git.
func gitHead(repo string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
