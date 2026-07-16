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

type crossRepoFixture struct {
	primary   string
	extras    []string
	usagePath string
	a         *app
	sweep     *debugTestChannel
	codex     *debugTestChannel
}

func initTestGitRepo(t *testing.T, root, file, body string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), root, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", file)
	run("commit", "-qm", "initial")
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func newCrossRepoFixture(t *testing.T, extraCount int) crossRepoFixture {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	primary := initTestGitRepo(t, filepath.Join(base, "primary"), "api.go", "package api\nfunc Do() {}\n")
	extras := make([]string, 0, extraCount)
	for i := 0; i < extraCount; i++ {
		extras = append(extras, initTestGitRepo(t, filepath.Join(base, "consumer-"+string(rune('a'+i))), "client.go", "package client\nfunc Use() {}\n"))
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(primary); err != nil {
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
		Verb: "cross-repo", Use: "agy:Gemini 3.1 Pro (High)", Fallback: []string{"claude:sonnet", "codex"},
	}}}
	sweepJSON := `{"findings":[{"producer":{"root":"` + primary + `","path":"api.go","line":2,"symbol":"Do"},"consumer":{"root":"` + extras[0] + `","path":"client.go","line":2,"symbol":"Use"},"relationship":"calls","contract":"api.Do","reason":"Use invokes Do"}]}`
	sweep := &debugTestChannel{name: "agy", response: sweepJSON}
	codex := &debugTestChannel{name: "codex", response: "VERIFIED: client.go consumes api.Do"}
	a := &app{
		routing: routing, tracker: tracker, router: router.FromConfig(routing, nil),
		channels: map[string]channel.Channel{"agy": sweep, "codex": codex}, progress: progress.Quiet(),
	}
	return crossRepoFixture{primary: primary, extras: extras, usagePath: usagePath, a: a, sweep: sweep, codex: codex}
}

func TestResolveCrossRepoInput(t *testing.T) {
	fx := newCrossRepoFixture(t, 2)
	nonGit := t.TempDir()
	subdir := filepath.Join(fx.extras[0], "nested")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		args    []string
		wantQ   string
		wantN   int
		wantErr string
	}{
		{name: "one extra", args: []string{fx.extras[0]}, wantN: 2},
		{name: "multiple and question", args: []string{fx.extras[0], fx.extras[1], "--", "who", "uses", "Do?"}, wantN: 3, wantQ: "who uses Do?"},
		{name: "no extra", wantErr: "usage"},
		{name: "non git", args: []string{nonGit}, wantErr: "not a git repository"},
		{name: "subdirectory", args: []string{subdir}, wantErr: "must name the git repository root exactly"},
		{name: "duplicate", args: []string{fx.primary}, wantErr: "duplicated"},
		{name: "flag shaped", args: []string{"--all"}, wantErr: "invalid cross-repo root"},
		{name: "two separators", args: []string{fx.extras[0], "--", "q", "--"}, wantErr: "only one --"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveCrossRepoInput(context.Background(), fx.primary, tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || len(got.Roots) != tt.wantN || got.Question != tt.wantQ {
				t.Fatalf("input = %+v, err=%v", got, err)
			}
		})
	}
}

func TestSensitiveMountReason(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outsideHome := t.TempDir()
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "home", path: home, want: true},
		{name: "ssh", path: filepath.Join(home, ".ssh", "repo"), want: true},
		{name: "git metadata", path: filepath.Join(home, "work", ".git"), want: true},
		{name: "gcloud", path: filepath.Join(home, ".config", "gcloud", "repo"), want: true},
		{name: "aws outside home", path: filepath.Join(outsideHome, ".aws", "repo"), want: true},
		{name: "keyring outside home", path: filepath.Join(outsideHome, ".local", "share", "keyrings", "repo"), want: true},
		{name: "ordinary repo", path: filepath.Join(home, "work", "repo"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sensitiveMountReason(tt.path) != ""; got != tt.want {
				t.Errorf("sensitiveMountReason(%q) present=%t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

func TestResolveGitRepositoryRootRefusesSensitiveMount(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := initTestGitRepo(t, filepath.Join(home, ".ssh", "repo"), "client.go", "package client\n")

	_, err := resolveGitRepositoryRoot(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "refusing sensitive mount") {
		t.Fatalf("error = %v, want sensitive-mount refusal", err)
	}
}

func TestCmdCrossRepoWritesAtomicReportAndReviewsOnce(t *testing.T) {
	fx := newCrossRepoFixture(t, 2)
	args := []string{fx.extras[0], fx.extras[1], "--", "who", "consumes", "Do?"}
	if err := cmdCrossRepo(context.Background(), fx.a, progress.Quiet(), args); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(fx.primary, crossRepoArtifactDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), "-report.md") {
		t.Fatalf("cross-repo artifacts = %v", entries)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(fx.primary, crossRepoArtifactDir, ".styx-artifact-*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temporary artifacts left behind: %v", leftovers)
	}
	body, err := os.ReadFile(filepath.Join(fx.primary, crossRepoArtifactDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"who consumes Do?", fx.primary, fx.extras[0], fx.extras[1], "api.Do", "VERIFIED", "Machine-readable findings", "Raw agy sweep"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("report missing %q:\n%s", want, body)
		}
	}
	sweepReqs, codexReqs := fx.sweep.snapshot(), fx.codex.snapshot()
	if len(sweepReqs) != 1 || len(codexReqs) != 1 {
		t.Fatalf("request counts sweep/codex = %d/%d", len(sweepReqs), len(codexReqs))
	}
	for _, req := range []channel.Request{sweepReqs[0], codexReqs[0]} {
		if req.WorkingDir != fx.primary || req.Write || strings.Join(req.ExtraRoots, ",") != strings.Join(fx.extras, ",") {
			t.Errorf("multi-root request = %+v", req)
		}
	}
	if sweepReqs[0].Model != "Gemini 3.1 Pro (High)" || !strings.Contains(codexReqs[0].Prompt, "client.go") {
		t.Errorf("sweep/review request mismatch: %+v / %+v", sweepReqs[0], codexReqs[0])
	}

	db, err := sql.Open("sqlite", fx.usagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT verb FROM usage WHERE verb LIKE 'cross-repo%' ORDER BY rowid`)
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
	if strings.Join(verbs, ",") != "cross-repo,cross-repo.review.codex" {
		t.Errorf("recorded roles = %v", verbs)
	}
}

func TestCmdCrossRepoFakeAgentMountsAndExtraRootMutationGuard(t *testing.T) {
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	fake, err := os.ReadFile(fakeSrc)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		extraCount int
		mutate     bool
		wantErr    string
	}{
		{name: "exact two-root mount", extraCount: 1},
		{name: "exact three-root mount", extraCount: 2},
		{name: "mutated extra root aborts", extraCount: 1, mutate: true, wantErr: "refused success"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newCrossRepoFixture(t, tt.extraCount)
			binDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(binDir, "agy"), fake, 0o755); err != nil {
				t.Fatal(err)
			}
			argsLog := filepath.Join(t.TempDir(), "args.log")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)
			t.Setenv("FAKEAGENT_TEXT", "not-json")
			if tt.mutate {
				t.Setenv("FAKEAGENT_MUTATE_PATH", filepath.Join(fx.extras[0], "agy-stray.txt"))
			}
			fx.a.channels["agy"] = channelagy.New()
			err := cmdCrossRepo(context.Background(), fx.a, progress.Quiet(), append(fx.extras, "--", "find", "consumers"))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				if len(fx.codex.snapshot()) != 0 {
					t.Fatal("Codex review must not run after an extra-root mutation")
				}
				entries, readErr := os.ReadDir(filepath.Join(fx.primary, crossRepoArtifactDir))
				if readErr != nil || len(entries) != 1 {
					t.Fatalf("forensic report entries/error = %v/%v", entries, readErr)
				}
				body, _ := os.ReadFile(filepath.Join(fx.primary, crossRepoArtifactDir, entries[0].Name()))
				if !strings.Contains(string(body), "SAFETY ABORT") || !strings.Contains(string(body), fx.extras[0]) || !strings.Contains(string(body), "agy-stray.txt") {
					t.Fatalf("extra-root mutation warning missing:\n%s", body)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			argvBytes, err := os.ReadFile(argsLog)
			if err != nil {
				t.Fatal(err)
			}
			argv := string(argvBytes)
			for _, root := range append([]string{fx.primary}, fx.extras...) {
				if strings.Count(argv, "--add-dir "+root) != 1 {
					t.Errorf("root %s not mounted exactly once:\n%s", root, argv)
				}
			}
			if strings.Count(argv, "--add-dir") != len(fx.extras)+1 || !strings.Contains(argv, "--model Gemini 3.1 Pro (High)") || !strings.Contains(argv, "find consumers") {
				t.Errorf("fakeagent argv has wrong mount/model/question set:\n%s", argv)
			}
		})
	}
}
