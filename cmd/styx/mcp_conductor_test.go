package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/shipgate"
)

// callTool is a test helper: find tool by name in conductorTools and invoke.
func callTool(t *testing.T, d *conductorDeps, name string, args any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	for _, tool := range conductorTools(d) {
		if tool.Name == name {
			res, err := tool.Handler(context.Background(), raw)
			if err != nil {
				return nil, err
			}
			b, _ := json.Marshal(res)
			var m map[string]any
			json.Unmarshal(b, &m)
			return m, nil
		}
	}
	t.Fatalf("tool %q not registered", name)
	return nil, nil
}

func TestDispatchShipGate(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeHandshake)}
	// No manager needed: the gate must fire BEFORE project resolution.
	first, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "codex", "message": "push it", "risk": "ship",
	})
	if err != nil {
		t.Fatalf("gated call must return a result, not an error: %v", err)
	}
	tok, _ := first["token"].(string)
	if first["allowed"] == true || tok == "" {
		t.Fatalf("want denied+token, got %v", first)
	}
}

func TestDispatchValidation(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff)}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"unknown cli", map[string]any{"cli": "gpt9", "message": "x", "risk": "edit"}, "unknown cli"},
		{"empty message", map[string]any{"cli": "codex", "risk": "edit"}, "message is required"},
		{"bad risk", map[string]any{"cli": "codex", "message": "x", "risk": "yolo"}, "risk must be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := callTool(t, d, "dispatch", tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestThreadStatusNoThreads(t *testing.T) {
	// Isolate from the real ~/.config/styx registry so alias resolution is
	// deterministic (mirrors the isolation pattern in mcp_test.go / repl_test.go).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff),
		managers: map[string]*managed{}}
	// project resolution failure surfaces as a tool error, not a panic
	_, err := callTool(t, d, "thread_status", map[string]any{"project": "no-such-project"})
	if err == nil {
		t.Fatal("unresolvable project must error loudly")
	}
}

// copyExecutable copies src to dst and marks dst executable (0755). Used to
// stand up a fake "claude" binary on PATH backed by testdata/fakeagent, since
// agent.NewClaudeAdapter() hardcodes Bin() == "claude" (no test override
// hook), unlike the *agent.ClaudeAdapter{BinPath: ...} used in package-local
// fixtures (internal/agent/manager_test.go, cmd/styx/repl_test.go's
// bindTestProject).
func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestDispatchHappyPath exercises dispatch end to end through
// conductorDeps.managerFor -> agent.Manager.Dispatch -> a fake "claude" CLI
// on PATH (testdata/fakeagent), asserting the tool result surfaces the fake's
// text and real usage tokens.
func TestDispatchHappyPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "conductor says hi")
	t.Setenv("FAKEAGENT_IN", "111")
	t.Setenv("FAKEAGENT_OUT", "22")

	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{
		{ID: "proj1", Name: "proj1", Path: projDir},
	}); err != nil {
		t.Fatal(err)
	}

	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })

	d := &conductorDeps{
		a: &app{
			routing: config.Routing{
				Brain: config.BrainConfig{
					Model:               "haiku",
					EmbedModel:          "nomic-embed-text",
					ContextThresholdPct: 70,
				},
				Tiers: map[string]string{"haiku": "haiku"},
			},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate:     shipgate.New(shipgate.ModeOff),
		emb:      replEmbedder{},
		managers: map[string]*managed{},
	}

	res, err := callTool(t, d, "dispatch", map[string]any{
		"project": "proj1", "cli": "claude", "message": "hello", "risk": "edit",
	})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res["text"] != "conductor says hi" {
		t.Errorf("text = %v, want %q", res["text"], "conductor says hi")
	}
	if res["tokens_in"] != float64(111) {
		t.Errorf("tokens_in = %v, want 111", res["tokens_in"])
	}
	if res["tokens_out"] != float64(22) {
		t.Errorf("tokens_out = %v, want 22", res["tokens_out"])
	}

	// thread_status on the same project should now report the thread.
	statusRes, err := callTool(t, d, "thread_status", map[string]any{"project": "proj1"})
	if err != nil {
		t.Fatalf("thread_status: %v", err)
	}
	lines, _ := statusRes["threads"].([]any)
	if len(lines) != 1 {
		t.Fatalf("threads = %v, want 1 line", statusRes["threads"])
	}
}

func TestPipelineRunGatesAuto(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeHandshake)}
	res, err := callTool(t, d, "pipeline_run", map[string]any{"pipeline": "auto", "arg": "build the thing"})
	if err != nil {
		t.Fatalf("gated call must return result, not error: %v", err)
	}
	tok, _ := res["token"].(string)
	if res["allowed"] == true || tok == "" {
		t.Fatalf("auto must be ship-gated, got %v", res)
	}
}

func TestPipelineRunRejectsUnknown(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff)}
	if _, err := callTool(t, d, "pipeline_run", map[string]any{"pipeline": "yolo"}); err == nil {
		t.Fatal("unknown pipeline must error")
	}
}

func TestMemorySaveValidatesKind(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff)}
	if _, err := callTool(t, d, "memory_save", map[string]any{"kind": "vibe", "text": "x"}); err == nil {
		t.Fatal("unknown kind must error")
	}
	if _, err := callTool(t, d, "memory_save", map[string]any{"kind": "decision", "text": ""}); err == nil {
		t.Fatal("empty text must error")
	}
}
