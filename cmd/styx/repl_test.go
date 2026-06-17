package main

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/audit"
	"github.com/ishaanbatra/styx/internal/brain"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
)

// scriptedBrain returns queued actions (or an error) in order.
type scriptedBrain struct {
	actions []brain.Action
	err     error
	i       int
}

func (s *scriptedBrain) Decide(context.Context, brain.Turn) (brain.Action, error) {
	if s.err != nil {
		return brain.Action{}, s.err
	}
	a := s.actions[s.i]
	if s.i < len(s.actions)-1 {
		s.i++
	}
	return a, nil
}

type replEmbedder struct{}

func (replEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

func newTestSession(t *testing.T, b brain.Brain, input string) (*replSession, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	mem, err := memory.Open(filepath.Join(dir, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mem.Close() })
	glob, err := memory.Open(filepath.Join(dir, "glob.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { glob.Close() })
	bud, err := budget.New(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bud.Close() })
	al, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	threads, err := agent.LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	if err != nil {
		t.Fatal(err)
	}
	fake, err := filepath.Abs("../../testdata/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	s := &replSession{
		proj:  config.Project{Name: "testproj", Path: dir},
		brain: b,
		mgr: &agent.Manager{
			Project:      config.Project{Name: "testproj", Path: dir},
			Threads:      threads,
			Adapters:     map[string]agent.Adapter{"claude": &agent.ClaudeAdapter{BinPath: fake}},
			Budget:       bud,
			Mem:          mem,
			Emb:          replEmbedder{},
			ThresholdPct: 70,
			DistillModel: "haiku",
			Timeout:      10 * time.Second,
		},
		mem:      mem,
		glob:     glob,
		emb:      replEmbedder{},
		audit:    al,
		tiers:    map[string]string{"fable": "fable", "opus": "opus", "sonnet": "sonnet", "haiku": "haiku"},
		fableCap: 2,
		tracker:  bud,
		pipelines: map[string]func(context.Context, string) error{
			"research": func(context.Context, string) error { out.WriteString("[pipeline research ran]\n"); return nil },
		},
		in:  bufio.NewReader(strings.NewReader(input)),
		out: out,
	}
	return s, out
}

func TestTurnReply(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionReply, Reply: "two threads are live", Confidence: 0.9}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "what's running?"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !strings.Contains(out.String(), "two threads are live") {
		t.Errorf("output = %q", out.String())
	}
}

func TestTurnDispatchPrintsRoutingLineAndResult(t *testing.T) {
	t.Setenv("FAKEAGENT_TEXT", "refactor done")
	b := &scriptedBrain{actions: []brain.Action{{
		Action:     brain.ActionDispatch,
		Dispatches: []brain.Dispatch{{Thread: "claude", Model: "sonnet", Message: "refactor it", Rationale: "implementation work"}},
		Confidence: 0.9,
	}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "refactor the loader"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "◆ claude·sonnet › implementation work") {
		t.Errorf("missing routing line:\n%s", got)
	}
	if !strings.Contains(got, "refactor done") {
		t.Errorf("missing agent result:\n%s", got)
	}
}

func TestTurnRememberStoresRoutingPreference(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{
		Action: brain.ActionRemember, Remember: "routing-preference: codex handles reviews", Confidence: 1,
	}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "no, codex should review"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	items, err := s.mem.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Kind != memory.KindRoutingPreference {
		t.Errorf("memory = %+v", items)
	}
	if !strings.Contains(out.String(), "remembered") {
		t.Errorf("no confirmation printed: %q", out.String())
	}
}

func TestTurnPipeline(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionPipeline, Pipeline: "research", Confidence: 0.9}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "research sync engines"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !strings.Contains(out.String(), "[pipeline research ran]") {
		t.Errorf("pipeline not invoked: %q", out.String())
	}
}

func TestTurnBrainDownAsksUser(t *testing.T) {
	t.Setenv("FAKEAGENT_TEXT", "manual route ok")
	b := &scriptedBrain{err: brain.ErrNeedUser}
	s, out := newTestSession(t, b, "claude\n")
	if err := s.turn(context.Background(), "do the thing"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "which thread") {
		t.Errorf("REPL did not ask the user:\n%s", got)
	}
	if !strings.Contains(got, "manual route ok") {
		t.Errorf("manual dispatch did not run:\n%s", got)
	}
}

func TestResolveModelFableDegradation(t *testing.T) {
	b := &scriptedBrain{}
	s, _ := newTestSession(t, b, "")
	// Below cap: fable passes through.
	if m, degraded := s.resolveModel("fable"); m != "fable" || degraded {
		t.Errorf("cold: model=%q degraded=%v", m, degraded)
	}
	// Record fableCap (=2) fable messages this week.
	for i := 0; i < 2; i++ {
		if err := s.tracker.Record(context.Background(), budget.Event{Channel: "claude", Verb: "thread", Model: "fable", Success: true}); err != nil {
			t.Fatal(err)
		}
	}
	if m, degraded := s.resolveModel("fable"); m != "opus" || !degraded {
		t.Errorf("hot: model=%q degraded=%v, want opus/true", m, degraded)
	}
	// Non-tier strings pass through untouched (ollama model names).
	if m, _ := s.resolveModel("qwen2.5-coder:14b"); m != "qwen2.5-coder:14b" {
		t.Errorf("passthrough = %q", m)
	}
}

func TestShipRiskConfirmationDeclined(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionPipeline, Pipeline: "auto", Confidence: 0.9}}}
	s, out := newTestSession(t, b, "n\n")
	ranAuto := false
	s.pipelines = map[string]func(ctx context.Context, arg string) error{
		"auto": func(ctx context.Context, _ string) error { ranAuto = true; return nil },
	}
	if err := s.turn(context.Background(), "ship the rate limiting feature"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if ranAuto {
		t.Error("auto pipeline ran despite declined ship-risk confirmation")
	}
	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("expected cancellation notice:\n%s", out.String())
	}
}

func TestShipRiskAutoApproved(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionPipeline, Pipeline: "auto", Confidence: 0.9}}}
	s, _ := newTestSession(t, b, "")
	s.assumeYes = true
	ranAuto := false
	s.pipelines = map[string]func(ctx context.Context, arg string) error{
		"auto": func(ctx context.Context, _ string) error { ranAuto = true; return nil },
	}
	if err := s.turn(context.Background(), "ship it"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	if !ranAuto {
		t.Error("auto pipeline did not run under assumeYes")
	}
}

func TestRoutingPreferenceIsLowConfidenceAndScoped(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionRemember,
		Remember: "routing-preference: codex handles algorithm reviews; scope: reviews", Confidence: 1}}}
	s, _ := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "no, codex should do reviews"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	items, err := s.mem.All(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	it := items[0]
	if it.Kind != memory.KindRoutingPreference {
		t.Errorf("kind = %s", it.Kind)
	}
	if it.Confidence >= 1 {
		t.Errorf("routing-pref confidence = %v, want < 1", it.Confidence)
	}
	if it.Scope != "reviews" {
		t.Errorf("scope = %q, want reviews", it.Scope)
	}
}

func TestAuditTrail(t *testing.T) {
	b := &scriptedBrain{actions: []brain.Action{{Action: brain.ActionReply, Reply: "hi", Confidence: 0.9}}}
	s, out := newTestSession(t, b, "")
	if err := s.turn(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if quit := s.slash("/audit"); quit {
		t.Fatal("/audit should not quit")
	}
	got := out.String()
	for _, want := range []string{"turn", "decision"} {
		if !strings.Contains(got, want) {
			t.Errorf("/audit output missing %q:\n%s", want, got)
		}
	}
}
