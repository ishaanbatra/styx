package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/prdraft"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

type prDraftTestChannel struct {
	respond func(channel.Request) channel.Response
	calls   int
}

type prDraftBudget map[string]float64

func (b prDraftBudget) UsedPct(_ context.Context, channel string) (float64, error) {
	return b[channel], nil
}

type prDraftBreaker map[string]bool

func (b prDraftBreaker) Broken(_ context.Context, channel string) bool { return b[channel] }

func (c *prDraftTestChannel) Name() string { return "pr-draft-test" }
func (c *prDraftTestChannel) BudgetState(context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}
func (c *prDraftTestChannel) Send(_ context.Context, req channel.Request) (channel.Response, error) {
	c.calls++
	return c.respond(req), nil
}

func prDraftRouter() *router.Router {
	return router.FromConfig(config.Routing{Rules: []config.Rule{
		{Verb: "pr.title", Signals: []string{"complex"}, Use: "claude:sonnet", Fallback: []string{"codex"}},
		{Verb: "pr.title", Use: "ollama:local-small", Fallback: []string{"claude:haiku"}},
		{Verb: "pr.body", Signals: []string{"complex"}, Use: "claude:sonnet", Fallback: []string{"codex"}},
		{Verb: "pr.body", Use: "ollama:local-small", Fallback: []string{"claude:haiku"}},
	}}, nil)
}

func TestBuildRunnerNoPublishFlagsSkipDraftModels(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	model := &prDraftTestChannel{respond: func(channel.Request) channel.Response {
		return channel.Response{Text: `{}`}
	}}
	a := &app{
		router: prDraftRouter(),
		channels: map[string]channel.Channel{
			"ollama": model,
			"claude": model,
		},
	}
	proj := project.Project{ID: "p1", Path: t.TempDir(), Language: "go"}
	tests := []struct {
		name       string
		noPR       bool
		noPush     bool
		wantPushed bool
	}{
		{name: "no pr", noPR: true, wantPushed: true},
		{name: "no push", noPush: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model.calls = 0
			runner := buildRunner(a, proj, "run", "fix parser", false, tt.noPR, tt.noPush)
			_, pushed, err := runner.RunShip(context.Background(), runner)
			if err != nil {
				t.Fatal(err)
			}
			if pushed != tt.wantPushed {
				t.Errorf("pushed = %t, want %t", pushed, tt.wantPushed)
			}
			if model.calls != 0 {
				t.Fatalf("drafting model called %d time(s)", model.calls)
			}
		})
	}
}

func TestDraftPullRequestFallsBackAndRecordsAttempts(t *testing.T) {
	repo := initPRDraftRepo(t)
	state := pipeline.NewState("run-17", "fix parser #17")
	state.Branch = "styx/fix-parser"
	state.Stages[4].Status, state.Stages[4].Attempts = pipeline.StageCompleted, 2
	state.Stages[5].Status, state.Stages[5].Attempts = pipeline.StageCompleted, 1

	local := &prDraftTestChannel{respond: func(req channel.Request) channel.Response {
		if strings.Contains(req.Prompt, "pull-request title") {
			return channel.Response{Text: `{"title":"All tests passed"}`, EstTokensIn: 11, EstTokensOut: 3}
		}
		return channel.Response{
			Text:        `{"summary_bullets":["Checks are green"],"test_plan_bullets":["Exercise auth"],"reviewer_checklist":["Inspect auth"],"release_note":"","label_suggestions":[]}`,
			EstTokensIn: 21, EstTokensOut: 7,
		}
	}}
	cloud := &prDraftTestChannel{respond: func(req channel.Request) channel.Response {
		if strings.Contains(req.Prompt, "pull-request title") {
			return channel.Response{Text: `{"title":"Fix parser handling for #17"}`, EstTokensIn: 12, EstTokensOut: 4}
		}
		return channel.Response{
			Text:        `{"summary_bullets":["Update ` + "`internal/parser.go`" + ` for #17"],"test_plan_bullets":["Exercise parser edge cases"],"reviewer_checklist":["Inspect parser boundaries"],"release_note":"Parser handling is safer.","label_suggestions":["tests"]}`,
			EstTokensIn: 22, EstTokensOut: 8,
		}
	}}
	tracker, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	a := &app{
		router: prDraftRouter(), tracker: tracker,
		channels: map[string]channel.Channel{"ollama": local, "claude": cloud},
	}
	proj := project.Project{ID: "project-1", Path: repo, Language: "go"}

	draft := draftPullRequest(context.Background(), a, proj, state)
	if draft.Title != "Fix parser handling for #17" || !strings.Contains(draft.Body, "Parser handling is safer.") {
		t.Fatalf("draft = %+v\n%s", draft, draft.Body)
	}
	if !draft.Draft || draft.TitleStatic || draft.BodyStatic {
		t.Fatalf("draft/static rules = %+v", draft)
	}
	if got := strings.Join(draft.Labels, ","); got != "bug,tests" {
		t.Errorf("labels = %q, want bug,tests", got)
	}
	if local.calls != 2 || cloud.calls != 2 {
		t.Fatalf("calls local/cloud = %d/%d, want 2/2", local.calls, cloud.calls)
	}
	if got, err := tracker.ModelCount(context.Background(), "ollama", "local-small", time.Hour); err != nil || got != 2 {
		t.Fatalf("local usage count = %d, err=%v", got, err)
	}
	if got, err := tracker.ModelCount(context.Background(), "claude", "haiku", time.Hour); err != nil || got != 2 {
		t.Fatalf("cloud usage count = %d, err=%v", got, err)
	}
	outcomes, err := tracker.OutcomesSince(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 4 {
		t.Fatalf("outcomes = %d, want 4: %+v", len(outcomes), outcomes)
	}
	var invalid, escalated int
	for _, outcome := range outcomes {
		if !strings.Contains(outcome.Signals, "clerical") || !strings.Contains(outcome.Signals, "verb:pr.") || strings.Contains(outcome.Signals, "complex") {
			t.Errorf("signals = %q", outcome.Signals)
		}
		if outcome.TaskID != "run-17:pr.title" && outcome.TaskID != "run-17:pr.body" {
			t.Errorf("outcome is not run-rateable: %+v", outcome)
		}
		if outcome.ErrorKind == "validation" {
			invalid++
		}
		if strings.Contains(outcome.Note, "escalated=true") {
			escalated++
		}
	}
	if invalid != 2 || escalated != 2 {
		t.Errorf("invalid/escalated outcomes = %d/%d, want 2/2", invalid, escalated)
	}
	health, err := tracker.ChannelHealth(context.Background(), "ollama", budget.BreakerThreshold, budget.BreakerWindow)
	if err != nil {
		t.Fatal(err)
	}
	if health.FailuresRecent != 0 || health.CircuitOpen {
		t.Fatalf("validation rejection polluted transport breaker: %+v", health)
	}
	if _, err := tracker.RateOutcome(context.Background(), "run-17:pr.title", true, "useful draft"); err != nil {
		t.Fatalf("rate run-scoped PR outcome: %v", err)
	}
}

func TestPRMicrotaskFallbackHonorsLiveBudgetAndBreaker(t *testing.T) {
	tests := []struct {
		name    string
		budget  prDraftBudget
		breaker prDraftBreaker
	}{
		{name: "over budget", budget: prDraftBudget{"claude": 90}},
		{name: "circuit open", breaker: prDraftBreaker{"claude": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := &prDraftTestChannel{respond: func(channel.Request) channel.Response {
				return channel.Response{Text: "invalid"}
			}}
			cloud := &prDraftTestChannel{respond: func(channel.Request) channel.Response {
				return channel.Response{Text: "valid"}
			}}
			routing := config.Routing{
				Budget: config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}},
				Rules:  []config.Rule{{Verb: "pr.title", Use: "ollama:small", Fallback: []string{"claude:haiku"}}},
			}
			r := router.FromConfig(routing, tt.budget)
			r.Breaker = tt.breaker
			a := &app{router: r, channels: map[string]channel.Channel{"ollama": local, "claude": cloud}}
			result := runPRMicrotask(context.Background(), a, t.TempDir(), "pr.title", []string{"clerical"}, "prompt",
				func(text string) (string, error) {
					if text == "valid" {
						return text, nil
					}
					return "", os.ErrInvalid
				}, nil, "static")
			if !result.StaticFallback || local.calls != 1 || cloud.calls != 0 {
				t.Fatalf("budget-aware fallback result=%+v local/cloud=%d/%d", result, local.calls, cloud.calls)
			}
		})
	}
}

func TestDraftPullRequestContextFailureUsesStaticWithoutModels(t *testing.T) {
	model := &prDraftTestChannel{respond: func(channel.Request) channel.Response {
		return channel.Response{Text: `{}`}
	}}
	tracker, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	a := &app{
		router: prDraftRouter(), tracker: tracker,
		channels: map[string]channel.Channel{"ollama": model, "claude": model},
	}
	state := pipeline.NewState("run-static", "fix parser #8")
	state.Stages[4].Status, state.Stages[4].Attempts = pipeline.StageCompleted, 1
	state.Stages[5].Status, state.Stages[5].Attempts = pipeline.StageCompleted, 1
	proj := project.Project{ID: "project-static", Path: t.TempDir(), Language: "go"}

	draft := draftPullRequest(context.Background(), a, proj, state)
	if model.calls != 0 || !draft.TitleStatic || !draft.BodyStatic || !draft.Draft {
		t.Fatalf("model calls/draft = %d / %+v", model.calls, draft)
	}
	if draft.Title != "fix parser #8" || !strings.Contains(draft.Body, "Test stage: completed successfully") {
		t.Fatalf("static draft = %+v\n%s", draft, draft.Body)
	}
	outcomes, err := tracker.OutcomesSince(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 2 {
		t.Fatalf("static outcomes = %d, want 2", len(outcomes))
	}
	for _, outcome := range outcomes {
		if outcome.CLI != "static" || outcome.TaskID == "" || !strings.Contains(outcome.Note, "static_fallback=true") || !strings.Contains(outcome.Signals, "clerical") || !strings.Contains(outcome.Signals, "static-fallback") || !strings.Contains(outcome.Signals, "verb:pr.") {
			t.Errorf("static outcome = %+v", outcome)
		}
	}
}

func TestPRDraftSignalsRaiseFloorForRisk(t *testing.T) {
	packet := prdraft.Context{Goal: "update parser", RiskFlags: []string{"security-sensitive changes"}}
	got := prDraftSignals("pr.body", packet, project.Project{Language: "go"})
	if !slices.Contains(got, signals.SigClerical) || !slices.Contains(got, signals.SigComplex) {
		t.Fatalf("risk-aware signals = %v", got)
	}
}

func initPRDraftRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runPRDraftGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runPRDraftGit(t, repo, "add", ".")
	runPRDraftGit(t, repo, "commit", "-q", "-m", "base")
	runPRDraftGit(t, repo, "checkout", "-q", "-b", "styx/fix-parser")
	if err := os.MkdirAll(filepath.Join(repo, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "parser.go"), []byte("package internal\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runPRDraftGit(t, repo, "add", ".")
	runPRDraftGit(t, repo, "commit", "-q", "-m", "fix parser #17")
	return repo
}

func runPRDraftGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}
