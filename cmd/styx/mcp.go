package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

type routeArgs struct {
	Task    string   `json:"task"`
	Verb    string   `json:"verb"`
	Signals []string `json:"signals"`
	Project string   `json:"project"`
}

type budgetSnapshot struct {
	Channel       string  `json:"channel"`
	SessionCount  int     `json:"session_count"`
	SessionLimit  int     `json:"session_limit"`
	SessionPct    float64 `json:"session_pct"`
	WeeklyCount   int     `json:"weekly_count"`
	WeeklyLimit   int     `json:"weekly_limit"`
	WeeklyPct     float64 `json:"weekly_pct"`
	CooldownUntil string  `json:"cooldown_until,omitempty"`
	Stale         bool    `json:"stale"`
}

type routeResult struct {
	Channel       string         `json:"channel"`
	Model         string         `json:"model"`
	Effort        string         `json:"effort,omitempty"`
	FallbackChain []string       `json:"fallback_chain"`
	Reasoning     string         `json:"reasoning"`
	Budget        budgetSnapshot `json:"budget"`
	Degraded      bool           `json:"degraded"`
}

// budgetSnapshotFor reads the budget State for a channel. On error it returns a
// snapshot flagged Stale=true rather than failing the whole call (degrade loud).
func budgetSnapshotFor(ctx context.Context, t *budget.Tracker, channel string) budgetSnapshot {
	st, err := t.State(ctx, channel)
	if err != nil {
		return budgetSnapshot{Channel: channel, Stale: true}
	}
	snap := budgetSnapshot{
		Channel:      channel,
		SessionCount: st.SessionCount,
		SessionLimit: st.SessionLimit,
		SessionPct:   st.SessionPct,
		WeeklyCount:  st.WeeklyCount,
		WeeklyLimit:  st.WeeklyLimit,
		WeeklyPct:    st.WeeklyPct,
	}
	if !st.CooldownUntil.IsZero() {
		snap.CooldownUntil = st.CooldownUntil.Format(time.RFC3339)
	}
	return snap
}

// handleRoute picks a channel for a task using the budget-aware router and
// returns the decision plus a budget snapshot for the chosen channel.
func handleRoute(ctx context.Context, r *router.Router, t *budget.Tracker, a routeArgs) (routeResult, error) {
	if a.Task == "" {
		return routeResult{}, fmt.Errorf("route: task is required")
	}
	verb := a.Verb
	if verb == "" {
		verb = "build"
	}
	sigs := a.Signals
	if len(sigs) == 0 {
		sigs = signals.Extract(verb, []string{a.Task}, config.Project{})
	}
	req := router.Request{Verb: verb, Args: []string{a.Task}, Signals: sigs}
	dec, err := r.Route(ctx, req)
	if err != nil {
		return routeResult{}, fmt.Errorf("route: %w", err)
	}
	chain := make([]string, 0, len(dec.Fallback))
	for _, cm := range dec.Fallback {
		chain = append(chain, cm.Channel+":"+cm.Model)
	}
	return routeResult{
		Channel:       dec.Channel,
		Model:         dec.Model,
		Effort:        dec.Effort,
		FallbackChain: chain,
		Reasoning:     r.Explain(ctx, req),
		Budget:        budgetSnapshotFor(ctx, t, dec.Channel),
		Degraded:      dec.Degraded,
	}, nil
}

type budgetStatusArgs struct {
	Channel string `json:"channel"`
}

// defaultChannelNames is the canonical channel set (mirrors cmd/styx/budget.go).
var defaultChannelNames = []string{"claude", "codex", "agy", "ollama"}

// handleBudgetStatus reports the budget snapshot for one channel, or all four
// when Channel is empty.
func handleBudgetStatus(ctx context.Context, t *budget.Tracker, a budgetStatusArgs) ([]budgetSnapshot, error) {
	channels := defaultChannelNames
	if a.Channel != "" {
		channels = []string{a.Channel}
	}
	out := make([]budgetSnapshot, 0, len(channels))
	for _, ch := range channels {
		out = append(out, budgetSnapshotFor(ctx, t, ch))
	}
	return out, nil
}
