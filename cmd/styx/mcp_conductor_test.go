package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
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

func TestPipelineRunRejectsUnknown(t *testing.T) {
	d := &conductorDeps{gate: shipgate.New(shipgate.ModeOff)}
	if _, err := callTool(t, d, "pipeline_run", map[string]any{"pipeline": "yolo"}); err == nil {
		t.Fatal("unknown pipeline must error")
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

	// Both landed in global.db — not a project store (note: no project was
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
	if len(items) != 2 {
		t.Fatalf("want 2 global items, got %d", len(items))
	}
	kinds := map[memory.Kind]bool{items[0].Kind: true, items[1].Kind: true}
	if !kinds[memory.KindRetrospective] || !kinds[memory.KindUserPreference] {
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

func TestDispatchSyncBusyThreadGuard(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4, nil)
	run1, started1, release1 := blockingRun(nil)
	reg.Spawn(taskSpec{ProjectID: "proj1", Thread: "codex", CLI: "codex", Risk: "edit"}, run1)
	<-started1
	defer close(release1)

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
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
		managers: map[string]*managed{}, reg: reg,
	}
	_, err = callTool(t, d, "dispatch", map[string]any{
		"project": "proj1", "thread": "codex", "cli": "codex", "message": "hi", "risk": "read",
	})
	if err == nil || !strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), "t1") {
		t.Fatalf("sync dispatch to a thread with a live background task must error naming it, got %v", err)
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
