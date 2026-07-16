package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	channelagy "github.com/ishaanbatra/styx/internal/channel/agy"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
)

type deadCodeFixture struct {
	repo      string
	usagePath string
	a         *app
	sweep     *debugTestChannel
	codex     *debugTestChannel
}

func newDeadCodeFixture(t *testing.T) deadCodeFixture {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "dead-code-project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q")
	git.Dir = repo
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "dead.go"), []byte("package sample\nfunc lonely() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	repo, err = os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	oldAlias, oldDir := globalProjectAlias, globalDirArg
	globalProjectAlias, globalDirArg = "", ""
	t.Cleanup(func() {
		globalProjectAlias, globalDirArg = oldAlias, oldDir
		_ = os.Chdir(oldWD)
	})

	usagePath := filepath.Join(t.TempDir(), "usage.db")
	tracker, err := budget.New(usagePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	routing := config.Routing{Rules: []config.Rule{{
		Verb: "dead-code", Use: "agy:Gemini 3.1 Pro (High)", Fallback: []string{"claude:sonnet", "codex"},
	}}}
	sweep := &debugTestChannel{name: "agy", response: `{"findings":[{"kind":"function","symbol":"lonely","definition":{"path":"dead.go","line":2},"reason":"no callers"}]}`}
	codex := &debugTestChannel{name: "codex", response: "UPHELD: dead.go:2 has no callers"}
	a := &app{
		routing: routing, tracker: tracker, router: router.FromConfig(routing, nil),
		channels: map[string]channel.Channel{"agy": sweep, "codex": codex},
		progress: progress.Quiet(),
	}
	return deadCodeFixture{repo: repo, usagePath: usagePath, a: a, sweep: sweep, codex: codex}
}

func TestResolveDeadCodeTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, arg, want, err string
	}{
		{name: "default", want: "."},
		{name: "subdirectory", arg: "pkg", want: "pkg"},
		{name: "unknown flag", arg: "--all", err: "unknown dead-code flag"},
		{name: "outside", arg: filepath.Dir(root), err: "outside project"},
		{name: "missing", arg: "missing", err: "resolve dead-code target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var args []string
			if tt.arg != "" {
				args = []string{tt.arg}
			}
			_, got, err := resolveDeadCodeTarget(root, args)
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("want error containing %q, got %v", tt.err, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("target = %q, err=%v; want %q", got, err, tt.want)
			}
		})
	}
	if _, _, err := resolveDeadCodeTarget(root, []string{"pkg", "extra"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("two paths should fail with usage, got %v", err)
	}
}

func TestCmdDeadCodeWritesAtomicReportAndReviewsOnce(t *testing.T) {
	fx := newDeadCodeFixture(t)
	if err := cmdDeadCode(context.Background(), fx.a, progress.Quiet(), []string{"dead.go"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(fx.repo, deadCodeArtifactDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "-report.md") {
		t.Fatalf("dead-code artifacts = %v", entries)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(fx.repo, deadCodeArtifactDir, ".styx-artifact-*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temporary artifacts left behind: %v", leftovers)
	}
	body, err := os.ReadFile(filepath.Join(fx.repo, deadCodeArtifactDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lonely — CONFIRMED", "UPHELD", "Raw agy sweep"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("report missing %q:\n%s", want, body)
		}
	}
	sweepReqs, codexReqs := fx.sweep.snapshot(), fx.codex.snapshot()
	if len(sweepReqs) != 1 || len(codexReqs) != 1 {
		t.Fatalf("request counts sweep/codex = %d/%d", len(sweepReqs), len(codexReqs))
	}
	if sweepReqs[0].Model != "Gemini 3.1 Pro (High)" || sweepReqs[0].WorkingDir != fx.repo || sweepReqs[0].Write {
		t.Errorf("sweep request = %+v", sweepReqs[0])
	}
	if codexReqs[0].WorkingDir != fx.repo || codexReqs[0].Write || !strings.Contains(codexReqs[0].Prompt, "lonely") {
		t.Errorf("codex spot-check request = %+v", codexReqs[0])
	}

	db, err := sql.Open("sqlite", fx.usagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT verb FROM usage WHERE verb LIKE 'dead-code%' ORDER BY rowid`)
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
	if strings.Join(verbs, ",") != "dead-code,dead-code.review.codex" {
		t.Errorf("recorded roles = %v", verbs)
	}
}

func TestCmdDeadCodeTreeGuard(t *testing.T) {
	fx := newDeadCodeFixture(t)
	fx.sweep.onSend = func() {
		if err := os.WriteFile(filepath.Join(fx.repo, "stray.txt"), []byte("agy wrote this"), 0o644); err != nil {
			t.Errorf("simulate agy write: %v", err)
		}
	}
	if err := cmdDeadCode(context.Background(), fx.a, progress.Quiet(), nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(fx.repo, deadCodeArtifactDir))
	body, err := os.ReadFile(filepath.Join(fx.repo, deadCodeArtifactDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Sweep modified the working tree") || !strings.Contains(string(body), "stray.txt") {
		t.Errorf("tree guard warning missing:\n%s", body)
	}
}

func TestCmdDeadCodeFakeAgentReceivesStructuredPinnedSweep(t *testing.T) {
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	fake, err := os.ReadFile(fakeSrc)
	if err != nil {
		t.Fatal(err)
	}
	fx := newDeadCodeFixture(t)
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "agy"), fake, 0o755); err != nil {
		t.Fatal(err)
	}
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)
	t.Setenv("FAKEAGENT_TEXT", "not-json")
	fx.a.channels["agy"] = channelagy.New()
	if err := cmdDeadCode(context.Background(), fx.a, progress.Quiet(), nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	argv := string(got)
	for _, want := range []string{"--model Gemini 3.1 Pro (High)", "--add-dir " + fx.repo, "Return ONLY one JSON object", `"findings"`} {
		if !strings.Contains(argv, want) {
			t.Errorf("fakeagent argv missing %q:\n%s", want, argv)
		}
	}
	if len(fx.codex.snapshot()) != 0 {
		t.Fatal("garbage sweep must not trigger a Codex spot-check")
	}
}
