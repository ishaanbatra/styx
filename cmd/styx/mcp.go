package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/mcpserver"
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

type recordUsageArgs struct {
	Channel   string `json:"channel"`
	Messages  int    `json:"messages"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Verb      string `json:"verb"`
	Model     string `json:"model"`
	Success   *bool  `json:"success"`
	Project   string `json:"project"`
	RunID     string `json:"run_id"`
}

type recordResult struct {
	Recorded bool           `json:"recorded"`
	Budget   budgetSnapshot `json:"budget"`
}

// handleRecordUsage records usage a consumer performed against a channel. The
// budget windows count rows (one row == one message), so Messages>1 emits that
// many rows; token totals ride the first row. Defaults: Messages=1, Success=true.
func handleRecordUsage(ctx context.Context, t *budget.Tracker, a recordUsageArgs) (recordResult, error) {
	if a.Channel == "" {
		return recordResult{}, fmt.Errorf("record_usage: channel is required")
	}
	success := true
	if a.Success != nil {
		success = *a.Success
	}
	n := a.Messages
	if n <= 0 {
		n = 1
	}
	for i := 0; i < n; i++ {
		ev := budget.Event{
			Channel: a.Channel,
			Verb:    a.Verb,
			Model:   a.Model,
			Success: success,
			Project: a.Project,
			RunID:   a.RunID,
		}
		if i == 0 {
			ev.TokensIn = a.TokensIn
			ev.TokensOut = a.TokensOut
		}
		if err := t.Record(ctx, ev); err != nil {
			return recordResult{}, fmt.Errorf("record_usage: %w", err)
		}
	}
	return recordResult{Recorded: true, Budget: budgetSnapshotFor(ctx, t, a.Channel)}, nil
}

const mcpServerVersion = "0.1.0"

var routeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"task":    map[string]any{"type": "string", "description": "The task or goal, in natural language."},
		"verb":    map[string]any{"type": "string", "description": "Optional styx verb (plan, build, research, review). Defaults to build."},
		"signals": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional routing signal tags; auto-derived from the task if omitted."},
		"project": map[string]any{"type": "string", "description": "Optional project path for context."},
	},
	"required": []string{"task"},
}

var budgetStatusSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"channel": map[string]any{"type": "string", "description": "Optional channel name; omit for all channels."},
	},
}

var recordUsageSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"channel":    map[string]any{"type": "string"},
		"messages":   map[string]any{"type": "integer", "description": "Messages consumed (default 1)."},
		"tokens_in":  map[string]any{"type": "integer"},
		"tokens_out": map[string]any{"type": "integer"},
		"verb":       map[string]any{"type": "string"},
		"model":      map[string]any{"type": "string"},
		"success":    map[string]any{"type": "boolean", "description": "Defaults to true."},
		"project":    map[string]any{"type": "string"},
		"run_id":     map[string]any{"type": "string"},
	},
	"required": []string{"channel"},
}

// mcpTools builds the MCP tool set bound to this app's router and tracker.
func mcpTools(a *app) []mcpserver.Tool {
	return []mcpserver.Tool{
		{
			Name:        "route",
			Description: "Choose which AI coding agent (channel) should handle a task, with budget-aware fallback. Returns the chosen channel, model, fallback chain, transparent reasoning, and the current budget snapshot.",
			InputSchema: routeSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in routeArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("route: invalid arguments: %w", err)
				}
				return handleRoute(ctx, a.router, a.tracker, in)
			},
		},
		{
			Name:        "budget_status",
			Description: "Report subscription budget per channel: 5h and weekly message counts/limits, percentages, and cooldowns.",
			InputSchema: budgetStatusSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in budgetStatusArgs
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &in); err != nil {
						return nil, fmt.Errorf("budget_status: invalid arguments: %w", err)
					}
				}
				return handleBudgetStatus(ctx, a.tracker, in)
			},
		},
		{
			Name:        "record_usage",
			Description: "Record that a consumer ran a channel, so styx's budget stays accurate when something other than styx executed the agent.",
			InputSchema: recordUsageSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in recordUsageArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("record_usage: invalid arguments: %w", err)
				}
				return handleRecordUsage(ctx, a.tracker, in)
			},
		},
	}
}

// cmdMCP runs styx as an MCP stdio server. stdout carries the JSON-RPC
// protocol; status goes to stderr via logStatus. The server runs until the
// host closes stdin (EOF).
func cmdMCP(a *app, args []string) error {
	srv := mcpserver.New("styx", mcpServerVersion, mcpTools(a))
	logStatus("mcp server ready on stdio (route, budget_status, record_usage)")
	return srv.Serve(context.Background(), os.Stdin, os.Stdout)
}
