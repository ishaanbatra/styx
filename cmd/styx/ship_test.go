package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func TestParseShipArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    shipCLIArgs
		wantErr string
	}{
		{name: "empty", want: shipCLIArgs{}},
		{name: "goal words", args: []string{"publish", "parser", "fix"}, want: shipCLIArgs{Goal: "publish parser fix"}},
		{name: "no pr", args: []string{"--no-pr", "publish", "parser"}, want: shipCLIArgs{NoPR: true, Goal: "publish parser"}},
		{name: "no push", args: []string{"publish", "--no-push", "parser"}, want: shipCLIArgs{NoPush: true, Goal: "publish parser"}},
		{name: "both flags", args: []string{"--no-pr", "--no-push", "publish"}, want: shipCLIArgs{NoPR: true, NoPush: true, Goal: "publish"}},
		{name: "base", args: []string{"publish", "--base", "feature/parent", "parser"}, want: shipCLIArgs{BaseBranch: "feature/parent", Goal: "publish parser"}},
		{name: "missing base value", args: []string{"publish", "--base"}, wantErr: "--base requires a branch"},
		{name: "base followed by flag", args: []string{"--base", "--no-pr"}, wantErr: "--base requires a branch"},
		{name: "empty base value", args: []string{"--base", ""}, wantErr: "--base requires a branch"},
		{name: "equals form rejected", args: []string{"--base=feature/parent"}, wantErr: "use --base <branch>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseShipArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseShipArgs(%v) error = %v, want substring %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseShipArgs(%v): %v", tt.args, err)
			}
			if got != tt.want {
				t.Fatalf("parseShipArgs(%v) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestCmdShipRefusesUnsafeRepositoryStates(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*testing.T, string)
		args    []string
		wantErr string
	}{
		{
			name:    "default branch",
			wantErr: "refusing to ship the default branch",
		},
		{
			name: "dirty worktree",
			setup: func(t *testing.T, repo string) {
				shipTestFeatureCommit(t, repo)
				if err := os.WriteFile(filepath.Join(repo, "uncommitted.txt"), []byte("dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "commit first; styx ship publishes committed work only",
		},
		{
			name: "no commits ahead",
			setup: func(t *testing.T, repo string) {
				runShipTestGit(t, repo, "checkout", "-q", "-b", "feature/empty")
			},
			wantErr: "has no commits ahead",
		},
		{
			name: "unresolvable explicit base",
			setup: func(t *testing.T, repo string) {
				shipTestFeatureCommit(t, repo)
			},
			args:    []string{"--base", "feature/missing"},
			wantErr: `resolve base branch "feature/missing"`,
		},
		{
			name: "explicit base is current branch",
			setup: func(t *testing.T, repo string) {
				shipTestFeatureCommit(t, repo)
			},
			args:    []string{"--base", "feature/ship"},
			wantErr: "refusing to ship the base branch",
		},
		{
			name: "no commits ahead of explicit base",
			setup: func(t *testing.T, repo string) {
				shipTestFeatureCommit(t, repo)
				runShipTestGit(t, repo, "branch", "feature/parent")
				runShipTestGit(t, repo, "checkout", "-q", "-b", "feature/empty")
			},
			args:    []string{"--base", "feature/parent"},
			wantErr: "has no commits ahead of base branch",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			repo := initShipTestRepo(t, false)
			if tt.setup != nil {
				tt.setup(t, repo)
			}
			withShipTestTarget(t, repo)

			err := cmdShip(context.Background(), &app{}, tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("cmdShip() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestCmdShipAcceptsResolvableNonDefaultBase(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repo := initShipTestRepo(t, false)
	runShipTestGit(t, repo, "checkout", "-q", "-b", "feature/parent")
	if err := os.WriteFile(filepath.Join(repo, "parent.txt"), []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShipTestGit(t, repo, "add", ".")
	runShipTestGit(t, repo, "commit", "-q", "-m", "add parent")
	runShipTestGit(t, repo, "checkout", "-q", "-b", "feature/stack")
	if err := os.WriteFile(filepath.Join(repo, "stack.txt"), []byte("stack\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShipTestGit(t, repo, "add", ".")
	runShipTestGit(t, repo, "commit", "-q", "-m", "add stack")
	withShipTestTarget(t, repo)

	if err := cmdShip(context.Background(), &app{}, []string{"--base", "feature/parent", "--no-push"}); err != nil {
		t.Fatalf("cmdShip() with explicit base: %v", err)
	}
}

func TestCmdShipNoPublishFlagsSkipDraftModels(t *testing.T) {
	tests := []struct {
		name string
		flag string
	}{
		{name: "no pr", flag: "--no-pr"},
		{name: "no push", flag: "--no-push"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			repo := initShipTestRepo(t, true)
			shipTestFeatureCommit(t, repo)
			withShipTestTarget(t, repo)

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
			if err := cmdShip(context.Background(), a, []string{tt.flag, "publish parser"}); err != nil {
				t.Fatal(err)
			}
			if model.calls != 0 {
				t.Fatalf("drafting model called %d time(s)", model.calls)
			}
		})
	}
}

func initShipTestRepo(t *testing.T, withOrigin bool) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "work")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runShipTestGit(t, repo, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShipTestGit(t, repo, "add", ".")
	runShipTestGit(t, repo, "commit", "-q", "-m", "base")
	if withOrigin {
		remote := filepath.Join(root, "origin.git")
		if err := os.MkdirAll(remote, 0o755); err != nil {
			t.Fatal(err)
		}
		runShipTestGit(t, remote, "init", "-q", "--bare", "--initial-branch=main")
		runShipTestGit(t, repo, "remote", "add", "origin", remote)
		runShipTestGit(t, repo, "push", "-q", "-u", "origin", "main")
	}
	return repo
}

func shipTestFeatureCommit(t *testing.T, repo string) {
	t.Helper()
	runShipTestGit(t, repo, "checkout", "-q", "-b", "feature/ship")
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runShipTestGit(t, repo, "add", ".")
	runShipTestGit(t, repo, "commit", "-q", "-m", "add feature")
}

func runShipTestGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v (%s)", strings.Join(args, " "), err, out)
	}
}

func withShipTestTarget(t *testing.T, repo string) {
	t.Helper()
	oldAlias, oldDir := globalProjectAlias, globalDirArg
	globalProjectAlias, globalDirArg = "", repo
	t.Cleanup(func() {
		globalProjectAlias, globalDirArg = oldAlias, oldDir
	})
}
