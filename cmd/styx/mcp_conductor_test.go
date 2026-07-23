package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/attribution"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	channelmlx "github.com/ishaanbatra/styx/internal/channel/mlx"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
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

func TestDispatchSchemaIncludesMLX(t *testing.T) {
	for _, tool := range conductorTools(&conductorDeps{}) {
		if tool.Name != "dispatch" {
			continue
		}
		schema := tool.InputSchema.(map[string]any)
		properties := schema["properties"].(map[string]any)
		cli := properties["cli"].(map[string]any)
		values := cli["enum"].([]string)
		for _, value := range values {
			if value == "mlx" {
				return
			}
		}
		t.Fatalf("dispatch cli enum = %v, want mlx", values)
	}
	t.Fatal("dispatch tool not registered")
}

func TestAttributedMessage(t *testing.T) {
	tests := []struct {
		name, msg, risk string
		wantDecorated   bool
	}{
		{"read untouched", "summarize the router", "read", false},
		{"edit decorated", "fix the loader", "edit", true},
		{"ship decorated", "ship the branch", "ship", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attributedMessage(tt.msg, tt.risk)
			decorated := strings.HasSuffix(got, attribution.CommitInstruction)
			if decorated != tt.wantDecorated {
				t.Errorf("attributedMessage(%q, %q): decorated = %v, want %v",
					tt.msg, tt.risk, decorated, tt.wantDecorated)
			}
			if !strings.HasPrefix(got, tt.msg) {
				t.Errorf("original message must be preserved as prefix, got %q", got)
			}
			if !tt.wantDecorated && got != tt.msg {
				t.Errorf("read-risk message must pass through untouched, got %q", got)
			}
		})
	}
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
		reg:      newTaskRegistry(context.Background(), 4, nil),
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
	if res["status"] != "done" || res["task_id"] == "" {
		t.Fatalf("awaited dispatch must return a claimed done task, got %v", res)
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

	// thread_status carries background task rows (always an array).
	if _, ok := statusRes["tasks"].([]any); !ok {
		t.Fatalf("thread_status must include a tasks array, got %T", statusRes["tasks"])
	}
}

// TestDispatchOllamaOneShotRecordsUsage is a regression test: the ollama
// one-shot branch returned without touching the budget tracker, so local
// dispatches never appeared in `styx budget` and the ledger under-reported
// real conductor activity.
func TestDispatchOllamaOneShotRecordsUsage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })

	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{"ollama": &recordingChannel{}},
		},
		gate:     shipgate.New(shipgate.ModeOff),
		emb:      replEmbedder{},
		managers: map[string]*managed{},
	}

	if _, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "ollama", "message": "write a haiku", "risk": "read",
	}); err != nil {
		t.Fatalf("dispatch ollama: %v", err)
	}

	st, err := bud.State(context.Background(), "ollama")
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionCount != 1 {
		t.Fatalf("ollama SessionCount = %d, want 1 (one-shot not recorded)", st.SessionCount)
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

func TestPipelineRunShipHandshake(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	repo := initShipTestRepo(t, true)
	shipTestFeatureCommit(t, repo)
	withShipTestTarget(t, repo)

	binDir := t.TempDir()
	gh := []byte("#!/bin/sh\nif [ \"$1\" = pr ] && [ \"$2\" = create ]; then\n  echo https://example.test/pr/17\nfi\n")
	if err := os.WriteFile(filepath.Join(binDir, "gh"), gh, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := &conductorDeps{a: &app{}, gate: shipgate.New(shipgate.ModeHandshake)}

	t.Run("unknown token is not allowed", func(t *testing.T) {
		res, err := callTool(t, d, "pipeline_run", map[string]any{
			"pipeline": "ship", "arg": "publish feature", "confirm_token": "unknown",
		})
		if err != nil {
			t.Fatalf("gated call must return result, not error: %v", err)
		}
		if res["allowed"] == true || !strings.Contains(res["message"].(string), "invalid or expired") {
			t.Fatalf("unknown token must be denied, got %v", res)
		}
	})

	var token string
	t.Run("missing token returns confirmation token", func(t *testing.T) {
		res, err := callTool(t, d, "pipeline_run", map[string]any{
			"pipeline": "ship", "arg": "publish feature",
		})
		if err != nil {
			t.Fatalf("gated call must return result, not error: %v", err)
		}
		token, _ = res["token"].(string)
		if res["allowed"] == true || token == "" {
			t.Fatalf("ship must be denied with a token, got %v", res)
		}
	})

	t.Run("valid token runs ship", func(t *testing.T) {
		res, err := callTool(t, d, "pipeline_run", map[string]any{
			"pipeline": "ship", "arg": "publish feature", "confirm_token": token,
		})
		if err != nil {
			t.Fatal(err)
		}
		if res["done"] != true || res["pipeline"] != "ship" {
			t.Fatalf("pipeline_run ship result = %v", res)
		}
	})
}

func TestPipelineRunRejectsUnknown(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff)}
	if _, err := callTool(t, d, "pipeline_run", map[string]any{"pipeline": "yolo"}); err == nil {
		t.Fatal("unknown pipeline must error")
	}
}

func TestPipelineRunDebug(t *testing.T) {
	fx := newDebugCommandFixture(t)
	// Pre-seed the manager cache so the post-run best-effort indexing path does
	// not need to construct agent adapters; cmdDebug itself is still exercised.
	d := &conductorDeps{
		a: fx.a, gate: shipgate.New(shipgate.ModeOff),
		managers: map[string]*managed{fx.projectID: {}},
	}
	res, err := callTool(t, d, "pipeline_run", map[string]any{
		"pipeline": "debug", "arg": "panic in cache",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res["done"] != true || res["pipeline"] != "debug" {
		t.Fatalf("pipeline_run debug result = %v", res)
	}
	if len(fx.sweep.snapshot()) != 1 || len(fx.codex.snapshot()) != 1 || len(fx.claude.snapshot()) != 1 {
		t.Fatal("pipeline_run debug did not execute all three roles")
	}
}

// TestPipelineRunIntelResolvesCwdProject proves the intel branch resolves the
// server's cwd project (the Task 6 launcher starts `styx mcp` in the project
// dir) the same way research/review/auto resolve theirs internally, instead of
// failing at target.Resolve with an empty alias. Full intel execution is
// impractical to fake here, so the test asserts the call gets PAST project
// resolution and INTO cmdIntel by hitting its controlled "agy channel not
// registered" boundary (the app's channels map is empty).
func TestPipelineRunIntelResolvesCwdProject(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())

	projDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	d := &conductorDeps{
		a:        &app{channels: map[string]channel.Channel{}},
		gate:     shipgate.New(shipgate.ModeOff),
		managers: map[string]*managed{},
	}
	_, err = callTool(t, d, "pipeline_run", map[string]any{"pipeline": "intel"})
	if err == nil {
		t.Fatal("want cmdIntel's agy-channel boundary error, got success")
	}
	if strings.Contains(err.Error(), "no target") || strings.Contains(err.Error(), "resolve project") {
		t.Fatalf("intel must resolve the cwd project, failed at resolution: %v", err)
	}
	if !strings.Contains(err.Error(), "agy channel not registered") {
		t.Fatalf("want cmdIntel's agy boundary, got: %v", err)
	}
}

// captureChannel records the last Request and returns a canned response.
type captureChannel struct{ last channel.Request }

func (c *captureChannel) Name() string { return "ollama" }
func (c *captureChannel) Send(_ context.Context, req channel.Request) (channel.Response, error) {
	c.last = req
	return channel.Response{Text: "pong", EstTokensIn: 3, EstTokensOut: 1}, nil
}
func (c *captureChannel) BudgetState(_ context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

func TestDispatchOllamaDefaultsModel(t *testing.T) {
	tr, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cap := &captureChannel{}
	d := &conductorDeps{
		gate: shipgate.New(shipgate.ModeOff),
		a: &app{
			channels: map[string]channel.Channel{"ollama": cap},
			tracker:  tr,
			routing:  config.Routing{Brain: config.BrainConfig{Model: "qwen2.5-coder:7b"}},
		},
	}
	res, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "ollama", "message": "say pong", "risk": "read",
	})
	if err != nil {
		t.Fatalf("ollama dispatch without model must succeed: %v", err)
	}
	if cap.last.Model != "qwen2.5-coder:7b" {
		t.Fatalf("model must default to routing Brain.Model, got %q", cap.last.Model)
	}
	if res["text"] != "pong" {
		t.Fatalf("want text pong, got %v", res["text"])
	}
	if _, ok := res["duration_s"]; !ok {
		t.Fatal("dispatch result must include duration_s")
	}
	if res["model"] != "qwen2.5-coder:7b" {
		t.Fatalf("dispatch result must echo the resolved model, got %v", res["model"])
	}
}

func TestDispatchMLXOneShotDefaultsModel(t *testing.T) {
	tr, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	cap := &captureChannel{}
	d := &conductorDeps{
		gate: shipgate.New(shipgate.ModeOff),
		a: &app{
			channels: map[string]channel.Channel{"mlx": cap},
			tracker:  tr,
		},
	}
	res, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "mlx", "message": "say pong", "risk": "read",
	})
	if err != nil {
		t.Fatalf("mlx dispatch without model must succeed: %v", err)
	}
	if cap.last.Model != channelmlx.DefaultModel {
		t.Fatalf("model = %q, want %q", cap.last.Model, channelmlx.DefaultModel)
	}
	if res["cli"] != "mlx" || res["text"] != "pong" || res["model"] != channelmlx.DefaultModel {
		t.Fatalf("mlx dispatch result = %v", res)
	}
	st, err := tr.State(context.Background(), "mlx")
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionCount != 1 {
		t.Fatalf("mlx SessionCount = %d, want 1", st.SessionCount)
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

func TestMemorySaveRoutesLearningKindsToGlobalStore(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &conductorDeps{
		gate:     shipgate.New(shipgate.ModeOff),
		emb:      replEmbedder{},
		managers: map[string]*managed{},
	}
	res, err := callTool(t, d, "memory_save", map[string]any{
		"kind": "retrospective", "text": "worked: codex on specced tasks / didn't: agy timeouts",
	})
	if err != nil {
		t.Fatalf("memory_save retrospective: %v", err)
	}
	if res["saved"] != true {
		t.Fatalf("want saved=true, got %v", res)
	}
	if _, err := callTool(t, d, "memory_save", map[string]any{
		"kind": "user-preference", "text": "prefers table-driven tests",
	}); err != nil {
		t.Fatalf("memory_save user-preference: %v", err)
	}
	if _, err := callTool(t, d, "memory_save", map[string]any{
		"kind": "routing-preference", "text": "use codex for specced repo edits",
	}); err != nil {
		t.Fatalf("memory_save routing-preference: %v", err)
	}

	// All three landed in global.db — not a project store (note: no project was
	// even resolvable here; global kinds must not require one).
	memDir, err := paths.MemoryDir()
	if err != nil {
		t.Fatal(err)
	}
	glob, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer glob.Close()
	items, err := glob.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("want 3 global items, got %d", len(items))
	}
	kinds := map[memory.Kind]bool{items[0].Kind: true, items[1].Kind: true, items[2].Kind: true}
	if !kinds[memory.KindRetrospective] || !kinds[memory.KindUserPreference] || !kinds[memory.KindRoutingPreference] {
		t.Fatalf("wrong kinds in global store: %+v", items)
	}
	if items[0].Scope != "global" {
		t.Fatalf("learning kinds default to global scope, got %q", items[0].Scope)
	}
}

func TestManagerForEmptyAliasUsesCwd(t *testing.T) {
	// Isolated config home; cwd (this repo) auto-registers on resolution.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &conductorDeps{managers: map[string]*managed{}}
	// managerFor needs deps only past resolution; a nil app panics later,
	// so resolve-only is asserted through the error path shape:
	// build minimal deps the way managerForProject needs them.
	tr, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	d.a = &app{tracker: tr, routing: config.Routing{}, channels: map[string]channel.Channel{"ollama": &captureChannel{}}}
	m, err := d.managerFor("")
	if err != nil {
		t.Fatalf("empty alias must resolve via cwd (launch dir), got %v", err)
	}
	if m.mgr.Project.Name == "" {
		t.Fatal("resolved project must be the cwd repo")
	}
}

func TestManagerForUnknownAliasListsRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	d := &conductorDeps{managers: map[string]*managed{}}
	_, err := d.managerFor("definitely-not-registered")
	if err == nil {
		t.Fatal("unknown alias must stay a loud error")
	}
	if !strings.Contains(err.Error(), "registered projects") {
		t.Fatalf("error must list registered projects for the MCP consumer, got: %v", err)
	}
}

func TestDispatchThreadAppendsOutcomeRow(t *testing.T) {
	// Same scaffolding as TestDispatchHappyPath: fakeagent as `claude` on PATH.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "done")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
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
				Brain: config.BrainConfig{Model: "haiku", ContextThresholdPct: 70},
				Tiers: map[string]string{"haiku": "haiku"},
			},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate:     shipgate.New(shipgate.ModeOff),
		emb:      replEmbedder{},
		managers: map[string]*managed{},
		reg:      newTaskRegistry(context.Background(), 4, nil),
	}

	if _, err := callTool(t, d, "dispatch", map[string]any{
		"project": "proj1", "cli": "claude",
		"message": "refactor the loader architecture", "risk": "edit",
	}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	rows, err := bud.OutcomesSince(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("read outcomes: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 outcome row, got %d", len(rows))
	}
	o := rows[0]
	if o.CLI != "claude" || o.Thread != "claude" || o.Risk != "edit" || o.Background {
		t.Fatalf("outcome row mismatch: %+v", o)
	}
	if o.ErrorKind != "" {
		t.Fatalf("success must record empty error kind, got %q", o.ErrorKind)
	}
	if o.TaskID == "" {
		t.Fatal("awaited dispatch outcome must carry its task id")
	}
	if !strings.Contains(o.Signals, "complex") {
		t.Fatalf("signals must be extracted from the message (refactor => complex), got %q", o.Signals)
	}
	if o.TokensIn == 0 || o.DurationS < 0 {
		t.Fatalf("tokens/duration must be recorded: %+v", o)
	}
}

func TestDispatchOllamaAppendsOutcomeRow(t *testing.T) {
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	cap := &captureChannel{}
	d := &conductorDeps{
		gate: shipgate.New(shipgate.ModeOff),
		a: &app{
			channels: map[string]channel.Channel{"ollama": cap},
			tracker:  bud,
			routing:  config.Routing{Brain: config.BrainConfig{Model: "qwen2.5-coder:7b"}},
		},
	}
	if _, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "ollama", "message": "say pong", "risk": "read",
	}); err != nil {
		t.Fatalf("ollama dispatch: %v", err)
	}
	rows, err := bud.OutcomesSince(context.Background(), time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("want 1 outcome row, got %d (%v)", len(rows), err)
	}
	if rows[0].CLI != "ollama" || rows[0].Thread != "" || rows[0].Model != "qwen2.5-coder:7b" {
		t.Fatalf("ollama outcome mismatch: %+v", rows[0])
	}
}

func TestRateDispatchStampsMostRecentOutcome(t *testing.T) {
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	ctx := context.Background()
	for _, o := range []budget.Outcome{
		{Thread: "codex", CLI: "codex"},
		{Thread: "codex", CLI: "codex"},
	} {
		if err := bud.RecordOutcome(ctx, o); err != nil {
			t.Fatal(err)
		}
	}
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff), a: &app{tracker: bud}}

	res, err := callTool(t, d, "rate_dispatch", map[string]any{
		"thread_or_task": "codex", "ok": true, "note": "clean implement",
	})
	if err != nil {
		t.Fatalf("rate_dispatch: %v", err)
	}
	if res["rated"] != true {
		t.Fatalf("want rated=true, got %v", res)
	}
	rows, _ := bud.OutcomesSince(ctx, time.Time{})
	if rows[0].Rating != "good" || rows[0].Note != "clean implement" {
		t.Fatalf("most recent codex outcome must carry the rating: %+v", rows[0])
	}
	if rows[1].Rating != "" {
		t.Fatalf("older outcome must stay unrated: %+v", rows[1])
	}

	// Unknown ref is a loud error; missing arg too.
	if _, err := callTool(t, d, "rate_dispatch", map[string]any{"thread_or_task": "ghost", "ok": false}); err == nil {
		t.Fatal("unknown thread/task must error")
	}
	if _, err := callTool(t, d, "rate_dispatch", map[string]any{"ok": true}); err == nil {
		t.Fatal("missing thread_or_task must error")
	}
}

func TestDispatchBackgroundRejectsShipAndOllama(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff)}
	_, err := callTool(t, d, "dispatch", map[string]any{
		"cli": "codex", "message": "ship it", "risk": "ship", "background": true,
	})
	if err == nil || !strings.Contains(err.Error(), "background") {
		t.Fatalf("ship-risk background dispatch must be rejected at spawn, got %v", err)
	}
	_, err = callTool(t, d, "dispatch", map[string]any{
		"cli": "ollama", "message": "quick", "risk": "read", "background": true,
	})
	if err == nil || !strings.Contains(err.Error(), "ollama") {
		t.Fatalf("ollama background dispatch must be rejected, got %v", err)
	}
}

func TestDispatchBackgroundSpawnBudgetCheck(t *testing.T) {
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	// Trip the circuit: 3 failures inside the breaker window.
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := bud.Record(ctx, budget.Event{Channel: "codex", Verb: "thread", Success: false, ErrorKind: "5xx"}); err != nil {
			t.Fatal(err)
		}
	}
	d := &conductorDeps{
		gate: shipgate.New(shipgate.ModeOff),
		a:    &app{tracker: bud, routing: config.Routing{}},
		reg:  newTaskRegistry(context.Background(), 4, nil),
	}
	_, err = callTool(t, d, "dispatch", map[string]any{
		"cli": "codex", "message": "long task", "risk": "read", "background": true,
	})
	if err == nil || !strings.Contains(err.Error(), "circuit") {
		t.Fatalf("spawn must fail synchronously on an open circuit, got %v", err)
	}
}

// TestDispatchAwaitedQueuesBehindBusyThread: a dispatch to a thread with a
// live background task no longer errors — it queues behind it (ordering
// rules) and completes once the blocker releases.
func TestDispatchAwaitedQueuesBehindBusyThread(t *testing.T) {
	fastAwait(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "queued then ran")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{}, reg: reg,
	}
	// t1: blocking background task holding the thread.
	run1, started1, release1 := blockingRun(nil)
	reg.Spawn(taskSpec{ProjectID: "proj1", Thread: "claude", CLI: "claude", Risk: "edit"}, run1)
	<-started1

	var handler func(context.Context, json.RawMessage) (any, error)
	for _, tool := range conductorTools(d) {
		if tool.Name == "dispatch" {
			handler = tool.Handler
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"project": "proj1", "thread": "claude", "cli": "claude", "message": "hi", "risk": "read",
	})
	type ret struct {
		res any
		err error
	}
	ch := make(chan ret, 1)
	go func() {
		res, err := handler(context.Background(), raw)
		ch <- ret{res, err}
	}()
	waitFor(t, "dispatch queued behind t1", func() bool {
		tk, ok := reg.Get("t2")
		return ok && tk.State == taskQueued && tk.QueuedBehind == "t1"
	})
	close(release1)
	got := <-ch
	if got.err != nil {
		t.Fatalf("queued dispatch must complete after blocker releases: %v", got.err)
	}
	b, _ := json.Marshal(got.res)
	var res map[string]any
	json.Unmarshal(b, &res)
	if res["text"] != "queued then ran" || res["status"] != "done" {
		t.Fatalf("awaited result mismatch: %v", res)
	}
}

// TestDispatchAwaitedDetachesOnCancel: cancelling the call context mid-await
// (host Esc) returns a detach notice; the task keeps running unclaimed.
func TestDispatchAwaitedDetachesOnCancel(t *testing.T) {
	fastAwait(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "slow answer")
	t.Setenv("FAKEAGENT_SLEEP", "1")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{}, reg: reg,
	}
	var handler func(context.Context, json.RawMessage) (any, error)
	for _, tool := range conductorTools(d) {
		if tool.Name == "dispatch" {
			handler = tool.Handler
		}
	}
	raw, _ := json.Marshal(map[string]any{
		"project": "proj1", "cli": "claude", "message": "hi", "risk": "read",
	})
	ctx, cancel := context.WithCancel(context.Background())
	type ret struct {
		res any
		err error
	}
	ch := make(chan ret, 1)
	go func() {
		res, err := handler(ctx, raw)
		ch <- ret{res, err}
	}()
	waitFor(t, "task running", func() bool { return state(reg, "t1") == taskRunning })
	cancel()
	got := <-ch
	if got.err != nil {
		t.Fatalf("detach must not be an error: %v", got.err)
	}
	b, _ := json.Marshal(got.res)
	var res map[string]any
	json.Unmarshal(b, &res)
	if res["detached"] != true || res["task_id"] != "t1" {
		t.Fatalf("want detach notice for t1, got %v", res)
	}
	if tk, _ := reg.Get("t1"); tk.Claimed {
		t.Fatal("detached task must stay unclaimed and collectible")
	}
}

func TestCollectSingleTaskLifecycle(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff), reg: reg}
	run1, started1, release1 := blockingRun(map[string]any{"text": "answer", "cli": "codex"})
	id, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"}, run1)
	<-started1

	// Unfinished: status + elapsed, NOT claimed.
	res, err := callTool(t, d, "collect", map[string]any{"task_id": id})
	if err != nil {
		t.Fatalf("collect running: %v", err)
	}
	if res["status"] != "running" {
		t.Fatalf("want running, got %v", res)
	}
	if _, ok := res["elapsed_s"]; !ok {
		t.Fatal("unfinished collect must report elapsed_s")
	}

	close(release1)
	waitFor(t, "done", func() bool { return state(reg, id) == taskDone })

	// Finished: full result, marks claimed.
	res, err = callTool(t, d, "collect", map[string]any{"task_id": id})
	if err != nil {
		t.Fatalf("collect done: %v", err)
	}
	if res["status"] != "done" || res["text"] != "answer" {
		t.Fatalf("collect must return the dispatch result, got %v", res)
	}
	if tk, _ := reg.Get(id); !tk.Claimed {
		t.Fatal("collecting a finished task must claim it")
	}

	// Unknown id: loud error.
	if _, err := callTool(t, d, "collect", map[string]any{"task_id": "t99"}); err == nil {
		t.Fatal("unknown task id must error")
	}
}

func TestBackgroundDispatchRoundtrip(t *testing.T) {
	// Full conductor-level lifecycle against a real (fake) CLI subprocess:
	// dispatch background → immediate task handle → piggyback on thread_status
	// → collect the finished result → outcome row carries the task id.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "bg-done")
	t.Setenv("FAKEAGENT_SLEEP", "1")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
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
				Brain: config.BrainConfig{ContextThresholdPct: 70},
				Tiers: map[string]string{},
			},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{},
		reg:      newTaskRegistry(context.Background(), 4, nil),
	}

	res, err := callTool(t, d, "dispatch", map[string]any{
		"project": "proj1", "cli": "claude", "message": "long job", "risk": "read",
		"background": true,
	})
	if err != nil {
		t.Fatalf("background dispatch: %v", err)
	}
	taskID, _ := res["task_id"].(string)
	if taskID == "" || res["status"] != "running" {
		t.Fatalf("want immediate task handle, got %v", res)
	}

	// While running: thread_status carries the task row (piggyback is
	// covered by TestWithBackgroundStatusPiggyback; here we assert the
	// conductor-level surface).
	ts, err := callTool(t, d, "thread_status", map[string]any{"project": "proj1"})
	if err != nil {
		t.Fatalf("thread_status: %v", err)
	}
	tasks, _ := ts["tasks"].([]any)
	if len(tasks) != 1 || !strings.Contains(tasks[0].(string), taskID) {
		t.Fatalf("running task must appear in thread_status tasks, got %v", ts["tasks"])
	}

	// Poll collect until done (fakeagent sleeps 1s).
	waitFor(t, "task done", func() bool {
		got, err := callTool(t, d, "collect", map[string]any{"task_id": taskID})
		return err == nil && got["status"] == "done"
	})
	// The done collect above claimed it — re-collect by id shows the claimed
	// result again (Get still knows it), but collect({}) has nothing pending.
	all, err := callTool(t, d, "collect", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if results, _ := all["results"].([]any); len(results) != 0 {
		t.Fatalf("claimed task must not resurface in collect all, got %v", all["results"])
	}

	// Outcome row: background flag + task id.
	rows, err := bud.OutcomesSince(context.Background(), time.Time{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("want 1 outcome row, got %d (%v)", len(rows), err)
	}
	if !rows[0].Background || rows[0].TaskID != taskID || rows[0].CLI != "claude" {
		t.Fatalf("background outcome row mismatch: %+v", rows[0])
	}
}

func TestCollectAllAndThreadStatusTasks(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4, nil)
	runA, startedA, releaseA := blockingRun(map[string]any{"text": "A"})
	idA, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "a", CLI: "codex", Risk: "read"}, runA)
	<-startedA
	close(releaseA)
	waitFor(t, "A done", func() bool { return state(reg, idA) == taskDone })
	runB, startedB, releaseB := blockingRun(nil)
	idB, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "b", CLI: "claude", Risk: "read"}, runB)
	<-startedB
	defer close(releaseB)

	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff), reg: reg}
	res, err := callTool(t, d, "collect", map[string]any{})
	if err != nil {
		t.Fatalf("collect all: %v", err)
	}
	results, _ := res["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("want 1 finished result, got %v", res["results"])
	}
	first, _ := results[0].(map[string]any)
	if first["task_id"] != idA || first["text"] != "A" {
		t.Fatalf("finished result mismatch: %v", first)
	}
	pending, _ := res["pending"].([]any)
	if len(pending) != 1 || !strings.Contains(pending[0].(string), idB) {
		t.Fatalf("running task must be summarized in pending, got %v", res["pending"])
	}
	if tk, _ := reg.Get(idA); !tk.Claimed {
		t.Fatal("collect all must claim returned results")
	}
	// Second collect: nothing left to return.
	res, _ = callTool(t, d, "collect", map[string]any{})
	if results, _ := res["results"].([]any); len(results) != 0 {
		t.Fatalf("claimed results must not repeat, got %v", res["results"])
	}
}

// TestPulseKeepsMirrorFreshAndFlushesFinal: the mechanical pulse must mirror
// live board state with NO ollama involved (Task-9 closure), write one final
// frame on the live→idle transition, and then go quiet.
func TestPulseKeepsMirrorFreshAndFlushesFinal(t *testing.T) {
	oldTick := pulseTick
	pulseTick = 5 * time.Millisecond
	defer func() { pulseTick = oldTick }()

	path := filepath.Join(t.TempDir(), "mirror.json")
	board := activity.NewBoard()
	d := &conductorDeps{
		board:      board,
		reg:        newTaskRegistry(context.Background(), 4, board),
		mirror:     activity.MirrorThrottle(board, path, time.Millisecond),
		mirrorPath: path,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.pulse(ctx)

	board.Record("p1/claude", "reading files")
	waitFor(t, "live state mirrored mid-run", func() bool {
		states, _, err := activity.ReadMirror(path)
		return err == nil && len(states) == 1 && !states[0].Done && states[0].Last == "reading files"
	})

	board.Done("p1/claude", 3*time.Second)
	waitFor(t, "final flush shows done", func() bool {
		states, _, err := activity.ReadMirror(path)
		return err == nil && len(states) == 1 && states[0].Done
	})

	// Idle: no further writes.
	fi1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("pulse must not rewrite the mirror while everything is idle")
	}
}

// TestDispatchParallelCombinesResults: two read-risk tasks on different
// threads run concurrently and both results return inline, in input order.
func TestDispatchParallelCombinesResults(t *testing.T) {
	fastAwait(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "parallel answer")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{},
		reg:      newTaskRegistry(context.Background(), 4, nil),
	}
	res, err := callTool(t, d, "dispatch_parallel", map[string]any{
		"tasks": []map[string]any{
			{"project": "proj1", "thread": "scan-a", "cli": "claude", "message": "scan A", "risk": "read"},
			{"project": "proj1", "thread": "scan-b", "cli": "claude", "message": "scan B", "risk": "read"},
		},
	})
	if err != nil {
		t.Fatalf("dispatch_parallel: %v", err)
	}
	if res["detached"] != false {
		t.Fatalf("completed fan-out must not be detached: %v", res)
	}
	results, _ := res["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %v", res["results"])
	}
	for i, r := range results {
		m, _ := r.(map[string]any)
		if m["status"] != "done" || m["text"] != "parallel answer" {
			t.Fatalf("result %d mismatch: %v", i, m)
		}
	}
	// Both awaited tasks were claimed: nothing pending for collect.
	all, err := callTool(t, d, "collect", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := all["results"].([]any); len(got) != 0 {
		t.Fatalf("awaited fan-out results must be claimed, got %v", all["results"])
	}
}

// TestDispatchParallelPerTaskFailures: a bad task fails as a per-task entry;
// valid siblings still run. Whole-call errors are reserved for bad args.
func TestDispatchParallelPerTaskFailures(t *testing.T) {
	fastAwait(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "survivor")
	fakeSrc, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	copyExecutable(t, fakeSrc, filepath.Join(binDir, "claude"))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	projDir := t.TempDir()
	if err := config.SaveProjects([]config.Project{{ID: "proj1", Name: "proj1", Path: projDir}}); err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	d := &conductorDeps{
		a: &app{
			routing:  config.Routing{Brain: config.BrainConfig{ContextThresholdPct: 70}, Tiers: map[string]string{}},
			tracker:  bud,
			channels: map[string]channel.Channel{},
		},
		gate: shipgate.New(shipgate.ModeOff), emb: replEmbedder{},
		managers: map[string]*managed{},
		reg:      newTaskRegistry(context.Background(), 4, nil),
	}
	res, err := callTool(t, d, "dispatch_parallel", map[string]any{
		"tasks": []map[string]any{
			{"cli": "ollama", "message": "not allowed here", "risk": "read"},
			{"cli": "claude", "message": "ship it", "risk": "ship"},
			{"project": "proj1", "thread": "ok", "cli": "claude", "message": "fine", "risk": "read"},
		},
	})
	if err != nil {
		t.Fatalf("per-task failures must not fail the call: %v", err)
	}
	results, _ := res["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %v", res["results"])
	}
	r0, _ := results[0].(map[string]any)
	r1, _ := results[1].(map[string]any)
	r2, _ := results[2].(map[string]any)
	if r0["status"] != "error" || !strings.Contains(r0["error"].(string), "cli") {
		t.Fatalf("ollama task must fail per-task: %v", r0)
	}
	if r1["status"] != "error" || !strings.Contains(r1["error"].(string), "risk") {
		t.Fatalf("ship task must fail per-task: %v", r1)
	}
	if r2["status"] != "done" || r2["text"] != "survivor" {
		t.Fatalf("valid sibling must complete: %v", r2)
	}

	// Bad args (no tasks) IS a whole-call error.
	if _, err := callTool(t, d, "dispatch_parallel", map[string]any{"tasks": []map[string]any{}}); err == nil {
		t.Fatal("empty tasks must be a call error")
	}
}
