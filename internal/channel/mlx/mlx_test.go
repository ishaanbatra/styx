package mlx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func installFakeMLX(t *testing.T) string {
	t.Helper()
	src, err := filepath.Abs("../../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mlx_lm.generate"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestSendSuccessUsesStdinAndDefaultModel(t *testing.T) {
	installFakeMLX(t)
	argsLog := filepath.Join(t.TempDir(), "args.log")
	stdinLog := filepath.Join(t.TempDir(), "stdin.log")
	t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)
	t.Setenv("FAKEAGENT_STDIN_LOG", stdinLog)

	resp, err := New().Send(context.Background(), channel.Request{
		System: "be concise",
		Prompt: "a prompt long enough that it must never be placed in argv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" {
		t.Fatal("expected generated text")
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := strings.TrimSpace(string(args))
	for _, want := range []string{
		"--model " + DefaultModel,
		"--system-prompt be concise",
		"--prompt -",
		"--max-tokens 1024",
		"--verbose false",
	} {
		if !strings.Contains(gotArgs, want) {
			t.Errorf("args %q missing %q", gotArgs, want)
		}
	}
	if strings.Contains(gotArgs, "a prompt long enough") {
		t.Errorf("prompt leaked into argv: %q", gotArgs)
	}
	stdin, err := os.ReadFile(stdinLog)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(stdin), "a prompt long enough that it must never be placed in argv"; got != want {
		t.Errorf("stdin = %q, want %q", got, want)
	}
}

func TestSendExplicitModel(t *testing.T) {
	installFakeMLX(t)
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)
	if _, err := New().Send(context.Background(), channel.Request{
		Model: "local/model", Prompt: "hi",
	}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--model local/model") {
		t.Errorf("explicit model not forwarded: %q", args)
	}
}

func TestSendNonZeroExitIsClassified(t *testing.T) {
	installFakeMLX(t)
	t.Setenv("FAKEAGENT_EXIT", "7")
	c := New()
	var streamed bytes.Buffer
	c.stderr = &streamed
	_, err := c.Send(context.Background(), channel.Request{Prompt: "hi"})
	assertKind(t, err, channel.ErrKindOther)
	if !strings.Contains(err.Error(), "fakeagent forced exit") {
		t.Errorf("captured stderr missing from error: %v", err)
	}
	if !strings.Contains(streamed.String(), "fakeagent forced exit") {
		t.Errorf("stderr was not streamed: %q", streamed.String())
	}
}

func TestSendSignalKillIsClassified(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signals are unavailable on Windows")
	}
	installFakeMLX(t)
	t.Setenv("FAKEAGENT_SIGNAL", "1")
	c := New()
	c.stderr = &bytes.Buffer{}
	_, err := c.Send(context.Background(), channel.Request{Prompt: "hi"})
	assertKind(t, err, channel.ErrKindKilled)
}

func TestSendMissingBinaryIsClassified(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := New().Send(context.Background(), channel.Request{Prompt: "hi"})
	assertKind(t, err, channel.ErrKindOther)
	if !strings.Contains(err.Error(), "mlx CLI not found on PATH") {
		t.Errorf("missing binary error = %v", err)
	}
}

func TestUnsupportedModesAreClassified(t *testing.T) {
	tests := []struct {
		name string
		req  channel.Request
	}{
		{name: "interactive", req: channel.Request{Interactive: true}},
		{name: "write", req: channel.Request{Write: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New().Send(context.Background(), tt.req)
			assertKind(t, err, channel.ErrKindOther)
		})
	}
}

func TestBudgetStateIsUnlimited(t *testing.T) {
	got, err := New().BudgetState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != (channel.Budget{}) {
		t.Errorf("BudgetState = %+v, want zero value", got)
	}
}

func TestParseOutputFixtures(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "verbose", want: "The answer is 42.\nWith a second line."},
		{name: "plain", want: "Generation: begins here, as ordinary prose.\nDone."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", tt.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if got := parseOutput(string(raw)); got != tt.want {
				t.Errorf("parseOutput = %q, want %q", got, tt.want)
			}
		})
	}
}

func assertKind(t *testing.T, err error, want channel.ErrorKindLabel) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	var classified *channel.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("error = %T %v, want ClassifiedError", err, err)
	}
	if classified.Kind != want {
		t.Errorf("kind = %q, want %q", classified.Kind, want)
	}
}
