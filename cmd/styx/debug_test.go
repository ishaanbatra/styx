package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	channelagy "github.com/ishaanbatra/styx/internal/channel/agy"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
)

type debugTestChannel struct {
	name     string
	response string

	mu       sync.Mutex
	requests []channel.Request
	onSend   func()
}

func (c *debugTestChannel) Name() string { return c.name }

func (c *debugTestChannel) Send(_ context.Context, req channel.Request) (channel.Response, error) {
	if c.onSend != nil {
		c.onSend()
	}
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
			want: debugCLIArgs{bug: "cache panics", testName: "TestCache", logPaths: []string{"panic.log"}, fileHints: []string{"cache.go", "store.go:42"}},
		},
		{
			name: "log corpus supplies default triage request",
			args: []string{"--log", "unit.log", "race.log"},
			want: debugCLIArgs{bug: "failure triage from provided logs", logPaths: []string{"unit.log", "race.log"}},
		},
		{
			name: "repeatable equals form preserves trailing description",
			args: []string{"--log=unit.log", "--log=race.log", "CI", "failures"},
			want: debugCLIArgs{bug: "CI failures", logPaths: []string{"unit.log", "race.log"}},
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
		{name: "missing log value", args: []string{"--log"}, err: "--log requires a value"},
		{name: "review only conflicts with log mode", args: []string{"--review-only", "brief.md", "--log=unit.log"}, err: "cannot be combined"},
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
			if got.bug != tt.want.bug || got.testName != tt.want.testName || strings.Join(got.logPaths, "|") != strings.Join(tt.want.logPaths, "|") ||
				got.reviewOnly != tt.want.reviewOnly || strings.Join(got.fileHints, "|") != strings.Join(tt.want.fileHints, "|") {
				t.Fatalf("parsed = %+v, want %+v", got, tt.want)
			}
		})
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
	if err := cmdDebug(context.Background(), fx.a, progress.Quiet(), []string{
		"--test", "TestCache", "--file", "cache.go:42", "cache panics",
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
	for _, want := range []string{"TestCache", "cache.go:42"} {
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

func TestCmdDebugLogModePassesPathsAndSkipsClaude(t *testing.T) {
	tests := []struct {
		name        string
		externalLog bool
		wantRoots   int
	}{
		{name: "logs inside project use project mount", wantRoots: 0},
		{name: "external log adds its parent", externalLog: true, wantRoots: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newDebugCommandFixture(t)
			first := filepath.Join(fx.repo, "unit.log")
			if err := os.WriteFile(first, []byte("UNBOUNDED_LOG_SENTINEL"), 0o644); err != nil {
				t.Fatal(err)
			}
			logs := []string{first}
			if tt.externalLog {
				second := filepath.Join(t.TempDir(), "race.log")
				if err := os.WriteFile(second, []byte("SECOND_LOG_SENTINEL"), 0o644); err != nil {
					t.Fatal(err)
				}
				logs = append(logs, second)
			}
			args := append([]string{"--log"}, logs...)
			if err := cmdDebug(context.Background(), fx.a, progress.Quiet(), args); err != nil {
				t.Fatal(err)
			}

			sweepReqs, codexReqs, claudeReqs := fx.sweep.snapshot(), fx.codex.snapshot(), fx.claude.snapshot()
			if len(sweepReqs) != 1 || len(codexReqs) != 1 || len(claudeReqs) != 0 {
				t.Fatalf("request counts sweep/codex/claude = %d/%d/%d, want 1/1/0", len(sweepReqs), len(codexReqs), len(claudeReqs))
			}
			req := sweepReqs[0]
			if len(req.Attachments) != len(logs) || len(req.ExtraRoots) != tt.wantRoots {
				t.Fatalf("sweep attachments/roots = %v/%v, want %d/%d", req.Attachments, req.ExtraRoots, len(logs), tt.wantRoots)
			}
			for i, path := range logs {
				if req.Attachments[i].Path != path || !strings.Contains(req.Prompt, path) {
					t.Errorf("log %q missing from path-based request: %+v", path, req)
				}
			}
			for _, content := range []string{"UNBOUNDED_LOG_SENTINEL", "SECOND_LOG_SENTINEL"} {
				if strings.Contains(req.Prompt, content) {
					t.Errorf("sweep prompt inlined log content %q", content)
				}
			}
			if !strings.Contains(req.Prompt, "Root-cause clusters") && !strings.Contains(req.Prompt, "root cause") {
				t.Errorf("log-mode prompt missing clustering instructions:\n%s", req.Prompt)
			}
			if got := strings.Join(recordedDebugVerbs(t, fx.usagePath), ","); got != "debug.sweep,debug.review.codex" {
				t.Errorf("log-mode routed roles = %q, want debug.sweep then one Codex review", got)
			}
		})
	}
}

func recordedDebugVerbs(t *testing.T, usagePath string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", usagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT verb FROM usage WHERE verb LIKE 'debug.%' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var verbs []string
	for rows.Next() {
		var verb string
		if err := rows.Scan(&verb); err != nil {
			t.Fatal(err)
		}
		verbs = append(verbs, verb)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return verbs
}

func TestCmdDebugLogModeFakeAgentReceivesAddDirNotContent(t *testing.T) {
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	fake, err := os.ReadFile(fakeSrc)
	if err != nil {
		t.Fatal(err)
	}
	fx := newDebugCommandFixture(t)
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "agy"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)
	fx.a.channels["agy"] = channelagy.New()

	externalDir := t.TempDir()
	logPath := filepath.Join(externalDir, "ci.log")
	if err := os.WriteFile(logPath, []byte("FAKEAGENT_MUST_NOT_SEE_THIS_CONTENT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdDebug(context.Background(), fx.a, progress.Quiet(), []string{"--log", logPath}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	argv := string(got)
	for _, want := range []string{"--add-dir", fx.repo, externalDir, logPath} {
		if !strings.Contains(argv, want) {
			t.Errorf("fakeagent argv missing %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "FAKEAGENT_MUST_NOT_SEE_THIS_CONTENT") {
		t.Errorf("fakeagent argv contains inlined log content:\n%s", argv)
	}
	if len(fx.claude.snapshot()) != 0 {
		t.Fatal("log mode must not call the Claude reviewer")
	}
}

func TestCmdDebugLogModeAppliesTreeGuard(t *testing.T) {
	fx := newDebugCommandFixture(t)
	git := exec.Command("git", "init", "-q")
	git.Dir = fx.repo
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	logPath := filepath.Join(fx.repo, "ci.log")
	if err := os.WriteFile(logPath, []byte("failure"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.sweep.onSend = func() {
		if err := os.WriteFile(filepath.Join(fx.repo, "stray.txt"), []byte("agy wrote this"), 0o644); err != nil {
			t.Errorf("write simulated sweep output: %v", err)
		}
	}
	if err := cmdDebug(context.Background(), fx.a, progress.Quiet(), []string{"--log", logPath}); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(fx.repo, "styx", "debug"))
	if err != nil {
		t.Fatal(err)
	}
	var reportPath string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "-report.md") {
			reportPath = filepath.Join(fx.repo, "styx", "debug", entry.Name())
			break
		}
	}
	body, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Sweep modified the working tree", "stray.txt"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("guarded log-mode report missing %q:\n%s", want, body)
		}
	}
}

func TestResolveDebugLogPaths(t *testing.T) {
	file := filepath.Join(t.TempDir(), "test.log")
	if err := os.WriteFile(file, []byte("failure"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		paths []string
		wantN int
		err   string
	}{
		{name: "regular file", paths: []string{file}, wantN: 1},
		{name: "duplicates collapse", paths: []string{file, file}, wantN: 1},
		{name: "missing file", paths: []string{filepath.Join(t.TempDir(), "missing.log")}, err: "stat debug log"},
		{name: "directory rejected", paths: []string{t.TempDir()}, err: "not a regular file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveDebugLogPaths(tt.paths)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("want error containing %q, got %v", tt.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.wantN || !filepath.IsAbs(got[0]) {
				t.Fatalf("resolved paths = %v, want %d absolute path(s)", got, tt.wantN)
			}
		})
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
