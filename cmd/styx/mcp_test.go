package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
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
