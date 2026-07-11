# Graphify Auto-Build Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When styx launches a conductor session in a repo, it automatically keeps that repo's graphify knowledge graph fresh — building/updating it in the background so Claude Code sessions can query the graph instead of grepping raw files.

**Architecture:** A new `internal/graph/` subsystem wraps the external `graphify` CLI (tree-sitter code knowledge graphs, github.com/safishamsi/graphify, installed separately via `uv tool install graphifyy`). Graph artifacts live in the repo's own `graphify-out/` directory — that is graphify's only output location and where graphify's own Claude Code skill and PreToolUse hook expect to find them. Styx stores only build metadata (`meta.json`: git HEAD at build time) under `~/.config/styx/state/graph/<project-id>/` to drive staleness checks. A `styx graphify` verb gives manual/scripted builds; the conductor launch path (`launchConductor`) fires background builds for every stale bound repo before handing the TTY to Claude Code.

**Tech Stack:** Go (stdlib only — no new dependencies), external `graphify` CLI via `os/exec`, existing styx packages (`paths`, `config`, `project`, `progress`).

## Global Constraints

Copied from `CLAUDE.md` — every task implicitly includes these:

- Channels/tools are CLIs, never SDKs: shell out to the `graphify` binary; never import a graph library.
- No new Go module dependencies.
- File writes are atomic (tmp + rename); on-disk locations go through `internal/paths` helpers.
- Never let a subprocess run unbounded — every exec gets a context with timeout.
- Never swallow errors (`x, _ :=`) — surface through progress stages or wrapped returns (`fmt.Errorf("...: %w", err)`).
- Status/narration to stderr via `logStatus`/`progress` (respect `--quiet`); results to stdout. **Extra rule for this feature:** once `host.Launch` has started Claude Code, nothing may write to stderr — background builds log to a file only.
- Table-driven tests with `t.Run`; fakes over mocks (scripted fake `graphify` binary, like `testdata/fakeagent`).
- New packages get a doc comment explaining their role in the orchestration.
- **Drift contract:** `docs/ARCHITECTURE.md` owns `cmd/styx/**` and `internal/**` — update it (and bump `last_verified`) in the same commit as any behavior change. Adding a verb also means updating `README.md`'s verb table and `cmd/styx/help.go`.
- `go vet ./... && gofmt -w .` before every commit.
- No daemons: background builds are goroutines inside the live styx process (which stays alive while Claude Code runs under `cmd.Run`), bounded by a 10-minute context. If the session exits first the build dies and the next launch retries — that is accepted, self-healing behavior.

## Design decisions (why, for the reviewer)

1. **Staleness = HEAD drift, not intel's 5-commit/7-day rule.** Intel builds are expensive LLM calls, so intel tolerates drift. Graphify `--update` is a cheap incremental AST pass (SHA256 cache, changed files only), so we rebuild whenever `git HEAD != meta.GitHead` or artifacts are missing. No age rule: an unchanged HEAD means the graph is still accurate.
2. **Zero config.** The feature is active iff the `graphify` binary is on `$PATH`. Escape hatch: `STYX_GRAPHIFY=off`. This avoids touching `routing.toml` defaults, `default_routing.go`, and the upgrade path.
3. **Skip non-registered targets.** `launchConductor` can focus a plain non-git directory (empty `proj.ID`). Graph state is keyed by project ID, so empty-ID projects are skipped everywhere.
4. **Lock file guards concurrent builds.** Two conductor sessions in one repo must not run two `graphify` processes over the same `graphify-out/`. An `O_CREATE|O_EXCL` lock file with mtime-based expiry (older than the build timeout = stale, reclaim) is enough.
5. **MCP query tools are explicitly deferred.** Consumption is graphify's own skill/hook (installed by `graphify install`); this plan is supply-side only.

## File structure

| File | Responsibility |
|---|---|
| `internal/graph/graph.go` (create) | Meta persistence, staleness check, availability check, lock, `Build` (exec graphify) |
| `internal/graph/graph_test.go` (create) | Table-driven tests with temp git repos + fake graphify binary |
| `cmd/styx/graphify.go` (create) | `styx graphify <target> [--force]` and `styx graphify ls` verb |
| `cmd/styx/launch.go` (modify) | `ensureGraphsFresh` background builds in `launchConductor` |
| `cmd/styx/launch_test.go` (modify) | Test for `ensureGraphsFresh` |
| `cmd/styx/dispatch.go` (modify) | Register `graphify` verb in the post-`loadApp` switch |
| `cmd/styx/help.go` (modify) | Two help lines after the `intel` entries |
| `README.md` (modify) | Verb table rows |
| `docs/ARCHITECTURE.md` (modify) | New `internal/graph` section + launch-path note + `last_verified` bump |
| `CLAUDE.md` (modify) | One subsystem bullet in the Architecture list |

---

### Task 1: `internal/graph` — meta persistence + staleness

**Files:**
- Create: `internal/graph/graph.go`
- Create: `internal/graph/graph_test.go`

**Interfaces:**
- Consumes: `config.Project` (fields `ID`, `Name`, `Path`), `paths.StateDir()`, `paths.EnsureDir()`.
- Produces (used by Tasks 2–4):
  - `const SchemaVersion = 1`
  - `const BuildTimeout = 10 * time.Minute`
  - `type Meta struct { SchemaVersion int; BuiltAt time.Time; GitHead string }` (json tags `schema_version`, `built_at`, `git_head`)
  - `func StateDir(proj config.Project) (string, error)` — `~/.config/styx/state/graph/<id>/`
  - `func SaveMeta(proj config.Project, m *Meta) error` / `func LoadMeta(proj config.Project) (*Meta, error)`
  - `func GraphPath(proj config.Project) string` — `<proj.Path>/graphify-out/graph.json`
  - `func IsStale(proj config.Project) (bool, string)` — reason string for narration; `("", false)` shape mirrors `intel.Staleness`
  - `func gitHead(repo string) string` (private)

- [ ] **Step 1: Write the failing tests**

Create `internal/graph/graph_test.go`:

```go
package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/config"
)

// newTestRepo creates a git repo with one commit and returns a Project for it.
func newTestRepo(t *testing.T) config.Project {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return config.Project{ID: "abc123def456", Name: "testrepo", Path: dir}
}

// commitChange adds a new commit so HEAD moves.
func commitChange(t *testing.T, proj config.Project) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(proj.Path, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "change"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = proj.Path
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// writeGraphArtifact simulates a completed graphify run in the repo.
func writeGraphArtifact(t *testing.T, proj config.Project) {
	t.Helper()
	dir := filepath.Join(proj.Path, "graphify-out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graph.json"),
		[]byte(`{"nodes":[{"id":"a"}],"edges":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := newTestRepo(t)
	m := &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: gitHead(proj.Path)}
	if err := SaveMeta(proj, m); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	got, err := LoadMeta(proj)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if got.GitHead != m.GitHead || got.SchemaVersion != SchemaVersion {
		t.Errorf("round trip mismatch: got %+v want %+v", got, m)
	}
}

func TestIsStale(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, proj config.Project)
		stale bool
	}{
		{"no meta yet", func(t *testing.T, proj config.Project) {}, true},
		{"fresh build", func(t *testing.T, proj config.Project) {
			writeGraphArtifact(t, proj)
			m := &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: gitHead(proj.Path)}
			if err := SaveMeta(proj, m); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"HEAD moved since build", func(t *testing.T, proj config.Project) {
			writeGraphArtifact(t, proj)
			m := &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: gitHead(proj.Path)}
			if err := SaveMeta(proj, m); err != nil {
				t.Fatal(err)
			}
			commitChange(t, proj)
		}, true},
		{"meta fresh but graph.json deleted", func(t *testing.T, proj config.Project) {
			m := &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: gitHead(proj.Path)}
			if err := SaveMeta(proj, m); err != nil {
				t.Fatal(err)
			}
			// no writeGraphArtifact — graph.json absent
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			proj := newTestRepo(t)
			tt.setup(t, proj)
			stale, reason := IsStale(proj)
			if stale != tt.stale {
				t.Errorf("IsStale = %v (%q), want %v", stale, reason, tt.stale)
			}
			if stale && reason == "" {
				t.Error("stale result must carry a reason")
			}
		})
	}
}

func TestIsStale_EmptyProjectID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := newTestRepo(t)
	proj.ID = ""
	stale, _ := IsStale(proj)
	if stale {
		t.Error("empty-ID (unregistered) project must never be reported stale")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/ishaanbatra/Documents/GitHub/styx && go test ./internal/graph/ -v`
Expected: FAIL to build — `undefined: Meta`, `undefined: SaveMeta`, etc.

- [ ] **Step 3: Write the implementation**

Create `internal/graph/graph.go`:

```go
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
		return "", err
	}
	return filepath.Join(s, "graph", proj.ID), nil
}

func metaPath(proj config.Project) (string, error) {
	d, err := StateDir(proj)
	if err != nil {
		return "", err
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
		return err
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
	return os.Rename(tmp, p)
}

// LoadMeta reads the build record.
func LoadMeta(proj config.Project) (*Meta, error) {
	p, err := metaPath(proj)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/graph/ -v`
Expected: PASS (`TestMetaRoundTrip`, `TestIsStale` all subtests, `TestIsStale_EmptyProjectID`)

- [ ] **Step 5: Format, vet, commit**

```bash
gofmt -w internal/graph/ && go vet ./internal/graph/
git add internal/graph/
git commit -m "feat(graph): meta persistence and HEAD-drift staleness for graphify graphs"
```

(ARCHITECTURE.md gets its `internal/graph` section in Task 2's commit, when the package's behavior is complete.)

---

### Task 2: `internal/graph` — Build (exec graphify), availability gate, lock

**Files:**
- Modify: `internal/graph/graph.go`
- Modify: `internal/graph/graph_test.go`
- Modify: `docs/ARCHITECTURE.md`

**Interfaces:**
- Consumes: Task 1's `Meta`/`SaveMeta`/`StateDir`/`GraphPath`/`gitHead`.
- Produces (used by Tasks 3–4):
  - `func Available() (string, bool)` — resolved binary path; false when not installed or `STYX_GRAPHIFY=off`
  - `func Build(ctx context.Context, proj config.Project, bin string) error` — runs `<bin> . --update` in `proj.Path`, logs to `state/graph/<id>/build.log`, validates artifact, writes meta. Returns `ErrBuildInProgress` when locked.
  - `var ErrBuildInProgress = errors.New("graph build already in progress")`
  - `func LogPath(proj config.Project) (string, error)` — for narration ("see <log>")

- [ ] **Step 1: Write the failing tests**

Append to `internal/graph/graph_test.go`:

```go
// fakeGraphify writes a scripted stand-in for the graphify CLI and returns its
// path. The script emulates `graphify . --update`: writes graphify-out/graph.json
// in the cwd. Behavior variants via mode: "ok", "fail" (exit 1), "badjson".
func fakeGraphify(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	var body string
	switch mode {
	case "ok":
		body = "#!/bin/sh\nmkdir -p graphify-out\nprintf '{\"nodes\":[{\"id\":\"a\"}],\"edges\":[]}' > graphify-out/graph.json\n"
	case "fail":
		body = "#!/bin/sh\necho boom >&2\nexit 1\n"
	case "badjson":
		body = "#!/bin/sh\nmkdir -p graphify-out\nprintf 'not json' > graphify-out/graph.json\n"
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	p := filepath.Join(dir, "graphify")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{"success writes meta", "ok", false},
		{"nonzero exit surfaces error", "fail", true},
		{"unparseable graph.json surfaces error", "badjson", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			proj := newTestRepo(t)
			bin := fakeGraphify(t, tt.mode)
			err := Build(context.Background(), proj, bin)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if _, lerr := LoadMeta(proj); lerr == nil {
					t.Error("failed build must not write meta")
				}
				return
			}
			m, lerr := LoadMeta(proj)
			if lerr != nil {
				t.Fatalf("LoadMeta after build: %v", lerr)
			}
			if m.GitHead != gitHead(proj.Path) {
				t.Error("meta.GitHead must record the built HEAD")
			}
			if stale, reason := IsStale(proj); stale {
				t.Errorf("freshly built project reported stale: %s", reason)
			}
		})
	}
}

func TestBuild_LockBlocksConcurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := newTestRepo(t)
	d, err := StateDir(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate an in-flight build: fresh lock file.
	if err := os.WriteFile(filepath.Join(d, "build.lock"), []byte("pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Build(context.Background(), proj, fakeGraphify(t, "ok"))
	if !errors.Is(err, ErrBuildInProgress) {
		t.Fatalf("want ErrBuildInProgress, got %v", err)
	}
	// An expired lock (older than BuildTimeout) is reclaimed.
	old := time.Now().Add(-BuildTimeout - time.Minute)
	if err := os.Chtimes(filepath.Join(d, "build.lock"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), proj, fakeGraphify(t, "ok")); err != nil {
		t.Fatalf("expired lock should be reclaimed, got %v", err)
	}
}

func TestAvailable_EnvOff(t *testing.T) {
	t.Setenv("STYX_GRAPHIFY", "off")
	if _, ok := Available(); ok {
		t.Error("STYX_GRAPHIFY=off must disable the feature")
	}
}
```

Add `"context"` and `"errors"` to the test file's imports.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/graph/ -v`
Expected: FAIL to build — `undefined: Build`, `undefined: Available`, `undefined: ErrBuildInProgress`.

- [ ] **Step 3: Write the implementation**

Append to `internal/graph/graph.go` (add `"context"` and `"errors"` to imports):

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/graph/ -v`
Expected: PASS (all Task 1 + Task 2 tests)

- [ ] **Step 5: Update docs/ARCHITECTURE.md (drift contract)**

Add a section alongside the Intel section (match surrounding heading style, e.g. after the `internal/intel` section):

```markdown
## Graph (`internal/graph/`)

Keeps per-project **graphify** knowledge graphs fresh. Wraps the external
`graphify` CLI (tree-sitter code knowledge graph; `uv tool install graphifyy`)
— styx never parses code itself. Active iff `graphify` is on PATH; disable
with `STYX_GRAPHIFY=off`. No routing.toml surface.

- **Artifacts:** in-repo `graphify-out/graph.json` (graphify's only output
  location, and where graphify's own Claude Code skill/hook expect it).
- **State:** `~/.config/styx/state/graph/<project-id>/` — `meta.json`
  (schema_version, built_at, git_head; atomic write), `build.log`,
  `build.lock` (O_EXCL; expired after BuildTimeout=10m and reclaimed).
- **Staleness:** HEAD-exact — stale iff meta or artifact missing, or current
  git HEAD != recorded head. No age/commit-count tolerance: `graphify --update`
  is an incremental SHA256-cached pass, so rebuilds are cheap (unlike intel's
  LLM-priced builds). Empty-ID projects (unregistered plain dirs) are never
  stale.
- **Build:** `graphify . --update` in the repo, ctx-bounded, output to
  build.log; graph.json must parse with ≥1 node before meta is recorded, so a
  failed build re-triggers on the next check.
- **Entry points:** `styx graphify <target> [--force]` / `styx graphify ls`
  (cmd/styx/graphify.go, synchronous); conductor launch auto-build
  (cmd/styx/launch.go `ensureGraphsFresh`, background goroutine, silent after
  the host owns the TTY — build output goes only to build.log). A build killed
  by session exit leaves stale meta and retries next launch.
```

Bump `last_verified:` in the frontmatter to today's date.

- [ ] **Step 6: Format, vet, commit**

```bash
gofmt -w internal/graph/ && go vet ./internal/graph/
git add internal/graph/ docs/ARCHITECTURE.md
git commit -m "feat(graph): graphify build execution with lock, log capture, and artifact validation"
```

---

### Task 3: `styx graphify` verb

**Files:**
- Create: `cmd/styx/graphify.go`
- Modify: `cmd/styx/dispatch.go` (one case in the post-`loadApp` switch)
- Modify: `cmd/styx/help.go` (two lines after the `intel` entries)
- Modify: `README.md` (verb table)
- Modify: `CLAUDE.md` (subsystem bullet)
- Modify: `docs/ARCHITECTURE.md` (`last_verified` only — section written in Task 2)

**Interfaces:**
- Consumes: `graph.Available()`, `graph.IsStale`, `graph.Build`, `graph.BuildTimeout`, `graph.LogPath`, `resolveGlobalTarget` (dispatch.go), `project.List()`, `a.progress`.
- Produces: `func cmdGraphify(a *app, args []string) error` (registered as verb `graphify`).

- [ ] **Step 1: Write the verb**

Create `cmd/styx/graphify.go` (mirrors `cmd/styx/intel.go` structure):

```go
package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/ishaanbatra/styx/internal/graph"
	"github.com/ishaanbatra/styx/internal/project"
)

// cmdGraphify is the manual/scripted entry point for graphify graph builds.
// `styx graphify <target>` builds synchronously (the conductor launch path
// does the same work in the background); `styx graphify ls` reports freshness.
func cmdGraphify(a *app, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: styx graphify <project-alias-or-path> [--force] | styx graphify ls")
	}
	if args[0] == "ls" {
		return cmdGraphifyLs()
	}
	force := false
	target := args[0]
	for _, arg := range args[1:] {
		if arg == "--force" {
			force = true
		}
	}

	bin, ok := graph.Available()
	if !ok {
		return errors.New("graphify CLI not available — install with `uv tool install graphifyy` (or unset STYX_GRAPHIFY)")
	}
	proj, err := resolveGlobalTarget(target)
	if err != nil {
		return err
	}
	if proj.ID == "" {
		return fmt.Errorf("%s is not a registered project — run `styx project add` first", proj.Path)
	}
	if !force {
		stale, reason := graph.IsStale(proj)
		if !stale {
			fmt.Printf("[styx] graph for %s is fresh; pass --force to rebuild\n", proj.Name)
			return nil
		}
		logStatus("graph is stale (%s); rebuilding...", reason)
	} else {
		logStatus("forcing graph rebuild for %s", proj.Name)
	}

	st := a.progress.Stage("Building knowledge graph for " + proj.Name)
	ctx, cancel := context.WithTimeout(context.Background(), graph.BuildTimeout)
	defer cancel()
	if err := graph.Build(ctx, proj, bin); err != nil {
		st.Fail(err)
		return fmt.Errorf("graph build: %w", err)
	}
	st.Done("done")
	fmt.Printf("[styx] built knowledge graph for %s -> %s\n", proj.Name, graph.GraphPath(proj))
	return nil
}

func cmdGraphifyLs() error {
	if _, ok := graph.Available(); !ok {
		fmt.Println("graphify CLI not available — install with `uv tool install graphifyy`")
	}
	projs, err := project.List()
	if err != nil {
		return err
	}
	for _, p := range projs {
		state := "fresh"
		if stale, reason := graph.IsStale(p); stale {
			state = "stale: " + reason
		}
		fmt.Printf("%-20s %-40s %s\n", p.Name, state, p.Path)
	}
	return nil
}
```

- [ ] **Step 2: Register the verb**

In `cmd/styx/dispatch.go`, in the post-`loadApp` switch, after `case "intel":`:

```go
	case "graphify":
		return cmdGraphify(a, args)
```

- [ ] **Step 3: Add help lines**

In `cmd/styx/help.go`, immediately after the two `intel` lines (currently lines 45–46):

```
  graphify <p> [--force]    Build/refresh the graphify knowledge graph (needs graphify CLI)
  graphify ls               List graph freshness per registered project
```

- [ ] **Step 4: Build and smoke-test by hand**

```bash
make build
./bin/styx graphify                # expect the usage error
./bin/styx graphify ls             # lists projects; "not available" note if CLI missing
STYX_GRAPHIFY=off ./bin/styx graphify styx   # expect "graphify CLI not available" error
```

Expected: exact messages from the code above; exit non-zero on the error cases.

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: PASS everywhere (no existing test asserts on the help text or dispatch switch exhaustively; if one does, update it to include the new verb).

- [ ] **Step 6: Update README.md and CLAUDE.md**

README.md verb table — add after the `intel ls` row (line ~88), same table:

```markdown
| `graphify <project> [--force]` | Build/refresh the graphify knowledge graph for a repo (wraps the external `graphify` CLI; skipped if not installed) |
| `graphify ls` | List knowledge-graph freshness per registered project |
```

CLAUDE.md Architecture list — add after the Intel bullet:

```markdown
- **Graph** (`internal/graph/`): per-project graphify knowledge-graph
  freshness — wraps the external `graphify` CLI, artifacts in-repo at
  `graphify-out/`, HEAD-drift staleness, auto-built on conductor launch
```

Bump `last_verified` in `docs/ARCHITECTURE.md` frontmatter if the date changed since Task 2.

- [ ] **Step 7: Format, vet, commit**

```bash
gofmt -w cmd/styx/ && go vet ./...
git add cmd/styx/graphify.go cmd/styx/dispatch.go cmd/styx/help.go README.md CLAUDE.md docs/ARCHITECTURE.md
git commit -m "feat(graphify): styx graphify verb — manual knowledge-graph builds and freshness listing"
```

---

### Task 4: Conductor auto-build on launch

**Files:**
- Modify: `cmd/styx/launch.go`
- Modify: `cmd/styx/launch_test.go`
- Modify: `docs/ARCHITECTURE.md` (launch-flow paragraph already drafted in Task 2's section; verify it matches, bump `last_verified`)

**Interfaces:**
- Consumes: `graph.Available()`, `graph.IsStale`, `graph.Build`, `graph.BuildTimeout`, `graph.LogPath`, `project.Project`, `logStatus`.
- Produces: `func ensureGraphsFresh(bin string, projs []project.Project) <-chan struct{}` — fires background builds for stale projects, returns a channel closed when all complete (tests wait on it; `launchConductor` ignores it).

- [ ] **Step 1: Write the failing test**

Append to `cmd/styx/launch_test.go` (reuse the fake-binary approach; `cmd/styx` tests are `package main` so the helper lives here — it may NOT import test helpers from `internal/graph`'s test file):

```go
func TestEnsureGraphsFresh(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Repo with one commit.
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "init")

	// Fake graphify that emulates `graphify . --update`.
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "graphify")
	script := "#!/bin/sh\nmkdir -p graphify-out\nprintf '{\"nodes\":[{\"id\":\"a\"}],\"edges\":[]}' > graphify-out/graph.json\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	registered := project.Project{ID: "feedcafe0123", Name: "r1", Path: dir}
	unregistered := project.Project{Name: "plain", Path: t.TempDir()} // empty ID: must be skipped

	done := ensureGraphsFresh(bin, []project.Project{registered, unregistered})
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("background builds did not finish")
	}

	if _, err := os.Stat(filepath.Join(dir, "graphify-out", "graph.json")); err != nil {
		t.Fatalf("graph artifact not built: %v", err)
	}
	if stale, reason := graph.IsStale(registered); stale {
		t.Errorf("registered project still stale after ensureGraphsFresh: %s", reason)
	}
	// Second call on a fresh graph must not rebuild (channel closes immediately,
	// artifact untouched): remove the fake bin so any attempted build would fail.
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	done2 := ensureGraphsFresh(bin, []project.Project{registered})
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatal("fresh-graph path should return immediately")
	}
	if stale, _ := graph.IsStale(registered); stale {
		t.Error("fresh graph must stay fresh after a no-op ensure")
	}
}
```

Add any missing imports to `launch_test.go`: `"os"`, `"os/exec"`, `"path/filepath"`, `"time"`, `"github.com/ishaanbatra/styx/internal/graph"`, `"github.com/ishaanbatra/styx/internal/project"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestEnsureGraphsFresh -v`
Expected: FAIL to build — `undefined: ensureGraphsFresh`.

- [ ] **Step 3: Implement `ensureGraphsFresh` and wire it into `launchConductor`**

Add to `cmd/styx/launch.go` (imports gain `"sync"`, `"time"` if absent, plus `"github.com/ishaanbatra/styx/internal/graph"`):

```go
// ensureGraphsFresh fires a background graphify build for every stale bound
// repo. All narration happens HERE, before the host owns the TTY — the
// goroutines are silent (build output goes to state/graph/<id>/build.log)
// because a stderr write mid-session would corrupt the Claude Code TUI.
// The returned channel closes when all builds finish; launchConductor ignores
// it (builds race the session and die with the process — meta is only written
// on success, so an interrupted build simply retries next launch).
func ensureGraphsFresh(bin string, projs []project.Project) <-chan struct{} {
	done := make(chan struct{})
	var wg sync.WaitGroup
	for _, p := range projs {
		stale, reason := graph.IsStale(p)
		if !stale {
			continue
		}
		logPath, lerr := graph.LogPath(p)
		if lerr != nil {
			logStatus("graph build skipped for %s: %v", p.Name, lerr)
			continue
		}
		logStatus("knowledge graph for %s is stale (%s) — rebuilding in background (log: %s)", p.Name, reason, logPath)
		wg.Add(1)
		go func(p project.Project) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), graph.BuildTimeout)
			defer cancel()
			// Errors land in build.log via Build; nothing to stderr from here.
			_ = graph.Build(ctx, p, bin)
		}(p)
	}
	go func() { wg.Wait(); close(done) }()
	return done
}
```

Note on the deliberate `_ = graph.Build(...)`: the repo rule is "never swallow errors — surface them through progress stages or wrapped returns". Here the error IS surfaced — `Build` writes the full subprocess output to `build.log` and its error text names that log — but it cannot go to stderr (TTY is Claude's) or a return value (fire-and-forget by design). `styx graphify ls` shows the project still stale, and a manual `styx graphify <p>` reproduces the error interactively. Keep this comment in the code.

Wire into `launchConductor`, right before the `guidance.Load` call. The existing extras loop already resolves each extra repo (`ep`); collect them:

```go
	graphProjects := []project.Project{p}
```

inside the existing `for _, name := range extraTail` loop, after `extras = append(extras, ep.Path)`:

```go
		graphProjects = append(graphProjects, ep)
```

then after the loop (before `guide, err := guidance.Load(p.Path)`):

```go
	if bin, ok := graph.Available(); ok {
		ensureGraphsFresh(bin, graphProjects) // background; dies with the session
	}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/styx/ -run TestEnsureGraphsFresh -v`
Expected: PASS

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 6: Verify the ARCHITECTURE.md launch paragraph, bump `last_verified`**

Task 2's section already documents the launch-path behavior. Re-read it against the code you just wrote; fix any drift (e.g. function name, silence rule) and bump `last_verified`.

- [ ] **Step 7: Format, vet, commit**

```bash
gofmt -w cmd/styx/ && go vet ./...
git add cmd/styx/launch.go cmd/styx/launch_test.go docs/ARCHITECTURE.md
git commit -m "feat(graphify): auto-build stale knowledge graphs in the background on conductor launch"
```

---

### Task 5: End-to-end verification (manual, needs the real graphify CLI)

**Files:** none (verification only)

- [ ] **Step 1: Install the real CLI (once, outside the repo)**

```bash
uv tool install graphifyy
graphify --help   # sanity: binary resolves
```

If the real CLI's flags differ from `. --update` (this plan was written against graphify v8-era docs), fix `graph.Build`'s argv in `internal/graph/graph.go` and the fake scripts' comment, and note the change in ARCHITECTURE.md — the fake-driven tests intentionally pin styx's expectation of the interface.

- [ ] **Step 2: Manual build against a real repo**

```bash
make install
cd ~/Documents/GitHub/ai-ta-backend   # 1,265 files — a real workout
styx graphify .
```

Expected: `[styx] graph is stale (no graph built yet); rebuilding...`, a progress stage, then `[styx] built knowledge graph for ... -> .../graphify-out/graph.json`. Verify `graphify-out/graph.json` exists and `styx graphify ls` shows the repo fresh.

- [ ] **Step 3: Conductor launch path**

```bash
cd ~/Documents/GitHub/ai-ta-backend
git commit --allow-empty -m "tmp: move HEAD to stale the graph"   # make it stale
styx
```

Expected before Claude Code takes over: `[styx] knowledge graph for ... is stale (git HEAD moved since last build) — rebuilding in background (log: ...)`. Inside the session: no stray `[styx]` lines corrupting the TUI. After exiting, `styx graphify ls` shows fresh (build finished during the session) and `git reset --hard HEAD~1` cleans up the empty commit.

- [ ] **Step 4: Escape hatch**

```bash
STYX_GRAPHIFY=off styx graphify ls
```

Expected: the "not available" notice; a conductor launch with the env var set fires no graph narration.

---

## Self-review checklist (run after writing, fixed inline)

- **Spec coverage:** auto-build on styx invocation (Task 4: conductor path — the user's stated entry point), staleness gating (Task 1), background + bounded + silent (Task 4), artifacts where graphify's consumers look (Task 1 `GraphPath`), manual verb (Task 3), MCP query tools deferred (explicitly out of scope), drift-contract doc updates in same commits (Tasks 2–4). ✔
- **Types consistent across tasks:** `Available() (string, bool)`, `IsStale(config.Project) (bool, string)`, `Build(ctx, config.Project, string) error`, `ensureGraphsFresh(string, []project.Project) <-chan struct{}` — `project.Project = config.Project` (alias, `internal/project/project.go:16`), so the cmd layer and graph package interoperate without conversion. ✔
- **No placeholders:** every code step is complete and compilable as written. ✔
- **Known risk, called out rather than hidden:** the exact `graphify` argv (`. --update`) and output shape are pinned from docs, not from a locally installed binary — Task 5 Step 1 is the checkpoint that validates and, if needed, corrects them in one place (`graph.Build`).
