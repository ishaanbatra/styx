package router

import (
	"context"
	"reflect"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/ishaanbatra/styx/internal/config"
)

type stubBudget struct {
	used map[string]float64
}

func (s *stubBudget) UsedPct(_ context.Context, channel string) (float64, error) {
	return s.used[channel], nil
}

func newRouter(rules []config.Rule, caps config.BudgetCaps, used map[string]float64) *Router {
	return &Router{
		Rules:  rules,
		Caps:   caps,
		Budget: &stubBudget{used: used},
	}
}

func TestRoute_FirstMatchWins(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus-4-7"},
			{Verb: "plan", Use: "claude:sonnet-4-6"},
		},
		config.BudgetCaps{},
		nil,
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan", Signals: []string{"complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "claude" || dec.Model != "opus-4-7" {
		t.Errorf("got %s:%s, want claude:opus-4-7", dec.Channel, dec.Model)
	}
}

func TestRoute_SignalsMustAllMatch(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "grunt", Signals: []string{"trivial"}, Use: "ollama:qwen2.5-coder:7b"},
			{Verb: "grunt", Use: "ollama:qwen2.5-coder:14b"},
		},
		config.BudgetCaps{},
		nil,
	)
	// Without "trivial" signal, second rule should win.
	dec, err := r.Route(context.Background(), Request{Verb: "grunt", Signals: []string{"lang:python"}})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "qwen2.5-coder:14b" {
		t.Errorf("got model %q, want qwen2.5-coder:14b", dec.Model)
	}
}

func TestRoute_BudgetCapDegradesToFallback(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Use: "claude:sonnet-4-6", Fallback: []string{"codex:gpt-5", "ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}},
		map[string]float64{"claude": 90},
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "codex" {
		t.Errorf("expected degradation to codex, got %s:%s", dec.Channel, dec.Model)
	}
	if dec.Degraded == false {
		t.Errorf("expected Degraded=true when primary is over cap")
	}
}

func TestRoute_BudgetCapPrimaryUnderCap_NoDegradation(t *testing.T) {
	// Symmetric case: primary is under its cap_pct → primary chosen, Degraded=false.
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Use: "claude:sonnet-4-6", Fallback: []string{"codex:gpt-5", "ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}},
		map[string]float64{"claude": 50}, // 50% used < 80% cap
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "claude" {
		t.Errorf("expected primary claude, got %s:%s", dec.Channel, dec.Model)
	}
	if dec.Degraded {
		t.Errorf("expected Degraded=false when primary is under cap")
	}
}

func TestRoute_NoMatchDefaultsToOllama(t *testing.T) {
	r := newRouter(nil, config.BudgetCaps{}, nil)
	dec, err := r.Route(context.Background(), Request{Verb: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "ollama" {
		t.Errorf("default channel = %s, want ollama", dec.Channel)
	}
}

func TestRoute_ParallelRule(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "review", Parallel: []string{"claude:sonnet-4-6", "codex:gpt-5"}, SynthesizeWith: "claude:sonnet-4-6"},
		},
		config.BudgetCaps{},
		nil,
	)
	dec, err := r.Route(context.Background(), Request{Verb: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Parallel {
		t.Fatal("expected Parallel=true")
	}
	wantTargets := []ChannelModel{{"claude", "sonnet-4-6"}, {"codex", "gpt-5"}}
	if diff := cmp.Diff(wantTargets, dec.ParallelTargets); diff != "" {
		t.Errorf("ParallelTargets mismatch:\n%s", diff)
	}
	if dec.SynthesizeWith.Channel != "claude" || dec.SynthesizeWith.Model != "sonnet-4-6" {
		t.Errorf("SynthesizeWith = %+v, want claude:sonnet-4-6", dec.SynthesizeWith)
	}
}

func TestParseChannelModel_BareChannel(t *testing.T) {
	cm, err := parseChannelModel("codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cm.Channel != "codex" || cm.Model != "" {
		t.Errorf("got %+v, want {codex }", cm)
	}
}

func TestRoute_CarriesEffort(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Use: "codex", Effort: "high"},
		},
		config.BudgetCaps{},
		nil,
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	effort := reflect.ValueOf(dec).FieldByName("Effort")
	if !effort.IsValid() {
		t.Fatal("Decision.Effort missing")
	}
	if got := effort.String(); got != "high" {
		t.Errorf("Effort = %q, want high", got)
	}
}

func TestExplain_DescribesPickedRule(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Use: "claude:sonnet-4-6"},
		},
		config.BudgetCaps{},
		nil,
	)
	out := r.Explain(context.Background(), Request{Verb: "plan"})
	if !contains(out, "claude:sonnet-4-6") || !contains(out, "rule") {
		t.Errorf("Explain output missing expected content:\n%s", out)
	}
}

func TestRoute_ComplexFloorDegradesToCapableChannel(t *testing.T) {
	// plan+complex: claude:opus primary over its 80% cap. IMPORTANT: styx budget
	// is per-CHANNEL, not per-model — so a same-channel opus->sonnet drop can never
	// escape the claude cap. Degradation must land on a DIFFERENT capable channel.
	// The complex floor (sonnet) keeps codex but excludes the below-floor ollama
	// fallback, so the decision degrades to codex — never ollama, never blocked.
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus",
				Fallback: []string{"codex", "ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}}, // codex uncapped -> available
		map[string]float64{"claude": 95},                         // only claude over cap
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan", Args: []string{"x"}, Signals: []string{"complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "codex" {
		t.Fatalf("chose %s:%s, want codex (below-floor ollama must be excluded)", dec.Channel, dec.Model)
	}
	if !dec.Degraded || dec.BlockedByBudget {
		t.Fatalf("want degraded=true blocked=false, got degraded=%v blocked=%v", dec.Degraded, dec.BlockedByBudget)
	}
	if dec.Floor != "sonnet" {
		t.Fatalf("floor = %q, want sonnet", dec.Floor)
	}
	wantAcc := []string{"claude:opus", "codex"} // ollama excluded from the floor-clearing set
	if diff := cmp.Diff(wantAcc, dec.TierPlan.Acceptable); diff != "" {
		t.Fatalf("acceptable mismatch (-want +got):\n%s", diff)
	}
	if dec.TierPlan.Chosen != "codex" {
		t.Fatalf("tier_plan.chosen = %q, want codex", dec.TierPlan.Chosen)
	}
	if dec.TierPlan.EscalateTo != "claude:opus" {
		t.Fatalf("escalate_to = %q, want claude:opus", dec.TierPlan.EscalateTo)
	}
}

func TestRoute_ChainExhaustionBlocksLoud(t *testing.T) {
	// Regression guard for the chain-exhaustion bug: opus primary AND its only
	// floor-clearing fallback (codex) are BOTH over cap. Old code returned the
	// over-cap primary with Degraded=true and never refused. New behavior:
	// BlockedByBudget=true, chosen stays the floor-clearing primary as a concrete
	// recommendation.
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus",
				Fallback: []string{"codex", "ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}, Codex: config.ChannelCap{CapPct: 80}},
		map[string]float64{"claude": 95, "codex": 90}, // both capable channels over cap
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan", Args: []string{"x"}, Signals: []string{"complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.BlockedByBudget {
		t.Fatalf("want BlockedByBudget=true (all floor-clearing channels over cap), got false")
	}
	if !dec.Degraded {
		t.Fatalf("want Degraded=true when blocked")
	}
	if dec.Channel != "claude" || dec.Model != "opus" {
		t.Fatalf("blocked chosen = %s:%s, want the floor-clearing primary claude:opus", dec.Channel, dec.Model)
	}
	// ollama must NOT be chosen: styx never returns a below-floor channel.
	if dec.Channel == "ollama" {
		t.Fatal("blocked path degraded to below-floor ollama — floor violated")
	}
}

func TestRoute_NonFlooredUnchanged(t *testing.T) {
	// A task with no complex/deep signal keeps v1 behavior: over-cap primary
	// degrades to the first available fallback, including ollama; never blocked.
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Use: "claude:sonnet",
				Fallback: []string{"ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}},
		map[string]float64{"claude": 95},
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan", Args: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "ollama" || dec.BlockedByBudget {
		t.Fatalf("non-floored task: got %s:%s blocked=%v, want ollama not blocked", dec.Channel, dec.Model, dec.BlockedByBudget)
	}
	if dec.Floor != "local" {
		t.Fatalf("floor = %q, want local (no floor)", dec.Floor)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
