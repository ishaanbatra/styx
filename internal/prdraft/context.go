// Package prdraft builds, validates, and renders deterministic pull-request
// drafts from pipeline and git evidence plus bounded model prose.
package prdraft

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ishaanbatra/styx/internal/pipeline"
)

// Commit is one commit included in the pull request.
type Commit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

// DiffStats are deterministic aggregate line counts from git diff --numstat.
type DiffStats struct {
	Files      int `json:"files"`
	Insertions int `json:"insertions"`
	Deletions  int `json:"deletions"`
}

// CheckState records pipeline-owned truth. Models receive this as evidence but
// cannot emit or override these fields in their response schemas.
type CheckState struct {
	Successful    bool   `json:"successful"`
	Skipped       bool   `json:"skipped"`
	SkippedReason string `json:"skipped_reason,omitempty"`
	Attempts      int    `json:"attempts"`
}

// Context is the deterministic packet supplied to both PR prose tasks.
type Context struct {
	Goal          string     `json:"goal"`
	Branch        string     `json:"branch"`
	BaseBranch    string     `json:"base_branch"`
	Commits       []Commit   `json:"commits"`
	TouchedPaths  []string   `json:"touched_paths"`
	DiffStats     DiffStats  `json:"diff_stats"`
	Tests         CheckState `json:"tests"`
	Review        CheckState `json:"review"`
	IssueRefs     []string   `json:"issue_refs"`
	TestRefs      []string   `json:"test_refs"`
	RiskFlags     []string   `json:"risk_flags"`
	CoreLabels    []string   `json:"core_labels"`
	AllowedLabels []string   `json:"allowed_labels"`
	DraftRequired bool       `json:"draft_required"`
}

var issueRE = regexp.MustCompile(`#([1-9][0-9]*)`)

// LabelAllowlist is deliberately static; no gh label discovery or cache is
// involved in PR drafting.
var LabelAllowlist = []string{
	"bug", "ci", "database", "dependencies", "documentation",
	"enhancement", "security", "tests",
}

// BuildContext snapshots the current branch diff relative to the repository's
// default branch and combines it with persisted pipeline state.
func BuildContext(ctx context.Context, projectPath string, state *pipeline.State) (Context, error) {
	return buildContext(ctx, projectPath, state, defaultBranch(ctx, projectPath))
}

// BuildContextWithBase snapshots the current branch diff relative to baseBranch
// and combines it with persisted pipeline state.
func BuildContextWithBase(ctx context.Context, projectPath string, state *pipeline.State, baseBranch string) (Context, error) {
	baseBranch = strings.TrimSpace(baseBranch)
	if baseBranch == "" {
		return Context{}, fmt.Errorf("build PR context: base branch is required")
	}
	return buildContext(ctx, projectPath, state, baseBranch)
}

func buildContext(ctx context.Context, projectPath string, state *pipeline.State, base string) (Context, error) {
	packet, err := ContextFromState(state)
	if err != nil {
		return Context{}, err
	}
	packet.BaseBranch = base
	rangeSpec := base + "...HEAD"
	logOut, err := gitOutput(ctx, projectPath, "log", "--format=%H%x09%s", rangeSpec)
	if err != nil {
		return Context{}, fmt.Errorf("build PR context commits: %w", err)
	}
	pathsOut, err := gitOutput(ctx, projectPath, "diff", "--name-only", rangeSpec, "--")
	if err != nil {
		return Context{}, fmt.Errorf("build PR context paths: %w", err)
	}
	numstat, err := gitOutput(ctx, projectPath, "diff", "--numstat", rangeSpec, "--")
	if err != nil {
		return Context{}, fmt.Errorf("build PR context stats: %w", err)
	}

	packet.Commits = parseCommits(logOut)
	packet.TouchedPaths = nonemptyLines(pathsOut)
	packet.DiffStats = parseNumstat(numstat)
	packet.IssueRefs = explicitIssues(state.Goal, packet.Commits)
	packet.TestRefs = testReferences(state.Goal, packet.Commits, packet.TouchedPaths)
	packet.RiskFlags = riskFlags(packet.TouchedPaths)
	packet.CoreLabels = coreLabels(state.Goal, packet.TouchedPaths)
	packet.DraftRequired = len(packet.RiskFlags) > 0 || packet.Tests.Attempts > 1 || packet.Review.Attempts > 1
	return packet, nil
}

// RequiresCapableModel raises drafting above the local tier when the packet is
// security/migration/dependency sensitive or unusually large. The thresholds
// use deterministic diff metadata, never model judgment.
func RequiresCapableModel(packet Context) bool {
	return len(packet.RiskFlags) > 0 || packet.DiffStats.Files > 50 ||
		packet.DiffStats.Insertions+packet.DiffStats.Deletions > 2000
}

// ContextFromState builds the deterministic state-only packet used when git
// evidence cannot be collected. Callers can still render static prose without
// letting a drafting failure strand publication.
func ContextFromState(state *pipeline.State) (Context, error) {
	if state == nil {
		return Context{}, fmt.Errorf("build PR context: nil pipeline state")
	}
	packet := Context{
		Goal: state.Goal, Branch: state.Branch,
		AllowedLabels: append([]string(nil), LabelAllowlist...),
		Tests:         checkState(state, "test"), Review: checkState(state, "review"),
	}
	packet.IssueRefs = explicitIssues(state.Goal, nil)
	packet.TestRefs = testReferences(state.Goal, nil, nil)
	packet.CoreLabels = coreLabels(state.Goal, nil)
	packet.DraftRequired = packet.Tests.Attempts > 1 || packet.Review.Attempts > 1
	return packet, nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v (%s)", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// DefaultBranch exposes the same default-branch detection used by BuildContext.
func DefaultBranch(ctx context.Context, repo string) string {
	return defaultBranch(ctx, repo)
}

func defaultBranch(ctx context.Context, repo string) string {
	if out, err := gitOutput(ctx, repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if _, branch, ok := strings.Cut(strings.TrimSpace(out), "/"); ok && branch != "" {
			return branch
		}
	}
	for _, branch := range []string{"main", "master", "trunk", "dev"} {
		cmd := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", branch)
		cmd.Dir = repo
		if cmd.Run() == nil {
			return branch
		}
	}
	return "main"
}

func parseCommits(s string) []Commit {
	var commits []Commit
	for _, line := range nonemptyLines(s) {
		sha, subject, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if len(sha) > 12 {
			sha = sha[:12]
		}
		commits = append(commits, Commit{SHA: sha, Subject: subject})
	}
	return commits
}

func nonemptyLines(s string) []string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, filepath.ToSlash(line))
		}
	}
	return lines
}

func parseNumstat(s string) DiffStats {
	var stats DiffStats
	for _, line := range nonemptyLines(s) {
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		stats.Files++
		if n, err := strconv.Atoi(fields[0]); err == nil {
			stats.Insertions += n
		}
		if n, err := strconv.Atoi(fields[1]); err == nil {
			stats.Deletions += n
		}
	}
	return stats
}

func checkState(state *pipeline.State, name string) CheckState {
	for _, stage := range state.Stages {
		if stage.Name == name {
			skipped := stage.SkippedReason != "" || stage.Status == pipeline.StageSkipped
			return CheckState{
				Successful:    stage.Status == pipeline.StageCompleted && !skipped,
				Skipped:       skipped,
				SkippedReason: stage.SkippedReason,
				Attempts:      stage.Attempts,
			}
		}
	}
	return CheckState{}
}

func explicitIssues(goal string, commits []Commit) []string {
	text := goal
	for _, commit := range commits {
		text += "\n" + commit.Subject
	}
	seen := map[string]bool{}
	var refs []string
	for _, match := range issueRE.FindAllStringSubmatch(text, -1) {
		ref := "#" + match[1]
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(refs[i], "#"))
		b, _ := strconv.Atoi(strings.TrimPrefix(refs[j], "#"))
		return a < b
	})
	return refs
}

var testRefRE = regexp.MustCompile(`\b(?:Test[A-Z][A-Za-z0-9_]*|test_[A-Za-z0-9_]+|[A-Za-z0-9_]+_test)\b`)

func testReferences(goal string, commits []Commit, paths []string) []string {
	text := goal + "\n" + strings.Join(paths, "\n")
	for _, commit := range commits {
		text += "\n" + commit.Subject
	}
	seen := map[string]bool{}
	var refs []string
	for _, ref := range testRefRE.FindAllString(text, -1) {
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}

func riskFlags(paths []string) []string {
	flags := map[string]bool{}
	for _, path := range paths {
		p := strings.ToLower(filepath.ToSlash(path))
		switch {
		case strings.HasPrefix(p, ".github/workflows/") || strings.Contains(p, "/ci/"):
			flags["ci/workflow changes"] = true
		case strings.Contains(p, "migration") || strings.Contains(p, "schema"):
			flags["database/schema changes"] = true
		case strings.Contains(p, "auth") || strings.Contains(p, "security") || strings.Contains(p, "permission"):
			flags["security-sensitive changes"] = true
		case p == "go.mod" || p == "go.sum" || strings.HasSuffix(p, "package-lock.json") || strings.HasSuffix(p, "requirements.txt"):
			flags["dependency changes"] = true
		}
	}
	return sortedKeys(flags)
}

func coreLabels(goal string, paths []string) []string {
	labels := map[string]bool{}
	lowerGoal := strings.ToLower(goal)
	if strings.Contains(lowerGoal, "fix") || strings.Contains(lowerGoal, "bug") {
		labels["bug"] = true
	} else {
		labels["enhancement"] = true
	}
	for _, path := range paths {
		p := strings.ToLower(filepath.ToSlash(path))
		switch {
		case strings.HasSuffix(p, "_test.go") || strings.Contains(p, "/test"):
			labels["tests"] = true
		case strings.HasSuffix(p, ".md") || strings.HasPrefix(p, "docs/"):
			labels["documentation"] = true
		case strings.HasPrefix(p, ".github/workflows/"):
			labels["ci"] = true
		case strings.Contains(p, "migration") || strings.Contains(p, "schema"):
			labels["database"] = true
		case strings.Contains(p, "auth") || strings.Contains(p, "security"):
			labels["security"] = true
		case p == "go.mod" || p == "go.sum" || strings.HasSuffix(p, "package-lock.json") || strings.HasSuffix(p, "requirements.txt"):
			labels["dependencies"] = true
		}
	}
	return sortedKeys(labels)
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
