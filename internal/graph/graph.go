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
	"context"
	"encoding/json"
	"errors"
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

// ErrBuildInProgress is returned when another styx process holds the build lock.
var ErrBuildInProgress = errors.New("graph build already in progress")

// Available reports whether the graphify integration is active: the external
// CLI is on PATH and the STYX_GRAPHIFY=off escape hatch is not set. This is
// the feature's entire configuration surface — no routing.toml key.
func Available() (string, bool) {
	if os.Getenv("STYX_GRAPHIFY") == "off" {
		return "", false
	}
	bin, err := exec.LookPath("graphify")
	if err != nil {
		return "", false
	}
	return bin, true
}

// LogPath returns the build log location for narration.
func LogPath(proj config.Project) (string, error) {
	d, err := StateDir(proj)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "build.log"), nil
}

// tryLock takes the per-project build lock. A lock file older than
// BuildTimeout belongs to a dead build and is reclaimed.
func tryLock(dir string) (release func(), err error) {
	lock := filepath.Join(dir, "build.lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if os.IsExist(err) {
		fi, serr := os.Stat(lock)
		if serr == nil && time.Since(fi.ModTime()) > BuildTimeout {
			if rerr := os.Remove(lock); rerr == nil {
				return tryLock(dir) // one retry after reclaiming
			}
		}
		return nil, ErrBuildInProgress
	}
	if err != nil {
		return nil, fmt.Errorf("take build lock: %w", err)
	}
	fmt.Fprintf(f, "%d", os.Getpid())
	f.Close()
	return func() { os.Remove(lock) }, nil
}

// graphShape is the minimal structure a valid graph.json must parse into.
type graphShape struct {
	Nodes []json.RawMessage `json:"nodes"`
	Edges []json.RawMessage `json:"edges"`
}

// Build runs `<bin> . --update` inside the repo, streaming output to
// state/graph/<id>/build.log, then validates graphify-out/graph.json and
// records the built HEAD. ctx bounds the subprocess (callers pass a
// BuildTimeout-derived context).
func Build(ctx context.Context, proj config.Project, bin string) error {
	d, err := StateDir(proj)
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(d); err != nil {
		return fmt.Errorf("ensure graph state dir: %w", err)
	}
	release, err := tryLock(d)
	if err != nil {
		return err
	}
	defer release()

	logPath := filepath.Join(d, "build.log")
	logF, err := os.Create(logPath)
	if err != nil {
		return fmt.Errorf("open build log: %w", err)
	}
	defer logF.Close()

	head := gitHead(proj.Path) // record BEFORE the build: commits landing mid-build re-trigger next check
	cmd := exec.CommandContext(ctx, bin, ".", "--update")
	cmd.Dir = proj.Path
	cmd.Stdout, cmd.Stderr = logF, logF
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("graphify build failed (log: %s): %w", logPath, err)
	}

	raw, err := os.ReadFile(GraphPath(proj))
	if err != nil {
		return fmt.Errorf("graphify produced no graph.json (log: %s): %w", logPath, err)
	}
	var shape graphShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return fmt.Errorf("graph.json is not valid JSON (log: %s): %w", logPath, err)
	}
	if len(shape.Nodes) == 0 {
		return fmt.Errorf("graph.json has zero nodes — refusing to record build (log: %s)", logPath)
	}

	return SaveMeta(proj, &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: head})
}
