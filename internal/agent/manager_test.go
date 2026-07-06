package agent

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
)

type managerFixture struct {
	m   *Manager
	bud *budget.Tracker
	mem *memory.Store
}

// fixedEmbedder always returns the same vector so memory writes succeed.
type fixedEmbedder struct{}

func (fixedEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func newManagerFixture(t *testing.T, window int) *managerFixture {
	t.Helper()
	dir := t.TempDir()
	ts, err := LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	if err != nil {
		t.Fatal(err)
	}
	bud, err := budget.New(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	mem, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })

	return &managerFixture{
		m: &Manager{
			Project:  config.Project{Name: "testproj", Path: dir},
			Threads:  ts,
			Adapters: map[string]Adapter{"claude": &ClaudeAdapter{BinPath: fakeBin(t), Window: window}},
			Budget:   bud,
			Mem:      mem,
			Emb:      fixedEmbedder{},
			Summarize: func(_ context.Context, text string) (string, error) {
				return "summary: " + text[:min13(20, len(text))], nil
			},
			ThresholdPct: 70,
			DistillModel: "haiku",
			Timeout:      10 * time.Second,
		},
		bud: bud,
		mem: mem,
	}
}

func min13(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestDispatchRecordsRealUsage(t *testing.T) {
	t.Setenv("FAKEAGENT_IN", "5000")
	t.Setenv("FAKEAGENT_OUT", "300")
	f := newManagerFixture(t, 200000)

	res, err := f.m.Dispatch(context.Background(), DispatchSpec{
		CLI: "claude", Model: "sonnet", Message: "implement it",
	}, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if res.InputTokens != 5000 || res.OutputTokens != 300 {
		t.Errorf("usage = %+v", res)
	}
	st, err := f.bud.State(context.Background(), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionCount != 1 {
		t.Errorf("budget rows = %d, want 1 (real usage recorded)", st.SessionCount)
	}
	n, err := f.bud.ModelCount(context.Background(), "claude", "sonnet", budget.WindowWeek)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sonnet budget rows = %d, want 1", n)
	}
}

func TestDispatchDistillsAtThreshold(t *testing.T) {
	// Window 10000, threshold 70% -> 120+20=140... use big usage instead:
	// emit 9000 input tokens so 9000+200 > 7000 triggers distillation.
	t.Setenv("FAKEAGENT_IN", "9000")
	t.Setenv("FAKEAGENT_OUT", "200")
	t.Setenv("FAKEAGENT_TEXT", "handoff: decisions, files, in-flight")
	f := newManagerFixture(t, 10000)

	compacted := ""
	f.m.OnCompact = func(name string) { compacted = name }

	_, err := f.m.Dispatch(context.Background(), DispatchSpec{
		CLI: "claude", Model: "sonnet", Message: "big work",
	}, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	th := f.m.Threads.Get("claude", "claude")
	if th.SessionID != "" {
		t.Errorf("SessionID = %q, want cleared after distill", th.SessionID)
	}
	if th.LastDistillation == "" {
		t.Error("LastDistillation empty after distill")
	}
	if compacted != "claude" {
		t.Errorf("OnCompact got %q", compacted)
	}
	// Distillation saved to memory.
	items, err := f.mem.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.Kind == memory.KindDistillation {
			found = true
		}
	}
	if !found {
		t.Error("no distillation item written to memory")
	}
}

func TestDispatchRecoversFromDeadSession(t *testing.T) {
	t.Setenv("FAKEAGENT_FAIL_RESUME", "1")
	f := newManagerFixture(t, 200000)
	th := f.m.Threads.Get("claude", "claude")
	th.SessionID = "dead-session"
	th.LastDistillation = "we had decided to use sqlite"

	log := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKEAGENT_ARGS_LOG", log)

	res, err := f.m.Dispatch(context.Background(), DispatchSpec{
		CLI: "claude", Model: "sonnet", Message: "keep going",
	}, nil)
	if err != nil {
		t.Fatalf("Dispatch should recover, got: %v", err)
	}
	if res.Text == "" {
		t.Error("empty result after recovery")
	}
	b, _ := os.ReadFile(log)
	calls := strings.TrimSpace(string(b))
	if !strings.Contains(calls, "--resume dead-session") {
		t.Errorf("first call did not try resume:\n%s", calls)
	}
	if !strings.Contains(calls, "we had decided to use sqlite") {
		t.Errorf("recovery call not seeded with last distillation:\n%s", calls)
	}
}

func TestDispatchRecordsProjectAndRunID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.db")
	bud, err := budget.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer bud.Close()
	threads, _ := LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	m := &Manager{
		Project:   config.Project{ID: "pid123", Name: "proj", Path: dir},
		ProjectID: "pid123",
		RunID:     "run-xyz",
		Threads:   threads,
		Adapters:  map[string]Adapter{"claude": &ClaudeAdapter{BinPath: fakeBin(t)}},
		Budget:    bud,
		Timeout:   10 * time.Second,
	}
	t.Setenv("FAKEAGENT_TEXT", "ok")
	if _, err := m.Dispatch(context.Background(), DispatchSpec{Thread: "claude", CLI: "claude", Message: "hi"}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Read the row back via an independent connection (driver registered by the budget import).
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var project, runID string
	row := db.QueryRowContext(context.Background(),
		`SELECT project, run_id FROM usage ORDER BY ts DESC LIMIT 1`)
	if err := row.Scan(&project, &runID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if project != "pid123" || runID != "run-xyz" {
		t.Errorf("got (%q,%q), want (pid123, run-xyz)", project, runID)
	}
}

func TestDispatchRendersExtraRoots(t *testing.T) {
	dir := t.TempDir()
	threads, _ := LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	argsLog := filepath.Join(dir, "args.log")
	m := &Manager{
		Project:  config.Project{Name: "proj", Path: dir},
		Threads:  threads,
		Adapters: map[string]Adapter{"claude": &ClaudeAdapter{BinPath: fakeBin(t)}},
		Timeout:  10 * time.Second,
	}
	t.Setenv("FAKEAGENT_TEXT", "ok")
	t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)
	if _, err := m.Dispatch(context.Background(), DispatchSpec{
		Thread: "claude", CLI: "claude", Message: "hi",
		ExtraRoots: []string{"/repos/other"},
	}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	b, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(b), "--add-dir") || !strings.Contains(string(b), "/repos/other") {
		t.Errorf("expected --add-dir /repos/other in argv, got: %s", b)
	}
}

func TestSeedMessage(t *testing.T) {
	f := newManagerFixture(t, 200000)
	ad := f.m.Adapters["claude"]

	t.Run("fresh thread gets role line", func(t *testing.T) {
		th := &Thread{Name: "claude", CLI: "claude"}
		got := f.m.seedMessage(th, ad, "hello")
		if !strings.Contains(got, "testproj") || !strings.Contains(got, "hello") {
			t.Errorf("seed = %q", got)
		}
	})
	t.Run("live session passes through", func(t *testing.T) {
		th := &Thread{Name: "claude", CLI: "claude", SessionID: "live", Turns: 2}
		if got := f.m.seedMessage(th, ad, "hello"); got != "hello" {
			t.Errorf("seed = %q, want passthrough", got)
		}
	})
	t.Run("restarted thread seeded with distillation", func(t *testing.T) {
		th := &Thread{Name: "claude", CLI: "claude", Turns: 5, LastDistillation: "decided X"}
		got := f.m.seedMessage(th, ad, "continue")
		if !strings.Contains(got, "decided X") {
			t.Errorf("seed = %q", got)
		}
	})
	t.Run("plain adapter seeded with rolling summary", func(t *testing.T) {
		plain := NewCodexAdapter()
		th := &Thread{Name: "codex", CLI: "codex", Summary: "earlier we tried Y"}
		got := f.m.seedMessage(th, plain, "next step")
		if !strings.Contains(got, "earlier we tried Y") || !strings.Contains(got, "next step") {
			t.Errorf("seed = %q", got)
		}
	})
}

func TestStatusLinesEmptyIsNotNil(t *testing.T) {
	m := &Manager{Threads: &ThreadStore{Threads: map[string]*Thread{}}}
	got := m.StatusLines()
	if got == nil {
		t.Fatal("StatusLines() with no threads must return [], not nil (JSON null breaks MCP consumers)")
	}
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %v", got)
	}
}
