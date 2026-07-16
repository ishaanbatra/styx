package prdraft

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/pipeline"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

func TestBuildContextFromPipelineAndGit(t *testing.T) {
	repo := t.TempDir()
	git(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "base")
	git(t, repo, "checkout", "-b", "styx/fix-auth")
	if err := os.MkdirAll(filepath.Join(repo, "internal", "auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "auth", "token.go"), []byte("package auth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-m", "fix auth token #17")

	state := pipeline.NewState("run", "fix auth token #17")
	state.Branch = "styx/fix-auth"
	state.Stages[4].Status, state.Stages[4].Attempts = pipeline.StageCompleted, 2
	state.Stages[5].Status, state.Stages[5].Attempts = pipeline.StageCompleted, 1
	ctx, err := BuildContext(context.Background(), repo, state)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Branch != state.Branch || len(ctx.Commits) != 1 || ctx.Commits[0].Subject != "fix auth token #17" {
		t.Errorf("commit context = %+v", ctx)
	}
	if !reflect.DeepEqual(ctx.TouchedPaths, []string{"internal/auth/token.go"}) {
		t.Errorf("paths = %v", ctx.TouchedPaths)
	}
	if ctx.DiffStats != (DiffStats{Files: 1, Insertions: 1}) {
		t.Errorf("stats = %+v", ctx.DiffStats)
	}
	if !reflect.DeepEqual(ctx.IssueRefs, []string{"#17"}) || !reflect.DeepEqual(ctx.RiskFlags, []string{"security-sensitive changes"}) {
		t.Errorf("issues/risks = %v / %v", ctx.IssueRefs, ctx.RiskFlags)
	}
	if !ctx.Tests.Successful || ctx.Tests.Attempts != 2 || !ctx.Review.Successful || !ctx.DraftRequired {
		t.Errorf("pipeline evidence = %+v", ctx)
	}
}

func TestTestReferencesAreGroundedAndSorted(t *testing.T) {
	got := testReferences("fix TestLogin and test_logout", []Commit{{Subject: "cover Parser_test"}}, []string{"internal/auth/token_test.go"})
	want := []string{"Parser_test", "TestLogin", "test_logout", "token_test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("test refs = %v, want %v", got, want)
	}
}

func TestDeterministicRiskAndDraftRules(t *testing.T) {
	tests := []struct {
		name         string
		paths        []string
		testAttempts int
		revAttempts  int
		wantRisk     bool
		wantDraft    bool
	}{
		{name: "ordinary", paths: []string{"internal/parser.go"}, testAttempts: 1, revAttempts: 1},
		{name: "workflow", paths: []string{".github/workflows/ci.yml"}, testAttempts: 1, revAttempts: 1, wantRisk: true, wantDraft: true},
		{name: "test fix loop", paths: []string{"internal/parser.go"}, testAttempts: 2, revAttempts: 1, wantDraft: true},
		{name: "review fix loop", paths: []string{"internal/parser.go"}, testAttempts: 1, revAttempts: 2, wantDraft: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			risks := riskFlags(tt.paths)
			draft := len(risks) > 0 || tt.testAttempts > 1 || tt.revAttempts > 1
			if (len(risks) > 0) != tt.wantRisk || draft != tt.wantDraft {
				t.Errorf("risks=%v draft=%t", risks, draft)
			}
		})
	}
}

func TestRequiresCapableModel(t *testing.T) {
	tests := []struct {
		name   string
		packet Context
		want   bool
	}{
		{name: "ordinary", packet: Context{DiffStats: DiffStats{Files: 4, Insertions: 20}}},
		{name: "risky", packet: Context{RiskFlags: []string{"security-sensitive changes"}}, want: true},
		{name: "many files", packet: Context{DiffStats: DiffStats{Files: 51}}, want: true},
		{name: "large diff", packet: Context{DiffStats: DiffStats{Insertions: 1500, Deletions: 501}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequiresCapableModel(tt.packet); got != tt.want {
				t.Errorf("RequiresCapableModel() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestContextFromStateSupportsStaticFallback(t *testing.T) {
	state := pipeline.NewState("run", "fix login #9")
	state.Branch = "styx/fix-login"
	state.Stages[4].Status, state.Stages[4].Attempts = pipeline.StageCompleted, 2
	state.Stages[5].Status, state.Stages[5].Attempts = pipeline.StageCompleted, 1

	ctx, err := ContextFromState(state)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Branch != state.Branch || !ctx.Tests.Successful || !ctx.Review.Successful || !ctx.DraftRequired {
		t.Fatalf("state-only context = %+v", ctx)
	}
	if !reflect.DeepEqual(ctx.IssueRefs, []string{"#9"}) || !reflect.DeepEqual(ctx.CoreLabels, []string{"bug"}) {
		t.Fatalf("issues/labels = %v / %v", ctx.IssueRefs, ctx.CoreLabels)
	}
}

func TestSkippedCheckStateIsNotReportedAsSuccessful(t *testing.T) {
	state := pipeline.NewState("run-skipped", "update docs")
	state.Stages[4].Status = pipeline.StageCompleted
	state.Stages[4].SkippedReason = "no test command configured"
	state.Stages[5].Status, state.Stages[5].Attempts = pipeline.StageCompleted, 1

	ctx, err := ContextFromState(state)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Tests.Successful || !ctx.Tests.Skipped || ctx.Tests.SkippedReason != "no test command configured" {
		t.Fatalf("skipped test state = %+v", ctx.Tests)
	}
	body := RenderBody(ctx, StaticBody(ctx))
	if !strings.Contains(body, "Test stage: skipped: no test command configured") {
		t.Fatalf("skipped truth missing from rendered body:\n%s", body)
	}
	if strings.Contains(body, "Test stage: completed successfully") {
		t.Fatalf("skipped test rendered as success:\n%s", body)
	}
}
