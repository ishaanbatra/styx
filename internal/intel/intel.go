package intel

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/progress"
)

const SchemaVersion = 1

// MaxCommitsBeforeStale and MaxAgeBeforeStale govern auto-refresh policy.
const (
	MaxCommitsBeforeStale = 5
	MaxAgeBeforeStale     = 7 * 24 * time.Hour
)

// Per-call timeouts for agy invocations during intel build. Without these,
// a hung agy call has no way to be cancelled via Ctrl-C.
const (
	ModuleSummaryTimeout = 2 * time.Minute
	KeySymbolsTimeout    = 3 * time.Minute
)

// Index is the persisted codebase intelligence record.
type Index struct {
	Project           string       `json:"project"`
	Path              string       `json:"path"`
	Language          string       `json:"language"`
	BuiltAt           time.Time    `json:"built_at"`
	GitHead           string       `json:"git_head"`
	CommitsSinceBuild int          `json:"git_commits_since_build"`
	SchemaVersion     int          `json:"schema_version"`
	FileTree          []string     `json:"file_tree"`
	Modules           []Module     `json:"modules,omitempty"`
	Conventions       Conventions  `json:"conventions"`
	KeySymbols        []KeySymbol  `json:"key_symbols,omitempty"`
	RecentCommits     []Commit     `json:"recent_commits,omitempty"`
	OpenTodos         []Todo       `json:"open_todos,omitempty"`
	ExternalDeps      ExternalDeps `json:"external_deps,omitempty"`
}

type Module struct {
	Path        string   `json:"path"`
	Purpose     string   `json:"purpose"`
	EntryPoints []string `json:"entry_points,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
}

type KeySymbol struct {
	Name string `json:"name"`
	File string `json:"file"`
	Why  string `json:"why"`
}

type Commit struct {
	SHA          string `json:"sha"`
	Subject      string `json:"subject"`
	FilesTouched int    `json:"files_touched"`
}

type Todo struct {
	File string `json:"file"`
	Text string `json:"text"`
}

type ExternalDeps struct {
	Key                     []string `json:"key,omitempty"`
	VersionPinsLikelyIntent []string `json:"version_pins_likely_intentional,omitempty"`
}

// AgyClient is the narrow interface the intel package needs from agy.
// Production code passes a real agy.Channel-backed adapter; tests pass a stub.
type AgyClient interface {
	Send(ctx context.Context, prompt, workingDir string) (string, error)
}

// indexDir returns ~/.config/styx/state/intel/<project-id>/
func indexDir(proj config.Project) (string, error) {
	state, err := paths.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "intel", proj.ID), nil
}

func indexPath(proj config.Project) (string, error) {
	d, err := indexDir(proj)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "index.json"), nil
}

// Build runs the full index build pipeline: walk, sniff, recent commits, todos,
// external deps, agy module summaries, agy key symbols. Atomic-writes the result.
func Build(ctx context.Context, proj config.Project, agy AgyClient, prog *progress.Tracker) (*Index, error) {
	if prog == nil {
		prog = progress.Quiet()
	}

	st := prog.Stage("Walking files in " + proj.Path)
	files, err := Walk(proj.Path)
	if err != nil {
		st.Fail(err)
		return nil, fmt.Errorf("walk: %w", err)
	}
	st.Done("%d files", len(files))

	st2 := prog.Stage("Sniffing conventions")
	conv := Sniff(proj.Path)
	st2.Done("test=%s, types=%s", conv.TestFramework, conv.TypeSystem)

	idx := &Index{
		Project:       proj.Name,
		Path:          proj.Path,
		Language:      proj.Language,
		BuiltAt:       time.Now().UTC(),
		SchemaVersion: SchemaVersion,
		FileTree:      files,
		Conventions:   conv,
		RecentCommits: recentCommits(proj.Path),
		OpenTodos:     openTodos(proj.Path),
		ExternalDeps:  externalDeps(proj.Path),
		GitHead:       gitHead(proj.Path),
	}

	idx.Modules = buildModuleSummaries(ctx, proj.Path, agy, prog)
	idx.KeySymbols = buildKeySymbols(ctx, proj.Path, agy, prog)

	if err := Save(proj, idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// Save atomically writes idx to ~/.config/styx/state/intel/<id>/index.json
func Save(proj config.Project, idx *Index) error {
	d, err := indexDir(proj)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		return err
	}
	p := filepath.Join(d, "index.json")
	tmp := p + ".tmp"
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Load reads the cached index.
func Load(proj config.Project) (*Index, error) {
	p, err := indexPath(proj)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, fmt.Errorf("parse index: %w", err)
	}
	return &idx, nil
}

// IsStale returns true (and a human reason) if the cached index needs rebuilding.
func IsStale(proj config.Project) (bool, string, error) {
	idx, err := Load(proj)
	if err != nil {
		if stderrors.Is(err, os.ErrNotExist) {
			return true, "no index built yet", nil
		}
		return true, "load failed: " + err.Error(), nil
	}
	if time.Since(idx.BuiltAt) > MaxAgeBeforeStale {
		return true, fmt.Sprintf("index is %d days old", int(time.Since(idx.BuiltAt).Hours()/24)), nil
	}
	n := commitsSince(proj.Path, idx.GitHead)
	if n > MaxCommitsBeforeStale {
		return true, fmt.Sprintf("%d commits since build (max %d)", n, MaxCommitsBeforeStale), nil
	}
	return false, "", nil
}

func gitHead(repo string) string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func commitsSince(repo, sha string) int {
	if sha == "" {
		return 0
	}
	cmd := exec.Command("git", "rev-list", "--count", sha+"..HEAD")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		// Bias toward "stale" rather than silently "fresh" on git failure.
		return math.MaxInt32
	}
	n, parseErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if parseErr != nil {
		return math.MaxInt32
	}
	return n
}

func recentCommits(repo string) []Commit {
	cmd := exec.Command("git", "log", "-n", "20", "--pretty=format:%H|%s")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "|", 2)
		if len(parts) != 2 {
			continue
		}
		commits = append(commits, Commit{SHA: parts[0][:7], Subject: parts[1]})
	}
	return commits
}

func openTodos(repo string) []Todo {
	cmd := exec.Command("git", "grep", "-nE", `TODO|FIXME|HACK`)
	cmd.Dir = repo
	out, _ := cmd.Output()
	var todos []Todo
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if i >= 30 || line == "" {
			break
		}
		// format: <file>:<lineno>:<text>
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		todos = append(todos, Todo{File: parts[0] + ":" + parts[1], Text: strings.TrimSpace(parts[2])})
	}
	return todos
}

func externalDeps(repo string) ExternalDeps {
	out := ExternalDeps{}
	if b, err := os.ReadFile(filepath.Join(repo, "go.mod")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "require ") {
				parts := strings.Fields(strings.TrimPrefix(line, "require "))
				if len(parts) >= 1 {
					out.Key = append(out.Key, parts[0])
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(repo, "package.json")); err == nil {
		// crude: just record first 10 dep names
		s := string(b)
		const marker = `"dependencies":`
		if idx := strings.Index(s, marker); idx >= 0 {
			block := s[idx:]
			if end := strings.Index(block, "}"); end > 0 {
				for _, line := range strings.Split(block[:end], ",") {
					line = strings.TrimSpace(line)
					if i := strings.Index(line, "\""); i >= 0 {
						rest := line[i+1:]
						if j := strings.Index(rest, "\""); j > 0 {
							out.Key = append(out.Key, rest[:j])
						}
					}
					if len(out.Key) >= 10 {
						break
					}
				}
			}
		}
	}
	return out
}

func buildModuleSummaries(ctx context.Context, repo string, agy AgyClient, prog *progress.Tracker) []Module {
	entries, err := os.ReadDir(repo)
	if err != nil {
		return nil
	}
	// Collect qualifying top-level directories first so we can show i/n progress.
	var dirs []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() || builtinExcludes[e.Name()] || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirs = append(dirs, e)
	}
	var mods []Module
	for i, e := range dirs {
		st := prog.Stage(fmt.Sprintf("Summarizing module %d/%d: %s", i+1, len(dirs), e.Name()))
		modPath := filepath.Join(repo, e.Name())
		prompt := fmt.Sprintf("Summarize the purpose, entry points, and dependencies of this module in <=120 words. Module dir: %s", e.Name())
		cctx, cancel := context.WithTimeout(ctx, ModuleSummaryTimeout)
		resp, err := agy.Send(cctx, prompt, modPath)
		cancel()
		if err != nil {
			st.Fail(err)
			mods = append(mods, Module{Path: e.Name(), Purpose: "(agy unavailable: " + err.Error() + ")"})
			continue
		}
		st.Done("done")
		mods = append(mods, Module{Path: e.Name(), Purpose: resp})
	}
	return mods
}

func buildKeySymbols(ctx context.Context, repo string, agy AgyClient, prog *progress.Tracker) []KeySymbol {
	st := prog.Stage("Extracting key symbols")
	prompt := "List the 10-15 most central types, functions, or services in this codebase, formatted as: <Name> (<file:line>) - <one-line why this is central>. Return one per line."
	cctx, cancel := context.WithTimeout(ctx, KeySymbolsTimeout)
	defer cancel()
	resp, err := agy.Send(cctx, prompt, repo)
	if err != nil {
		st.Fail(err)
		return nil
	}
	var syms []KeySymbol
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// crude parse: "<Name> (<file>) - <why>"
		open := strings.Index(line, "(")
		closeP := strings.Index(line, ")")
		dash := strings.Index(line, " - ")
		if open <= 0 || closeP <= open || dash <= closeP {
			continue
		}
		syms = append(syms, KeySymbol{
			Name: strings.TrimSpace(line[:open]),
			File: strings.TrimSpace(line[open+1 : closeP]),
			Why:  strings.TrimSpace(line[dash+3:]),
		})
		if len(syms) >= 15 {
			break
		}
	}
	st.Done("%d symbols", len(syms))
	return syms
}
