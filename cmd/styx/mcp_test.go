package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/intel"
	"github.com/ishaanbatra/styx/internal/mcpserver"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/router"
)

func testRouterAndTracker(t *testing.T) (*router.Router, *budget.Tracker) {
	t.Helper()
	tr, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	r := router.FromConfig(config.Routing{
		Rules: []config.Rule{{Verb: "build", Use: "codex:gpt-5"}},
	}, tr)
	return r, tr
}

func TestHandleRoute_ReturnsChannelModelReasoningBudget(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	res, err := handleRoute(context.Background(), r, tr, routeArgs{Task: "add dark mode", Verb: "build"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Channel != "codex" || res.Model != "gpt-5" {
		t.Fatalf("got %s:%s, want codex:gpt-5", res.Channel, res.Model)
	}
	if res.Reasoning == "" {
		t.Error("expected non-empty reasoning")
	}
	if res.Budget.Channel != "codex" {
		t.Errorf("budget snapshot channel = %q, want codex", res.Budget.Channel)
	}
}

func TestHandleRoute_RequiresTask(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	if _, err := handleRoute(context.Background(), r, tr, routeArgs{}); err == nil {
		t.Fatal("expected error when task is empty")
	}
}

func TestHandleBudgetStatus_AllChannels(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	out, err := handleBudgetStatus(context.Background(), tr, budgetStatusArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 4 {
		t.Fatalf("want 4 channels, got %d", len(out))
	}
}

func TestHandleBudgetStatus_SingleChannel(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	out, err := handleBudgetStatus(context.Background(), tr, budgetStatusArgs{Channel: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Channel != "claude" {
		t.Fatalf("want [claude], got %+v", out)
	}
}

func TestHandleRecordUsage_AppendsAndReflects(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	ctx := context.Background()
	res, err := handleRecordUsage(ctx, tr, recordUsageArgs{Channel: "claude", Messages: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Recorded {
		t.Fatal("expected recorded=true")
	}
	st, err := tr.State(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.SessionCount != 3 {
		t.Fatalf("tracker session count = %d, want 3", st.SessionCount)
	}
	if res.Budget.SessionCount != 3 {
		t.Fatalf("result budget session count = %d, want 3", res.Budget.SessionCount)
	}
}

func TestHandleRecordUsage_RequiresChannel(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	if _, err := handleRecordUsage(context.Background(), tr, recordUsageArgs{}); err == nil {
		t.Fatal("expected error for missing channel")
	}
}

func TestBudgetSnapshotFor_StaleOnError(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	tr.Close() // closing the tracker forces State() to error
	snap := budgetSnapshotFor(context.Background(), tr, "codex")
	if !snap.Stale {
		t.Fatal("expected Stale=true after tracker closed")
	}
	if snap.Channel != "codex" {
		t.Fatalf("channel = %q, want codex", snap.Channel)
	}
}

func TestMCPTools_EndToEndRoute(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	a := &app{router: r, tracker: tr}
	srv := mcpserver.New("styx", "test", mcpTools(a))
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"route","arguments":{"task":"add dark mode","verb":"build"}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, `"route"`) || !strings.Contains(s, `"budget_status"`) || !strings.Contains(s, `"record_usage"`) {
		t.Fatalf("tools/list missing a tool: %s", s)
	}
	if !strings.Contains(s, "codex") {
		t.Fatalf("route call did not return codex: %s", s)
	}
}

func TestHandleRoute_V2Fields_ComplexClassifiedAndFloor(t *testing.T) {
	r, tr := testRouterAndTracker(t) // rule: {Verb:"build", Use:"codex:gpt-5"} per v1 fixture
	ctx := context.Background()
	// No signals passed: handler must classify. "refactor" yields the "complex" signal.
	res, err := handleRoute(ctx, r, tr, routeArgs{Task: "refactor the auth architecture", Verb: "build"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range res.ClassifiedSignals {
		if s == "complex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("classified_signals = %v, want to include 'complex'", res.ClassifiedSignals)
	}
	if res.Floor != "sonnet" {
		t.Fatalf("floor = %q, want sonnet for a complex task", res.Floor)
	}
	if res.TierPlan == nil || res.TierPlan.Chosen == "" {
		t.Fatalf("tier_plan missing: %+v", res.TierPlan)
	}
}

func TestHandleRoute_V2_BlockedByBudgetSetsRetryAfter(t *testing.T) {
	tr, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	// A plan+complex rule whose only floor-clearing targets (claude, codex) are
	// both over cap; cooldown gives a concrete retry hint.
	ctx := context.Background()
	if err := tr.MarkCooldown(ctx, "claude", time.Now().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r := router.FromConfig(config.Routing{
		Budget: config.BudgetCaps{
			Claude: config.ChannelCap{CapPct: 80},
			Codex:  config.ChannelCap{CapPct: 80},
		},
		Rules: []config.Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus", Fallback: []string{"codex"}},
		},
	}, overCapBudget{}) // both claude+codex reported over cap; see helper below
	res, err := handleRoute(ctx, r, tr, routeArgs{Task: "redesign the whole thing", Verb: "plan", Signals: []string{"complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.BlockedByBudget {
		t.Fatalf("blocked_by_budget = false, want true when all capable channels over cap")
	}
	if res.Channel == "ollama" {
		t.Fatal("blocked route returned below-floor ollama")
	}
	if res.RetryAfterS <= 0 {
		t.Fatalf("retry_after_s = %d, want > 0 (claude cooldown ~30m)", res.RetryAfterS)
	}
}

// overCapBudget reports every channel as 100% used so overCap() fires.
type overCapBudget struct{}

func (overCapBudget) UsedPct(ctx context.Context, channel string) (float64, error) { return 100, nil }

func TestHandleChannelHealth_AllAndSingle(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	ctx := context.Background()
	for _, kind := range []string{"5xx", "5xx", "killed"} {
		if err := tr.Record(ctx, budget.Event{Channel: "claude", Verb: "plan", Success: false, ErrorKind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := handleChannelHealth(ctx, tr, channelHealthArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(defaultChannelNames) {
		t.Fatalf("got %d channels, want %d", len(all), len(defaultChannelNames))
	}
	var claude *channelHealthResult
	for i := range all {
		if all[i].Channel == "claude" {
			claude = &all[i]
		}
	}
	if claude == nil || !claude.CircuitOpen || claude.FailuresRecent != 3 ||
		claude.ErrorKinds["server"] != 2 || claude.ErrorKinds["killed"] != 1 {
		t.Fatalf("claude health wrong: %+v", claude)
	}

	one, err := handleChannelHealth(ctx, tr, channelHealthArgs{Channel: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Channel != "codex" || one[0].CircuitOpen {
		t.Fatalf("single-channel health wrong: %+v", one)
	}
}

// syncBuffer is a concurrency-safe bytes.Buffer. Since Task 1
// (concurrent tools/call), a Serve response can land while the test's input
// writer is still running on another goroutine, so tests that peek at
// output before Serve returns need a safe accessor.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMCPTools_EndToEndChannelHealth(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	a := &app{router: r, tracker: tr}
	srv := mcpserver.New("styx", "test", mcpTools(a))

	// channel_health's handler runs a real, ctx-aware sqlite query. Since
	// Task 1, tools/call runs on its own goroutine and EOF cancels every
	// in-flight call, so writing the whole script and closing the reader
	// immediately (as a plain strings.Reader would) races the dispatched
	// goroutine and can cancel the query before it starts — a real host's
	// stdin pipe would instead block until it actually disconnects. Model
	// that here with io.Pipe: keep it open until the response is observed,
	// then close it (EOF) to let Serve return.
	pr, pw := io.Pipe()
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(context.Background(), pr, out) }()

	go func() {
		for _, line := range []string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
			`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"channel_health","arguments":{}}}`,
		} {
			if _, err := io.WriteString(pw, line+"\n"); err != nil {
				return
			}
		}
	}()

	deadline := time.After(5 * time.Second)
	for !strings.Contains(out.String(), `"id":2`) {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for channel_health response")
		case <-time.After(time.Millisecond):
		}
	}
	pw.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	// The tools/call payload is JSON-marshaled and then embedded as a "text"
	// string field, so the outer encoder escapes its quotes (e.g. \"circuit_open\").
	// Match the bare field name, consistent with TestMCPTools_EndToEndRoute's
	// same workaround for tool-call (not tools/list) output.
	s := out.String()
	if !strings.Contains(s, "circuit_open") || !strings.Contains(s, "error_kinds") {
		t.Fatalf("channel_health output missing fields:\n%s", s)
	}
}

func TestResolveProjectStrict_Errors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if _, err := resolveProjectStrict(""); err == nil {
		t.Fatal("empty project accepted; want required-error")
	}
	_, err := resolveProjectStrict("definitely-not-a-registered-project")
	if err == nil || !strings.Contains(err.Error(), "unknown project") {
		t.Fatalf("unknown project err = %v, want 'unknown project'", err)
	}
	var ce *channel.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("err not classified: %v", err)
	}
}

func TestHandleGetIntel_WholeSectionAndStale(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := config.Project{ID: "demo", Name: "demo", Path: t.TempDir(), Language: "go"}
	idx := &intel.Index{
		Project: "demo", Path: proj.Path, Language: "go",
		BuiltAt:       time.Now().UTC(),
		SchemaVersion: 1,
		Conventions:   intel.Conventions{TestFramework: "go test"},
		KeySymbols:    []intel.KeySymbol{{Name: "Router", File: "router.go", Why: "central"}},
	}
	if err := intel.Save(proj, idx); err != nil {
		t.Fatal(err)
	}
	// whole index
	whole, err := handleGetIntel(context.Background(), proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if whole.Index == nil || whole.Index.Conventions.TestFramework != "go test" {
		t.Fatalf("whole index missing conventions: %+v", whole.Index)
	}
	if whole.Stale {
		t.Fatalf("just-built index reported stale: %q", whole.StalenessReason)
	}
	// section filter
	sec, err := handleGetIntel(context.Background(), proj, "key_symbols")
	if err != nil {
		t.Fatal(err)
	}
	if sec.Index != nil || len(sec.KeySymbols) != 1 || sec.KeySymbols[0].Name != "Router" {
		t.Fatalf("key_symbols section wrong: %+v", sec)
	}
	// unknown section
	if _, err := handleGetIntel(context.Background(), proj, "bogus"); err == nil {
		t.Fatal("unknown section accepted")
	}
}

func TestHandleGetIntel_NotBuiltIsStaleNotError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := config.Project{ID: "never", Name: "never", Path: t.TempDir()}
	res, err := handleGetIntel(context.Background(), proj, "")
	if err != nil {
		t.Fatalf("missing index should not error: %v", err)
	}
	if !res.Stale || res.StalenessReason == "" {
		t.Fatalf("missing index: want stale with reason, got %+v", res)
	}
}

// fakeEmb returns a fixed vector, or an error to simulate ollama-down.
type fakeEmb struct {
	vec []float32
	err error
}

func (f fakeEmb) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

func TestHandleRecall_HitAndOllamaDownLoud(t *testing.T) {
	ctx := context.Background()
	proj := config.Project{ID: "demo", Name: "demo"}
	ps, err := memory.Open(filepath.Join(t.TempDir(), "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	gs, err := memory.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer gs.Close()
	if _, err := ps.Add(ctx, memory.Item{Kind: memory.KindDecision, Text: "use codex as implementer", Confidence: 1, Embedding: []float32{1, 0, 0}}); err != nil {
		t.Fatal(err)
	}

	// normal hit: query vector aligned with the stored item.
	ok := fakeEmb{vec: []float32{1, 0, 0}}
	res, err := handleRecall(ctx, proj, ok, ps, gs, recallArgs{Project: "demo", Query: "who implements", K: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Text != "use codex as implementer" {
		t.Fatalf("recall hit wrong: %+v", res.Hits)
	}

	// ollama down: loud classified error, never empty-as-success.
	down := fakeEmb{err: errors.New(`embed call: Post "http://localhost:11434/api/embed": dial tcp: connect: connection refused`)}
	_, err = handleRecall(ctx, proj, down, ps, gs, recallArgs{Project: "demo", Query: "who implements"})
	if err == nil {
		t.Fatal("ollama-down returned nil error (empty-as-success)")
	}
	if !strings.Contains(err.Error(), "recall unavailable") {
		t.Fatalf("recall error not loud: %v", err)
	}
	var ce *channel.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("recall error not classified: %v", err)
	}
}

func TestHandleRecall_QueryRequired(t *testing.T) {
	ps, _ := memory.Open(filepath.Join(t.TempDir(), "p.db"))
	defer ps.Close()
	gs, _ := memory.Open(filepath.Join(t.TempDir(), "g.db"))
	defer gs.Close()
	_, err := handleRecall(context.Background(), config.Project{Name: "p"}, fakeEmb{vec: []float32{1}}, ps, gs, recallArgs{Project: "p", Query: ""})
	if err == nil {
		t.Fatal("empty query accepted")
	}
}

type preloadRoundTripFunc func(*http.Request) (*http.Response, error)

func (f preloadRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestPreloadOllamaModelsPolicy(t *testing.T) {
	tests := []struct {
		name       string
		preload    bool
		keepAlive  string
		wantModels []string
	}{
		{
			name:      "disabled makes no requests",
			preload:   false,
			keepAlive: "5m",
		},
		{
			name:       "enabled warms both models with configured residency",
			preload:    true,
			keepAlive:  "17m",
			wantModels: []string{"brain-model", "embed-model"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests []map[string]any
			client := &http.Client{Transport: preloadRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/api/generate" {
					t.Errorf("path = %q, want /api/generate", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode preload request: %v", err)
				}
				requests = append(requests, body)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
				}, nil
			})}

			a := &app{routing: config.Routing{
				Brain:  config.BrainConfig{Model: "brain-model", EmbedModel: "embed-model"},
				Ollama: config.OllamaConfig{KeepAlive: tt.keepAlive, PreloadModels: tt.preload},
			}}
			preloadOllamaModelsAt(a, "http://ollama.test", client)

			if len(requests) != len(tt.wantModels) {
				t.Fatalf("request count = %d, want %d", len(requests), len(tt.wantModels))
			}
			for i, wantModel := range tt.wantModels {
				if requests[i]["model"] != wantModel {
					t.Errorf("request %d model = %v, want %q", i, requests[i]["model"], wantModel)
				}
				if requests[i]["keep_alive"] != tt.keepAlive {
					t.Errorf("request %d keep_alive = %v, want configured %q", i, requests[i]["keep_alive"], tt.keepAlive)
				}
			}
		})
	}
}
