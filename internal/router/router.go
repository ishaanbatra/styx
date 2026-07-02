// Package router evaluates routing.toml rules and picks a (channel, model)
// for each request, with budget-aware degradation and fallback chains.
package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/signals"
)

// BudgetSource abstracts the per-channel usage backend (sqlite in prod,
// in-memory stub in tests).
type BudgetSource interface {
	UsedPct(ctx context.Context, channel string) (float64, error)
}

// BreakerSource reports whether a channel's circuit is open (too many recent
// failures). The router routes around broken channels like over-cap ones.
type BreakerSource interface {
	Broken(ctx context.Context, channel string) bool
}

// Router evaluates rules + budget state to produce a Decision.
type Router struct {
	Rules   []config.Rule
	Caps    config.BudgetCaps
	Budget  BudgetSource
	Breaker BreakerSource // optional
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

// TierPlan is the capability-floor view of a routing decision: the floor-clearing
// candidate targets (chain order), the one chosen within budget, and the next
// higher-tier target to escalate to.
type TierPlan struct {
	Acceptable []string // channel:model targets that clear the floor
	Chosen     string   // channel:model actually chosen
	EscalateTo string   // next higher-tier acceptable target, or ""
}

// Decision describes the chosen channel + fallback chain.
type Decision struct {
	Channel  string
	Model    string
	Effort   string
	Fallback []ChannelModel
	RuleIdx  int    // -1 if no rule matched (default)
	Reason   string // human-readable trace

	// Parallel-rule fields (review verb)
	Parallel        bool
	ParallelTargets []ChannelModel
	SynthesizeWith  ChannelModel

	// Degraded is true when budget or reliability checks caused fallback selection.
	Degraded bool

	// Capability-floor fields (v2). Floor is the minimum tier the request's
	// signals require (e.g. "sonnet"); TierPlan is the floor-restricted candidate
	// view; BlockedByBudget is true when every floor-clearing target is over
	// budget or circuit-open — the loud-refusal signal. When blocked, Channel/Model
	// still name the floor-clearing primary as a concrete recommendation (never a
	// below-floor lie, never null).
	Floor           string
	TierPlan        TierPlan
	BlockedByBudget bool
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
			Effort:  rule.Effort,
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

	// Assemble the ordered candidate chain and derive the capability floor.
	targets := append([]ChannelModel{primary}, fallback...)
	floor := signals.Floor(req.Signals)

	// floorClearing = candidates that meet the floor, in chain order.
	var floorClearing []ChannelModel
	for _, t := range targets {
		if signals.TierOf(t.Channel, t.Model) >= floor {
			floorClearing = append(floorClearing, t)
		}
	}
	routeSet := floorClearing
	floorUnmet := len(floorClearing) == 0
	if floorUnmet {
		// Misconfigured rule: no chain target meets the floor. Route best-effort
		// over the full chain but flag it loudly — never silently pretend it fits.
		routeSet = targets
	}

	chosen := routeSet[0]
	degraded := false
	blocked := false
	reason := fmt.Sprintf("matched rule #%d -> %s:%s", idx, chosen.Channel, chosen.Model)
	if r.unavailable(ctx, chosen.Channel) {
		degraded = true
		found := false
		for _, f := range routeSet[1:] {
			if !r.unavailable(ctx, f.Channel) {
				chosen = f
				found = true
				reason = fmt.Sprintf("rule #%d primary (%s:%s) unavailable; degraded to %s:%s (>= floor %s)",
					idx, primary.Channel, primary.Model, f.Channel, f.Model, floor)
				break
			}
		}
		if !found {
			// Every floor-clearing target is over budget / circuit-open. Refuse
			// LOUD: keep the floor-clearing primary as a concrete recommendation
			// but set BlockedByBudget so a consumer never runs it thinking it's fine.
			blocked = true
			chosen = routeSet[0]
			reason = fmt.Sprintf("rule #%d: all targets >= floor %s are over budget or circuit-open; blocked (recommend %s:%s once budget frees)",
				idx, floor, chosen.Channel, chosen.Model)
		}
	}
	if floorUnmet {
		degraded = true
		reason = fmt.Sprintf("rule #%d: no chain target meets required floor %s; best-effort %s:%s may be under-capable",
			idx, floor, chosen.Channel, chosen.Model)
	}

	return Decision{
		Channel:         chosen.Channel,
		Model:           chosen.Model,
		Effort:          rule.Effort,
		Fallback:        fallback,
		RuleIdx:         idx,
		Reason:          reason,
		Degraded:        degraded,
		Floor:           floor.String(),
		TierPlan:        buildTierPlan(floorClearing, chosen),
		BlockedByBudget: blocked,
	}, nil
}

// cmStr renders a ChannelModel as "channel:model" (or bare channel when Model is empty).
func cmStr(c ChannelModel) string {
	if c.Model == "" {
		return c.Channel
	}
	return c.Channel + ":" + c.Model
}

// buildTierPlan reports the floor-clearing candidates, the chosen target, and the
// lowest-tier candidate strictly above the chosen tier (the escalation target).
func buildTierPlan(acceptable []ChannelModel, chosen ChannelModel) TierPlan {
	tp := TierPlan{Chosen: cmStr(chosen)}
	chosenTier := signals.TierOf(chosen.Channel, chosen.Model)
	var escalate *ChannelModel
	for i := range acceptable {
		tp.Acceptable = append(tp.Acceptable, cmStr(acceptable[i]))
		t := signals.TierOf(acceptable[i].Channel, acceptable[i].Model)
		if t > chosenTier && (escalate == nil || t < signals.TierOf(escalate.Channel, escalate.Model)) {
			c := acceptable[i]
			escalate = &c
		}
	}
	if escalate != nil {
		tp.EscalateTo = cmStr(*escalate)
	}
	return tp
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
	if d.Floor != "" && d.Floor != "local" {
		fmt.Fprintf(&b, "floor: %s\n", d.Floor)
	}
	if d.BlockedByBudget {
		fmt.Fprintf(&b, "blocked: all targets >= floor %s over budget or circuit-open\n", d.Floor)
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

// unavailable reports whether a channel should be routed around: over its
// budget cap or with an open failure circuit.
func (r *Router) unavailable(ctx context.Context, ch string) bool {
	if r.overCap(ctx, ch) {
		return true
	}
	return r.Breaker != nil && r.Breaker.Broken(ctx, ch)
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
		if s == "" {
			return ChannelModel{}, fmt.Errorf("empty channel:model")
		}
		return ChannelModel{Channel: s, Model: ""}, nil
	}
	return ChannelModel{Channel: s[:idx], Model: s[idx+1:]}, nil
}
