// Package router evaluates routing.toml rules and picks a (channel, model)
// for each request, with budget-aware degradation and fallback chains.
package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
)

// BudgetSource abstracts the per-channel usage backend (sqlite in prod,
// in-memory stub in tests).
type BudgetSource interface {
	UsedPct(ctx context.Context, channel string) (float64, error)
}

// Router evaluates rules + budget state to produce a Decision.
type Router struct {
	Rules  []config.Rule
	Caps   config.BudgetCaps
	Budget BudgetSource
}

// Request is the input to Route.
type Request struct {
	Verb    string
	Args    []string
	Signals []string
}

// ChannelModel is a fully-qualified target.
type ChannelModel struct {
	Channel string
	Model   string
}

// Decision describes the chosen channel + fallback chain.
type Decision struct {
	Channel  string
	Model    string
	Fallback []ChannelModel
	RuleIdx  int    // -1 if no rule matched (default)
	Reason   string // human-readable trace

	// Parallel-rule fields (review verb)
	Parallel        bool
	ParallelTargets []ChannelModel
	SynthesizeWith  ChannelModel

	// Degraded is true when budget caused a fallback to be selected.
	Degraded bool
}

// FromConfig builds a Router using the standard config + sqlite budget tracker.
func FromConfig(routing config.Routing, b BudgetSource) *Router {
	return &Router{Rules: routing.Rules, Caps: routing.Budget, Budget: b}
}

// Route picks a channel:model for `req`.
func (r *Router) Route(ctx context.Context, req Request) (Decision, error) {
	idx, rule, ok := r.matchRule(req)
	if !ok {
		return Decision{
			Channel: "ollama", Model: "qwen2.5-coder:14b",
			RuleIdx: -1,
			Reason:  fmt.Sprintf("no rule matched verb=%q; defaulting to ollama:qwen2.5-coder:14b", req.Verb),
		}, nil
	}

	if len(rule.Parallel) > 0 {
		targets := make([]ChannelModel, 0, len(rule.Parallel))
		for _, p := range rule.Parallel {
			cm, err := parseChannelModel(p)
			if err != nil {
				return Decision{}, err
			}
			targets = append(targets, cm)
		}
		synth, err := parseChannelModel(rule.SynthesizeWith)
		if err != nil {
			return Decision{}, err
		}
		return Decision{
			Channel: targets[0].Channel, Model: targets[0].Model,
			RuleIdx: idx, Parallel: true,
			ParallelTargets: targets, SynthesizeWith: synth,
			Reason: fmt.Sprintf("matched rule #%d (parallel)", idx),
		}, nil
	}

	primary, err := parseChannelModel(rule.Use)
	if err != nil {
		return Decision{}, err
	}
	fallback := []ChannelModel{}
	for _, f := range rule.Fallback {
		cm, err := parseChannelModel(f)
		if err != nil {
			return Decision{}, err
		}
		fallback = append(fallback, cm)
	}

	// Budget-aware degradation: if primary is over its cap, walk into fallback.
	chosen := primary
	degraded := false
	reason := fmt.Sprintf("matched rule #%d -> %s:%s", idx, chosen.Channel, chosen.Model)
	if r.overCap(ctx, chosen.Channel) {
		degraded = true
		for _, f := range fallback {
			if !r.overCap(ctx, f.Channel) {
				reason = fmt.Sprintf("rule #%d primary (%s:%s) over cap; degraded to %s:%s",
					idx, primary.Channel, primary.Model, f.Channel, f.Model)
				chosen = f
				break
			}
		}
	}
	return Decision{
		Channel: chosen.Channel, Model: chosen.Model,
		Fallback: fallback, RuleIdx: idx, Reason: reason, Degraded: degraded,
	}, nil
}

// Explain returns a human-readable trace of routing for `req`.
func (r *Router) Explain(ctx context.Context, req Request) string {
	d, err := r.Route(ctx, req)
	if err != nil {
		return "router error: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "verb=%q signals=%v\n", req.Verb, req.Signals)
	fmt.Fprintf(&b, "decision: %s:%s\n", d.Channel, d.Model)
	fmt.Fprintf(&b, "reason: %s\n", d.Reason)
	if len(d.Fallback) > 0 {
		fmt.Fprintf(&b, "fallback: ")
		for i, f := range d.Fallback {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s:%s", f.Channel, f.Model)
		}
		b.WriteString("\n")
	}
	if d.Parallel {
		fmt.Fprintf(&b, "parallel targets: %v synthesize_with: %s:%s\n", d.ParallelTargets, d.SynthesizeWith.Channel, d.SynthesizeWith.Model)
	}
	return b.String()
}

func (r *Router) matchRule(req Request) (int, config.Rule, bool) {
	for i, rule := range r.Rules {
		if rule.Verb != req.Verb {
			continue
		}
		if !signalsContainAll(req.Signals, rule.Signals) {
			continue
		}
		return i, rule, true
	}
	return -1, config.Rule{}, false
}

func signalsContainAll(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := map[string]struct{}{}
	for _, s := range have {
		set[s] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}

func (r *Router) overCap(ctx context.Context, channel string) bool {
	cap := r.capFor(channel)
	if cap <= 0 || r.Budget == nil {
		return false
	}
	used, err := r.Budget.UsedPct(ctx, channel)
	if err != nil {
		return false
	}
	return used >= cap
}

func (r *Router) capFor(channel string) float64 {
	switch channel {
	case "claude":
		return r.Caps.Claude.CapPct
	case "codex":
		return r.Caps.Codex.CapPct
	case "agy", "gemini": // gemini is the v0.1 alias
		return r.Caps.Agy.CapPct
	}
	return 0
}

// parseChannelModel splits "channel:model" into a typed pair.
// Special "channel:interactive" sentinel is parsed too.
func parseChannelModel(s string) (ChannelModel, error) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return ChannelModel{}, fmt.Errorf("invalid channel:model %q", s)
	}
	return ChannelModel{Channel: s[:idx], Model: s[idx+1:]}, nil
}
