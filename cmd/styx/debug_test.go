package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
)

type debugTestChannel struct {
	name     string
	response string

	mu       sync.Mutex
	requests []channel.Request
}

func (c *debugTestChannel) Name() string { return c.name }

func (c *debugTestChannel) Send(_ context.Context, req channel.Request) (channel.Response, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	return channel.Response{Text: c.response, EstTokensIn: 11, EstTokensOut: 7}, nil
}

func (c *debugTestChannel) BudgetState(context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

func (c *debugTestChannel) snapshot() []channel.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]channel.Request(nil), c.requests...)
}

type fixedDebugBudget struct{ used float64 }

func (b fixedDebugBudget) UsedPct(context.Context, string) (float64, error) {
	return b.used, nil
}

func TestParseDebugArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want debugCLIArgs
		err  string
	}{
		{
			name: "all flags and positional bug",
			args: []string{"--test", "TestCache", "--log=panic.log", "--file", "cache.go", "--file=store.go:42", "cache", "panics"},
			want: debugCLIArgs{bug: "cache panics", testName: "TestCache", logPath: "panic.log", fileHints: []string{"cache.go", "store.go:42"}},
		},
		{
			name: "review only supplies default description",
			args: []string{"--review-only", "brief.md"},
			want: debugCLIArgs{bug: "review-only debug diagnosis", reviewOnly: "brief.md"},
		},
		{name: "double dash", args: []string{"--", "--looks-like-a-flag"}, want: debugCLIArgs{bug: "--looks-like-a-flag"}},
		{name: "unknown flag", args: []string{"--wat"}, err: "unknown debug flag"},
		{name: "missing bug", args: nil, err: "usage: styx debug"},
		{name: "missing file value", args: []string{"--file"}, err: "--file requires a value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDebugArgs(tt.args)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("want error containing %q, got %v", tt.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.bug != tt.want.bug || got.testName != tt.want.testName || got.logPath != tt.want.logPath ||
				got.reviewOnly != tt.want.reviewOnly || strings.Join(got.fileHints, "|") != strings.Join(tt.want.fileHints, "|") {
				t.Fatalf("parsed = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestReadDebugLogCapsInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panic.log")
	body := strings.Repeat("x", maxDebugLogBytes+128)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readDebugLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != maxDebugLogBytes {
		t.Fatalf("log length = %d, want cap %d", len(got), maxDebugLogBytes)
	}
}

type debugCommandFixture struct {
	repo      string
	usagePath string
	projectID string
	a         *app
	sweep     *debugTestChannel
	codex     *debugTestChannel
	claude    *debugTestChannel
}

func newDebugCommandFixture(t *testing.T) debugCommandFixture {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	repo := filepath.Join(t.TempDir(), "debug-project")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	repo, err = os.Getwd() // normalize symlinked temp roots (for example /var -> /private/var)
	if err != nil {
		t.Fatal(err)
	}
	oldAlias, oldDir := globalProjectAlias, globalDirArg
	globalProjectAlias, globalDirArg = "", ""
	restore := func() {
		globalProjectAlias, globalDirArg = oldAlias, oldDir
		_ = os.Chdir(oldWD)
	}
	t.Cleanup(restore)

	usagePath := filepath.Join(t.TempDir(), "usage.db")
	tracker, err := budget.New(usagePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })

	routing := config.Routing{Rules: []config.Rule{
		{Verb: "debug.sweep", Use: "agy:default", Fallback: []string{"claude:sonnet"}},
		{Verb: "debug.review.codex", Use: "codex", Effort: "high"},
		{Verb: "debug.review.claude", Use: "claude:sonnet"},
	}}
	sweep := &debugTestChannel{name: "agy", response: "## Symptom\nboom\n\n## Evidence\n- cache.go:42"}
	codex := &debugTestChannel{name: "codex", response: `{"blocking":[],"important":[],"nits":[]}`}
	claude := &debugTestChannel{name: "claude", response: `{"blocking":[],"important":[],"nits":[]}`}
	a := &app{
		routing: routing,
		tracker: tracker,
		router:  router.FromConfig(routing, nil),
		channels: map[string]channel.Channel{
			"agy": sweep, "codex": codex, "claude": claude,
		},
		progress: progress.Quiet(),
	}
	return debugCommandFixture{
		repo: repo, usagePath: usagePath, projectID: config.ProjectID(repo), a: a,
		sweep: sweep, codex: codex, claude: claude,
	}
}

func TestCmdDebugWritesArtifactsAndRecordsRoleUsage(t *testing.T) {
	fx := newDebugCommandFixture(t)
	logPath := filepath.Join(fx.repo, "panic.log")
	if err := os.WriteFile(logPath, []byte("stack line"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdDebug(context.Background(), fx.a, progress.Quiet(), []string{
		"--test", "TestCache", "--log", logPath, "--file", "cache.go:42", "cache panics",
	}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(fx.repo, "styx", "debug"))
	if err != nil {
		t.Fatal(err)
	}
	var briefFound, reportFound bool
	for _, entry := range entries {
		briefFound = briefFound || strings.HasSuffix(entry.Name(), "-brief.md")
		reportFound = reportFound || strings.HasSuffix(entry.Name(), "-report.md")
	}
	if !briefFound || !reportFound {
		t.Fatalf("debug artifacts = %v; want brief and report", entries)
	}

	sweepReqs, codexReqs, claudeReqs := fx.sweep.snapshot(), fx.codex.snapshot(), fx.claude.snapshot()
	if len(sweepReqs) != 1 || len(codexReqs) != 1 || len(claudeReqs) != 1 {
		t.Fatalf("request counts sweep/codex/claude = %d/%d/%d", len(sweepReqs), len(codexReqs), len(claudeReqs))
	}
	if sweepReqs[0].WorkingDir != fx.repo || sweepReqs[0].Write {
		t.Errorf("sweep request must attach repo read-only: %+v", sweepReqs[0])
	}
	if codexReqs[0].WorkingDir != "" || claudeReqs[0].WorkingDir != "" || codexReqs[0].Write || claudeReqs[0].Write {
		t.Errorf("review requests must be brief-only and read-only: codex=%+v claude=%+v", codexReqs[0], claudeReqs[0])
	}
	for _, want := range []string{"TestCache", "stack line", "cache.go:42"} {
		if !strings.Contains(sweepReqs[0].Prompt, want) {
			t.Errorf("sweep prompt missing %q", want)
		}
	}
	if codexReqs[0].Effort != "high" {
		t.Errorf("codex effort = %q, want high", codexReqs[0].Effort)
	}

	db, err := sql.Open("sqlite", fx.usagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT verb, project, run_id FROM usage WHERE verb LIKE 'debug.%' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var verbs []string
	var runID string
	for rows.Next() {
		var verb, projectID, gotRunID string
		if err := rows.Scan(&verb, &projectID, &gotRunID); err != nil {
			t.Fatal(err)
		}
		if projectID != fx.projectID || gotRunID == "" {
			t.Errorf("usage correlation project/run = %q/%q", projectID, gotRunID)
		}
		if runID == "" {
			runID = gotRunID
		} else if gotRunID != runID {
			t.Errorf("usage run IDs differ: %q vs %q", runID, gotRunID)
		}
		verbs = append(verbs, verb)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(verbs, ",") != "debug.sweep,debug.review.codex,debug.review.claude" &&
		strings.Join(verbs, ",") != "debug.sweep,debug.review.claude,debug.review.codex" {
		t.Fatalf("recorded debug verbs = %v", verbs)
	}
}

func TestCmdDebugRefusesBlockedSweep(t *testing.T) {
	fx := newDebugCommandFixture(t)
	fx.a.router.Budget = fixedDebugBudget{used: 100}
	fx.a.router.Caps = config.BudgetCaps{
		Agy: config.ChannelCap{CapPct: 1}, Claude: config.ChannelCap{CapPct: 1},
	}
	err := cmdDebug(context.Background(), fx.a, progress.Quiet(), []string{"panic in cache"})
	if err == nil || !strings.Contains(err.Error(), "blocked by budget or circuit state") || !strings.Contains(err.Error(), "--review-only") {
		t.Fatalf("want loud budget refusal, got %v", err)
	}
	if len(fx.sweep.snapshot()) != 0 {
		t.Fatal("blocked sweep must not call a channel")
	}
}

func TestTreeStateDiff(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pre, post string
		want      []string
	}{
		{name: "identical states", pre: "?? a.txt\n", post: "?? a.txt\n", want: nil},
		{name: "sweep adds a file", pre: "", post: "?? stray.txt\n", want: []string{"?? stray.txt"}},
		{name: "sweep edits a tracked file", pre: "?? a.txt\n", post: "?? a.txt\n M internal/a.go\n", want: []string{"M internal/a.go"}},
		{name: "pre-existing dirt is ignored", pre: " M internal/a.go\n?? a.txt\n", post: " M internal/a.go\n?? a.txt\n", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := treeStateDiff(tc.pre, tc.post)
			if len(got) != len(tc.want) {
				t.Fatalf("treeStateDiff = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("treeStateDiff = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestGitTreeStateSkipsNonGitDir(t *testing.T) {
	if _, err := gitTreeState(context.Background(), t.TempDir()); err == nil {
		t.Fatal("non-git dir must return an error so the tree guard is skipped")
	}
}
