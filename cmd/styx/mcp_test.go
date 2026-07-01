package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/mcpserver"
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
	for i := 0; i < 3; i++ {
		if err := tr.Record(ctx, budget.Event{Channel: "claude", Verb: "plan", Success: false, ErrorKind: "5xx"}); err != nil {
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
	if claude == nil || !claude.CircuitOpen || claude.FailuresRecent != 3 || claude.ErrorKinds["server"] != 3 {
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

func TestMCPTools_EndToEndChannelHealth(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	a := &app{router: r, tracker: tr}
	srv := mcpserver.New("styx", "test", mcpTools(a))
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"channel_health","arguments":{}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	// The tools/call payload is JSON-marshaled and then embedded as a "text"
	// string field, so the outer encoder escapes its quotes (e.g. \"circuit_open\").
	// Match the bare field name, consistent with TestMCPTools_EndToEndRoute's
	// same workaround for tool-call (not tools/list) output.
	if !strings.Contains(out.String(), "circuit_open") || !strings.Contains(out.String(), "error_kinds") {
		t.Fatalf("channel_health output missing fields:\n%s", out.String())
	}
}
