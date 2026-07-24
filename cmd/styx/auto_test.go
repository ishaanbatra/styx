package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/attribution"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
)

func TestRouteChannelFallback(t *testing.T) {
	tests := []struct {
		name    string
		routing config.Routing
	}{
		{
			name: "routing error",
			routing: config.Routing{Rules: []config.Rule{{
				Verb: "test",
			}}},
		},
		{
			name: "unregistered routed channel",
			routing: config.Routing{Rules: []config.Rule{{
				Verb: "test",
				Use:  "missing:model",
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ollama := &recordingChannel{}
			a := &app{
				router: router.FromConfig(tt.routing, nil),
				channels: map[string]channel.Channel{
					"ollama": ollama,
				},
			}

			got := routeChannel(a, "test", nil)
			if got.ch != ollama {
				t.Errorf("channel = %T, want fallback Ollama channel", got.ch)
			}
			if got.model != "qwen2.5-coder:7b" {
				t.Errorf("model = %q, want qwen2.5-coder:7b", got.model)
			}
			if got.id != "ollama:qwen2.5-coder:7b" {
				t.Errorf("id = %q, want ollama:qwen2.5-coder:7b", got.id)
			}
		})
	}
}

func TestCommitReviewFixes(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(t *testing.T, repo string)
		wantCommit bool
	}{
		{
			name: "clean tree",
		},
		{
			name: "tracked change",
			mutate: func(t *testing.T, repo string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("fixed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantCommit: true,
		},
		{
			name: "untracked change",
			mutate: func(t *testing.T, repo string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, "fix.go"), []byte("package fix\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantCommit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := initPRDraftRepo(t)
			runPRDraftGit(t, repo, "config", "user.name", "Test")
			runPRDraftGit(t, repo, "config", "user.email", "test@example.com")
			runPRDraftGit(t, repo, "config", "commit.gpgsign", "false")
			before, err := gitRevParse(repo, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if tt.mutate != nil {
				tt.mutate(t, repo)
			}

			if err := commitReviewFixes(repo, 2); err != nil {
				t.Fatal(err)
			}

			after, err := gitRevParse(repo, "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if got := after != before; got != tt.wantCommit {
				t.Errorf("created commit = %t, want %t", got, tt.wantCommit)
			}
			status := exec.Command("git", "status", "--porcelain")
			status.Dir = repo
			if out, err := status.CombinedOutput(); err != nil {
				t.Fatalf("git status --porcelain: %v (%s)", err, out)
			} else if len(out) != 0 {
				t.Errorf("worktree still dirty: %s", out)
			}
			if !tt.wantCommit {
				return
			}
			log := exec.Command("git", "log", "-1", "--pretty=%B")
			log.Dir = repo
			out, err := log.CombinedOutput()
			if err != nil {
				t.Fatalf("git log: %v (%s)", err, out)
			}
			want := "fix(review): apply review fixes (attempt 2)\n\n" + attribution.Trailer
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("commit message = %q, want %q", got, want)
			}
		})
	}
}

func TestBuildRunnerRunReviewPersistsAttemptArtifacts(t *testing.T) {
	repo := initPRDraftRepo(t)
	tracker, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tracker.Close() })
	reviewer := &recordingChannel{}
	a := &app{
		tracker: tracker,
		router: router.FromConfig(config.Routing{Rules: []config.Rule{{
			Verb: "review",
			Use:  "ollama:test",
		}}}, nil),
		channels: map[string]channel.Channel{"ollama": reviewer},
	}
	proj := project.Project{ID: "p1", Path: repo}
	runner := buildRunner(a, proj, "run-17", "fix parser", false, true, true)

	for attempt := 1; attempt <= 2; attempt++ {
		if _, _, _, err := runner.RunReview(context.Background(), runner); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		attempt int
		want    string
	}{
		{attempt: 1, want: "ok"},
		{attempt: 2, want: "ok"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt %d", tt.attempt), func(t *testing.T) {
			path := filepath.Join(repo, "styx", "reviews", fmt.Sprintf("run-17-attempt-%d.md", tt.attempt))
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("artifact = %q, want %q", got, tt.want)
			}
		})
	}
	entries, err := os.ReadDir(filepath.Join(repo, "styx", "reviews"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("temporary artifact left behind: %s", entry.Name())
		}
	}
}
