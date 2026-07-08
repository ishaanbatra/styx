package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
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

func bindTestProject(t *testing.T, name string, bud *budget.Tracker) *boundProject {
	t.Helper()
	dir := t.TempDir()
	mem, err := memory.Open(filepath.Join(dir, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	threads, _ := agent.LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	fake, _ := filepath.Abs("../../testdata/fakeagent")
	p := config.Project{ID: name, Name: name, Path: dir}
	return &boundProject{
		proj: p,
		mem:  mem,
		mgr: &agent.Manager{
			Project: p, ProjectID: name, Threads: threads,
			Adapters: map[string]agent.Adapter{"claude": &agent.ClaudeAdapter{BinPath: fake}},
			Budget:   bud, Mem: mem, Emb: replEmbedder{},
			ThresholdPct: 70, DistillModel: "haiku", Timeout: 10 * time.Second,
		},
		closers: []func() error{mem.Close},
	}
}

func newTestSession(t *testing.T, b brain.Brain, input string) (*replSession, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
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
	bp := bindTestProject(t, "testproj", bud)
	t.Cleanup(func() {
		for _, c := range bp.closers {
			_ = c()
		}
	})
	out := &bytes.Buffer{}
	s := &replSession{
		bound:    map[string]*boundProject{"testproj": bp},
		focus:    "testproj",
		brain:    b,
		glob:     glob,
		emb:      replEmbedder{},
		audit:    al,
		tiers:    map[string]string{"fable": "fable", "opus": "opus", "sonnet": "sonnet", "haiku": "haiku"},
		fableCap: 2,
		tracker:  bud,
		pipelines: map[string]func(context.Context, string) error{
			"research": func(context.Context, string) error { out.WriteString("[pipeline research ran]\n"); return nil },
		},
		in:    bufio.NewReader(strings.NewReader(input)),
		out:   out,
		board: activity.NewBoard(),
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
	items, err := s.mem().All(context.Background())
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
	items, err := s.mem().All(context.Background())
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

// TestWatchCommandRendersBoard proves /watch renders the session board's live
// agent state (and, when set, the watcher note) through s.println.
func TestWatchCommandRendersBoard(t *testing.T) {
	b := &scriptedBrain{}
	s, out := newTestSession(t, b, "")
	s.board.Record("claude", "Bash: go test ./...")
	s.board.SetWatcherNote("both agents look healthy")
	if quit := s.slash("/watch"); quit {
		t.Fatal("/watch should not quit")
	}
	got := out.String()
	if !strings.Contains(got, "claude") {
		t.Errorf("/watch did not render agent label:\n%s", got)
	}
	if !strings.Contains(got, "Bash: go test") {
		t.Errorf("/watch did not render last action:\n%s", got)
	}
	if !strings.Contains(got, "both agents look healthy") {
		t.Errorf("/watch did not render watcher note:\n%s", got)
	}
}

// TestWatchCommandEmptyBoard proves /watch on a fresh session nudges the user
// rather than printing nothing.
func TestWatchCommandEmptyBoard(t *testing.T) {
	b := &scriptedBrain{}
	s, out := newTestSession(t, b, "")
	if quit := s.slash("/watch"); quit {
		t.Fatal("/watch should not quit")
	}
	if !strings.Contains(out.String(), "no agent activity yet") {
		t.Errorf("/watch on empty board should nudge:\n%s", out.String())
	}
}

func TestDispatchTargetsNamedRepoWithExtraRoots(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "done")

	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "ai-ta-backend", bud)
	b := bindTestProject(t, "ai-ta-teacher-ui", bud)
	// Register both so target.Resolve / extra-root resolution find them.
	if err := config.SaveProjects([]config.Project{
		{ID: "ai-ta-backend", Name: "ai-ta-backend", Path: a.proj.Path},
		{ID: "ai-ta-teacher-ui", Name: "ai-ta-teacher-ui", Path: b.proj.Path},
	}); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	s := &replSession{
		bound:   map[string]*boundProject{"ai-ta-backend": a, "ai-ta-teacher-ui": b},
		focus:   "ai-ta-backend",
		emb:     replEmbedder{},
		tracker: bud,
		tiers:   map[string]string{"opus": "opus", "haiku": "haiku"},
		in:      bufio.NewReader(strings.NewReader("")),
		out:     out,
	}
	d := brain.Dispatch{
		Thread: "claude", Message: "trace upload",
		Project: "ai-ta-teacher-ui", ExtraRoots: []string{"ai-ta-backend"},
	}
	if _, err := s.runOneDispatch(context.Background(), d, "opus", false); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// The teacher-ui thread ran; backend's did not.
	if b.mgr.Threads.Get("claude", "claude").Turns != 1 {
		t.Errorf("teacher-ui thread did not run")
	}
	if a.mgr.Threads.Get("claude", "claude").Turns != 0 {
		t.Errorf("backend thread should be untouched")
	}
}

func TestDispatchUnknownTargetSurfacesError(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())

	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "backend", bud)
	_ = config.SaveProjects([]config.Project{{ID: "backend", Name: "backend", Path: a.proj.Path}})

	s := &replSession{
		bound: map[string]*boundProject{"backend": a}, focus: "backend",
		emb: replEmbedder{}, tracker: bud,
		tiers: map[string]string{"opus": "opus"},
		in:    bufio.NewReader(strings.NewReader("")), out: &bytes.Buffer{},
	}
	_, err := s.runOneDispatch(context.Background(), brain.Dispatch{
		Thread: "claude", Message: "x", Project: "nope-not-registered",
	}, "opus", false)
	if err == nil || !strings.Contains(err.Error(), "unknown project") {
		t.Fatalf("want unknown-project error, got %v", err)
	}
	if !errors.Is(err, errUnresolvedRepo) {
		t.Errorf("error should wrap errUnresolvedRepo so the turn loop escalates")
	}
	if a.mgr.Threads.Get("claude", "claude").Turns != 0 {
		t.Errorf("no thread should run on an unresolved target (no silent fallback to focus)")
	}
}

// TestScriptedSession drives several turns through one session end-to-end:
// reply -> dispatch (fake CLI) -> remember -> recall influences the next turn.
func TestScriptedSession(t *testing.T) {
	t.Setenv("FAKEAGENT_TEXT", "implemented and tested")
	t.Setenv("FAKEAGENT_SESSION", "sess-e2e")
	b := &scriptedBrain{actions: []brain.Action{
		{Action: brain.ActionReply, Reply: "hello! ready to work", Confidence: 0.95},
		{Action: brain.ActionDispatch, Confidence: 0.9,
			Dispatches: []brain.Dispatch{{Thread: "claude", Model: "sonnet", Message: "add retry logic", Rationale: "implementation"}}},
		{Action: brain.ActionRemember, Remember: "we retry 3 times with backoff", Confidence: 1},
	}}
	s, out := newTestSession(t, b, "")
	ctx := context.Background()

	for _, utterance := range []string{
		"hey styx",
		"add retry logic to the loader",
		"remember: we retry 3 times with backoff",
	} {
		if err := s.turn(ctx, utterance); err != nil {
			t.Fatalf("turn %q: %v", utterance, err)
		}
	}

	got := out.String()
	for _, want := range []string{
		"hello! ready to work",
		"◆ claude·sonnet › implementation",
		"implemented and tested",
		"◆ remembered",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("session output missing %q:\n%s", want, got)
		}
	}

	// The dispatch persisted a durable thread with the CLI's session id.
	th := s.mgr().Threads.Get("claude", "claude")
	if th.SessionID != "sess-e2e" || th.Turns != 1 {
		t.Errorf("thread after session = %+v", th)
	}
	// The remember landed in project memory and is recallable.
	hits, err := memory.Recall(ctx, s.emb, "what's our retry policy?", 1, s.mem())
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Item.Text, "retry 3 times") {
		t.Errorf("recall = %+v", hits)
	}
	// /why explains the last decision.
	if !strings.Contains(s.lastActionJSON(), "remember") {
		t.Errorf("lastActionJSON = %s", s.lastActionJSON())
	}
	// /status shows the live thread with a context meter.
	if quit := s.slash("/status"); quit {
		t.Fatal("/status should not quit")
	}
	if !strings.Contains(out.String(), "claude (claude): 1 turns") {
		t.Errorf("status output missing thread line:\n%s", out.String())
	}
}

func TestRecallSpansBoundRepos(t *testing.T) {
	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "A", bud)
	b := bindTestProject(t, "B", bud)
	glob, _ := memory.Open(filepath.Join(t.TempDir(), "glob.db"))

	s := &replSession{
		bound: map[string]*boundProject{"A": a, "B": b},
		focus: "A",
		glob:  glob,
		emb:   replEmbedder{},
	}
	// A fact learned in repo B.
	vec, _ := s.emb.Embed(context.Background(), "the embedding worker lives in B")
	if _, err := b.mem.Add(context.Background(), memory.Item{
		Kind: memory.KindFact, Text: "the embedding worker lives in B",
		Project: "B", Embedding: vec, Confidence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Recall from focus A must surface B's fact.
	hits, err := s.recallAll(context.Background(), "where is the embedding worker")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.Item.Text, "embedding worker lives in B") {
			found = true
		}
	}
	if !found {
		t.Errorf("cross-repo recall did not surface B's fact: %+v", hits)
	}
}

func TestAllReposResolve(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if err := config.SaveProjects([]config.Project{
		{ID: "alpha", Name: "alpha", Path: t.TempDir()},
		{ID: "beta", Name: "beta", Path: t.TempDir()},
	}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name   string
		tokens []string
		wantOK bool
	}{
		{"all resolve", []string{"alpha", "beta"}, true},
		{"single resolves", []string{"alpha"}, true},
		{"one token unknown", []string{"alpha", "nope-xyz"}, false},
		{"utterance (none resolve)", []string{"fix", "the", "bug"}, false},
		{"empty", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := allReposResolve(tt.tokens)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (got=%v)", ok, tt.wantOK, got)
			}
			if ok && len(got) != len(tt.tokens) {
				t.Errorf("returned tokens = %v, want len %d", got, len(tt.tokens))
			}
			if !ok && got != nil {
				t.Errorf("expected nil tokens on false, got %v", got)
			}
		})
	}
}

func TestFocusSlashResolvesAndFailsLoud(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "alpha", bud)
	b := bindTestProject(t, "beta", bud)
	if err := config.SaveProjects([]config.Project{
		{ID: "alpha", Name: "alpha", Path: a.proj.Path},
		{ID: "beta", Name: "beta", Path: b.proj.Path},
	}); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	s := &replSession{
		bound: map[string]*boundProject{"alpha": a, "beta": b}, focus: "alpha",
		emb: replEmbedder{}, tracker: bud,
		in: bufio.NewReader(strings.NewReader("")), out: out,
	}
	// Known name flips focus.
	if quit := s.slash("/focus beta"); quit {
		t.Fatal("/focus should not quit")
	}
	if s.focus != "beta" {
		t.Errorf("focus = %q, want beta", s.focus)
	}
	// Unresolved name: focus unchanged, error surfaced.
	if quit := s.slash("/focus nope-xyz"); quit {
		t.Fatal("/focus should not quit")
	}
	if s.focus != "beta" {
		t.Errorf("focus changed on unresolved name: %q", s.focus)
	}
	if !strings.Contains(out.String(), "focus:") {
		t.Errorf("no error surfaced for unresolved /focus: %q", out.String())
	}
	// Missing argument: usage message, no quit, focus unchanged.
	if quit := s.slash("/focus"); quit {
		t.Fatal("/focus should not quit")
	}
	if s.focus != "beta" {
		t.Errorf("focus changed on usage error: %q", s.focus)
	}
}

func TestReposListsBoundWithFocusMarker(t *testing.T) {
	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "alpha", bud)
	b := bindTestProject(t, "beta", bud)
	out := &bytes.Buffer{}
	s := &replSession{
		bound: map[string]*boundProject{"alpha": a, "beta": b}, focus: "alpha",
		in: bufio.NewReader(strings.NewReader("")), out: out,
	}
	if quit := s.slash("/repos"); quit {
		t.Fatal("/repos should not quit")
	}
	got := out.String()
	if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
		t.Errorf("/repos missing a bound repo:\n%s", got)
	}
	if !strings.Contains(got, "→") {
		t.Errorf("/repos missing focus marker:\n%s", got)
	}
}

func TestTwoRepoScriptedSession(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "traced")
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)

	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "backend", bud)
	b := bindTestProject(t, "teacher", bud)
	_ = config.SaveProjects([]config.Project{
		{ID: "backend", Name: "backend", Path: a.proj.Path},
		{ID: "teacher", Name: "teacher", Path: b.proj.Path},
	})
	out := &bytes.Buffer{}
	s := &replSession{
		bound: map[string]*boundProject{"backend": a, "teacher": b},
		focus: "backend", emb: replEmbedder{}, tracker: bud,
		tiers: map[string]string{"opus": "opus", "haiku": "haiku"},
		in:    bufio.NewReader(strings.NewReader("")), out: out,
	}
	d := brain.Dispatch{Thread: "claude", Message: "trace upload", Project: "teacher", ExtraRoots: []string{"backend"}}
	if _, err := s.runOneDispatch(context.Background(), d, "opus", false); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(log), "--add-dir") || !strings.Contains(string(log), a.proj.Path) {
		t.Errorf("backend not attached to teacher dispatch: %s", log)
	}
	if b.mgr.Threads.Get("claude", "claude").Turns != 1 {
		t.Errorf("teacher thread did not run")
	}
}
