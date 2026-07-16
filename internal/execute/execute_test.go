package execute

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/attribution"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/progress"
)

// recordingChannel is an in-memory channel.Channel that captures the last
// Request it received and returns a canned response.
type recordingChannel struct {
	got  channel.Request
	resp string
}

func TestBuildPromptIncludesAttribution(t *testing.T) {
	p := buildPrompt("PLAN BODY")
	if !strings.Contains(p, attribution.CommitInstruction) {
		t.Error("buildPrompt missing attribution.CommitInstruction")
	}
	if !strings.Contains(p, "PLAN BODY") {
		t.Error("buildPrompt missing plan content")
	}
}

func (r *recordingChannel) Name() string { return "fake" }
func (r *recordingChannel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}
func (r *recordingChannel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	r.got = req
	return channel.Response{Text: r.resp}, nil
}

func TestApply_RoutesThroughInjectedChannelWithWrite(t *testing.T) {
	fake := &recordingChannel{resp: "applied via codex"}
	out, err := Apply(context.Background(), Options{
		PlanContent: "# Plan\n\nAdd the feature.",
		ProjectPath: "/some/proj",
		Model:       "gpt-5",
		Channel:     fake,
	}, progress.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	if out != "applied via codex" {
		t.Errorf("Apply returned %q, want the channel response", out)
	}
	if !fake.got.Write {
		t.Error("injected channel must receive Write=true")
	}
	if fake.got.Model != "gpt-5" {
		t.Errorf("Model = %q, want gpt-5", fake.got.Model)
	}
	if fake.got.WorkingDir != "/some/proj" {
		t.Errorf("WorkingDir = %q, want /some/proj", fake.got.WorkingDir)
	}
	if !strings.Contains(fake.got.Prompt, "Add the feature.") {
		t.Errorf("prompt missing plan content: %q", fake.got.Prompt)
	}
}

func fakeClaude(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestApply_InvokesClaudeWithSkipPermissionsAndPrompt(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	dir := fakeClaude(t, `echo "$@" > "$STYX_ARGS_FILE"; echo "done"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("STYX_ARGS_FILE", argsFile)

	stdout, err := Apply(context.Background(), Options{
		PlanContent: "# Plan\n\nDo the thing.",
		ProjectPath: "/some/proj",
	}, progress.Quiet())
	if err != nil {
		t.Fatal(err)
	}
	if stdout == "" {
		t.Error("expected non-empty stdout")
	}
	captured, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--dangerously-skip-permissions", "-p", "implement this plan"}
	for _, w := range want {
		if !contains(string(captured), w) {
			t.Errorf("missing %q in claude args: %s", w, captured)
		}
	}
}

func TestApply_NonZeroExitIsError(t *testing.T) {
	dir := fakeClaude(t, `echo "boom" >&2; exit 7`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	_, err := Apply(context.Background(), Options{PlanContent: "x", ProjectPath: "/p"}, progress.Quiet())
	if err == nil {
		t.Fatal("expected error on non-zero exit")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDetectTestCommand(t *testing.T) {
	cases := []struct {
		name      string
		framework string
		wantCmd   []string
	}{
		{"pytest", "pytest", []string{"pytest"}},
		{"jest", "jest", []string{"npm", "test"}},
		{"vitest", "vitest", []string{"npm", "test"}},
		{"go test", "go test", []string{"go", "test", "./..."}},
		{"cargo test", "cargo test", []string{"cargo", "test"}},
		{"unknown framework yields nil", "homegrown", nil},
		{"empty framework yields nil", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DetectTestCommand(c.framework)
			if len(got) != len(c.wantCmd) {
				t.Fatalf("got %v, want %v", got, c.wantCmd)
			}
			for i, w := range c.wantCmd {
				if got[i] != w {
					t.Errorf("got[%d] = %q, want %q", i, got[i], w)
				}
			}
		})
	}
}

func TestRunTests_PassingCommand(t *testing.T) {
	res, err := RunTests(context.Background(), t.TempDir(), []string{"true"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Errorf("expected pass, got fail. output: %s", res.Output)
	}
}

func TestRunTests_FailingCommand(t *testing.T) {
	res, err := RunTests(context.Background(), t.TempDir(), []string{"false"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Passed {
		t.Error("expected fail, got pass")
	}
}

func TestRunTests_NilCommandSkips(t *testing.T) {
	res, err := RunTests(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped {
		t.Error("expected Skipped=true for nil command")
	}
}

func TestFixLoop_StopsWhenFixerSays_FIXED(t *testing.T) {
	attempts := 0
	verify := func(ctx context.Context) (bool, string) {
		attempts++
		// Pass on the second attempt.
		return attempts >= 2, "test output line " + itoaShim(attempts)
	}
	fix := func(ctx context.Context, problem string, attempt int) error {
		return nil
	}
	res, err := FixLoop(context.Background(), FixLoopOptions{
		MaxAttempts: 5,
		Verify:      verify,
		Fix:         fix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Fixed {
		t.Errorf("expected Fixed=true, got: %+v", res)
	}
	if res.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", res.Attempts)
	}
}

func TestFixLoop_ExhaustsAttempts(t *testing.T) {
	verify := func(ctx context.Context) (bool, string) { return false, "still broken" }
	fix := func(ctx context.Context, problem string, attempt int) error { return nil }
	res, err := FixLoop(context.Background(), FixLoopOptions{
		MaxAttempts: 3,
		Verify:      verify,
		Fix:         fix,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Fixed {
		t.Error("expected Fixed=false after exhaustion")
	}
	if res.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", res.Attempts)
	}
}

func itoaShim(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestShip_PushOnly(t *testing.T) {
	// Fake git so 'push' succeeds without touching a real remote.
	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "git"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", gitDir+":"+os.Getenv("PATH"))
	res, err := Ship(context.Background(), ShipOptions{
		ProjectPath: t.TempDir(),
		Branch:      "feat/x",
		NoPR:        true,
		NoPush:      false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pushed {
		t.Error("expected Pushed=true")
	}
	if res.PRURL != "" {
		t.Errorf("expected no PR URL, got %q", res.PRURL)
	}
}

func TestShip_NoPushSkipsPushAndPR(t *testing.T) {
	res, err := Ship(context.Background(), ShipOptions{
		ProjectPath: t.TempDir(),
		Branch:      "feat/y",
		NoPush:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pushed {
		t.Error("expected Pushed=false with NoPush=true")
	}
}

func TestPRBody(t *testing.T) {
	tests := []struct {
		name string
		opts ShipOptions
		want string
	}{
		{
			name: "default body gets goal plus footer",
			opts: ShipOptions{Goal: "add attribution"},
			want: "Goal: add attribution\n\n" + attribution.PRFooter,
		},
		{
			name: "custom body keeps content, gains footer",
			opts: ShipOptions{PRBody: "Custom summary.\n"},
			want: "Custom summary.\n\n" + attribution.PRFooter,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prBody(tt.opts); got != tt.want {
				t.Errorf("prBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShipCreatesExplicitDraftAndAppliesLabels(t *testing.T) {
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "gh.log")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gh := `#!/bin/sh
echo "$@" >> "$STYX_GH_LOG"
if [ "$1 $2" = "pr create" ]; then
  echo "https://github.com/acme/repo/pull/7"
  exit 0
fi
if [ "$1 $2" = "pr edit" ]; then
  exit 0
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("STYX_GH_LOG", logPath)

	res, err := Ship(context.Background(), ShipOptions{
		ProjectPath: t.TempDir(), Branch: "styx/change", Goal: "ignored",
		PRTitle: "Intentional title", PRBody: "Structured body", Draft: true,
		Labels: []string{"bug", "tests"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PRURL != "https://github.com/acme/repo/pull/7" || len(res.MetadataErrors) != 0 {
		t.Fatalf("result = %+v", res)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(log)
	for _, want := range []string{
		"pr create --title Intentional title --body Structured body", "--draft",
		"pr edit https://github.com/acme/repo/pull/7 --add-label bug --add-label tests",
		attribution.PRFooter,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("gh calls missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "--fill") {
		t.Errorf("gh create must not use --fill:\n%s", got)
	}
}

func TestShipRecoversExistingURLAndKeepsItOnLabelFailure(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	gh := `#!/bin/sh
if [ "$1 $2" = "pr create" ]; then
  echo "a pull request already exists" >&2
  exit 1
fi
if [ "$1 $2" = "pr view" ]; then
  echo "https://github.com/acme/repo/pull/9"
  exit 0
fi
if [ "$1 $2" = "pr edit" ]; then
  echo "label unavailable" >&2
  exit 2
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	res, err := Ship(context.Background(), ShipOptions{
		ProjectPath: t.TempDir(), Branch: "styx/resumed", PRTitle: "Title",
		PRBody: "Body", Labels: []string{"bug"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.PRURL != "https://github.com/acme/repo/pull/9" {
		t.Fatalf("recovered PR URL lost: %+v", res)
	}
	if len(res.MetadataErrors) != 1 || !strings.Contains(res.MetadataErrors[0], "label unavailable") {
		t.Fatalf("label failure not surfaced: %+v", res)
	}
}
