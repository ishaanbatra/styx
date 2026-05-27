package router

import (
	"context"
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
		Rules: rules,
		Caps:  caps,
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
