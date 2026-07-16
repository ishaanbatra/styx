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

const mapImpactSweepResponse = `{"findings":[{"source":{"path":"impact.go","line":2,"symbol":"Changed"},"dependent":{"path":"user.go","line":2,"symbol":"Use"},"relationship":"calls","impact":"direct","reason":"Use calls Changed"}]}`

type mapImpactFixture struct {
	repo      string
	usagePath string
	a         *app
	sweep     *debugTestChannel
	codex     *debugTestChannel
}

func newMapImpactFixture(t *testing.T) mapImpactFixture {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(t.TempDir(), "map-impact-project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	runGit("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "impact.go"), []byte("package sample\nfunc Changed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "impact.go")
	runGit("commit", "-qm", "initial")
	if err := os.WriteFile(filepath.Join(repo, "user.go"), []byte("package sample\nfunc Use() { Changed() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "user.go")
	runGit("commit", "-qm", "dependent")

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
		Verb: "map-impact", Use: "agy:Gemini 3.1 Pro (High)", Fallback: []string{"claude:sonnet", "codex"},
	}}}
	sweep := &debugTestChannel{name: "agy", response: mapImpactSweepResponse}
	codex := &debugTestChannel{name: "codex", response: "VERIFIED: user.go:2 calls Changed"}
	a := &app{
		routing: routing, tracker: tracker, router: router.FromConfig(routing, nil),
		channels: map[string]channel.Channel{"agy": sweep, "codex": codex},
		progress: progress.Quiet(),
	}
	return mapImpactFixture{repo: repo, usagePath: usagePath, a: a, sweep: sweep, codex: codex}
}

func TestResolveMapImpactInput(t *testing.T) {
	fx := newMapImpactFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.go")
	if err := os.WriteFile(outside, []byte("package outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, arg, wantKind, wantValue, wantErr string
	}{
		{name: "symbol", arg: "Changed", wantKind: "symbol", wantValue: "Changed"},
		{name: "file", arg: "impact.go", wantKind: "file", wantValue: "impact.go"},
		{name: "diff ref", arg: "HEAD~1", wantKind: "diff", wantValue: "HEAD~1"},
		{name: "diff range", arg: "HEAD~1..HEAD", wantKind: "diff", wantValue: "HEAD~1..HEAD"},
		{name: "directory", arg: ".", wantErr: "not a regular file"},
		{name: "outside file", arg: outside, wantErr: "outside project"},
		{name: "unknown flag", arg: "--all", wantErr: "unknown map-impact flag"},
		{name: "empty", arg: " ", wantErr: "must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveMapImpactInput(context.Background(), fx.repo, []string{tt.arg})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil || got.Kind != tt.wantKind || got.Value != tt.wantValue {
				t.Fatalf("input = %+v, err=%v; want %s/%s", got, err, tt.wantKind, tt.wantValue)
			}
		})
	}
	for _, args := range [][]string{nil, {"one", "two"}} {
		if _, err := resolveMapImpactInput(context.Background(), fx.repo, args); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("args %v should fail with usage, got %v", args, err)
		}
	}
}

func TestCmdMapImpactWritesAtomicReportAndReviewsOnce(t *testing.T) {
	fx := newMapImpactFixture(t)
	if err := cmdMapImpact(context.Background(), fx.a, progress.Quiet(), []string{"Changed"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(fx.repo, mapImpactArtifactDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "-report.md") {
		t.Fatalf("map-impact artifacts = %v", entries)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(fx.repo, mapImpactArtifactDir, ".styx-artifact-*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temporary artifacts left behind: %v", leftovers)
	}
	body, err := os.ReadFile(filepath.Join(fx.repo, mapImpactArtifactDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Input kind: symbol", "Changed", "user.go:2", "VERIFIED", "Machine-readable findings", `"findings": [`, "Raw agy sweep"} {
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
	if codexReqs[0].WorkingDir != fx.repo || codexReqs[0].Write || !strings.Contains(codexReqs[0].Prompt, "user.go") {
		t.Errorf("codex spot-check request = %+v", codexReqs[0])
	}

	db, err := sql.Open("sqlite", fx.usagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT verb FROM usage WHERE verb LIKE 'map-impact%' ORDER BY rowid`)
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
	if strings.Join(verbs, ",") != "map-impact,map-impact.review.codex" {
		t.Errorf("recorded roles = %v", verbs)
	}
}

func TestCmdMapImpactTreeGuard(t *testing.T) {
	fx := newMapImpactFixture(t)
	fx.sweep.onSend = func() {
		if err := os.WriteFile(filepath.Join(fx.repo, "stray.txt"), []byte("agy wrote this"), 0o644); err != nil {
			t.Errorf("simulate agy write: %v", err)
		}
	}
	if err := cmdMapImpact(context.Background(), fx.a, progress.Quiet(), []string{"Changed"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(fx.repo, mapImpactArtifactDir))
	body, err := os.ReadFile(filepath.Join(fx.repo, mapImpactArtifactDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Sweep modified the working tree") || !strings.Contains(string(body), "stray.txt") {
		t.Errorf("tree guard warning missing:\n%s", body)
	}
}

func TestCmdMapImpactFakeAgentInputs(t *testing.T) {
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	fake, err := os.ReadFile(fakeSrc)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, input, wantJSON string
	}{
		{name: "symbol", input: "Changed", wantJSON: `{"kind":"symbol","value":"Changed"}`},
		{name: "file", input: "impact.go", wantJSON: `{"kind":"file","value":"impact.go"}`},
		{name: "diff", input: "HEAD~1", wantJSON: `{"kind":"diff","value":"HEAD~1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newMapImpactFixture(t)
			binDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(binDir, "agy"), fake, 0o755); err != nil {
				t.Fatal(err)
			}
			argsLog := filepath.Join(t.TempDir(), "args.log")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)
			t.Setenv("FAKEAGENT_TEXT", "not-json")
			fx.a.channels["agy"] = channelagy.New()
			if err := cmdMapImpact(context.Background(), fx.a, progress.Quiet(), []string{tt.input}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(argsLog)
			if err != nil {
				t.Fatal(err)
			}
			argv := string(got)
			for _, want := range []string{"--model Gemini 3.1 Pro (High)", "--add-dir " + fx.repo, "Return ONLY one JSON object", `"findings"`, tt.wantJSON} {
				if !strings.Contains(argv, want) {
					t.Errorf("fakeagent argv missing %q:\n%s", want, argv)
				}
			}
			if len(fx.codex.snapshot()) != 0 {
				t.Fatal("garbage sweep must not trigger a Codex spot-check")
			}
		})
	}
}
