package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/intel"
	"github.com/ishaanbatra/styx/internal/mcpserver"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
	"github.com/ishaanbatra/styx/internal/target"
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

	// v2 capability-floor fields (additive; v1 consumers ignore unknown keys).
	ClassifiedSignals []string  `json:"classified_signals,omitempty"`
	Floor             string    `json:"floor,omitempty"`
	TierPlan          *tierPlan `json:"tier_plan,omitempty"`
	BlockedByBudget   bool      `json:"blocked_by_budget"`
	RetryAfterS       int       `json:"retry_after_s,omitempty"`
}

// tierPlan is the JSON view of router.TierPlan: the floor-clearing candidate
// targets, the one chosen within budget, and the next higher-tier escalation.
type tierPlan struct {
	Acceptable []string `json:"acceptable"`
	Chosen     string   `json:"chosen"`
	EscalateTo string   `json:"escalate_to,omitempty"`
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
	res := routeResult{
		Channel:       dec.Channel,
		Model:         dec.Model,
		Effort:        dec.Effort,
		FallbackChain: chain,
		Reasoning:     r.Explain(ctx, req),
		Budget:        budgetSnapshotFor(ctx, t, dec.Channel),
		Degraded:      dec.Degraded,
	}
	// sigs is the signal slice used for routing (Extracted here when the caller
	// omitted them). Surface it and the floor plan.
	res.ClassifiedSignals = sigs
	res.Floor = dec.Floor
	res.BlockedByBudget = dec.BlockedByBudget
	res.TierPlan = &tierPlan{
		Acceptable: dec.TierPlan.Acceptable,
		Chosen:     dec.TierPlan.Chosen,
		EscalateTo: dec.TierPlan.EscalateTo,
	}
	if dec.BlockedByBudget {
		res.RetryAfterS = minRetryAfter(ctx, t, dec.TierPlan.Acceptable)
	}
	return res, nil
}

// minRetryAfter returns the smallest positive RetryAfter across the acceptable
// targets' channels, or 0 when none has a known retry window.
func minRetryAfter(ctx context.Context, t *budget.Tracker, acceptable []string) int {
	best := 0
	for _, cm := range acceptable {
		ch := cm
		if i := strings.IndexByte(cm, ':'); i >= 0 {
			ch = cm[:i]
		}
		s, err := t.RetryAfter(ctx, ch)
		if err != nil || s <= 0 {
			continue
		}
		if best == 0 || s < best {
			best = s
		}
	}
	return best
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

type channelHealthArgs struct {
	Channel string `json:"channel"`
}

type channelHealthResult struct {
	Channel            string         `json:"channel"`
	CircuitOpen        bool           `json:"circuit_open"`
	FailuresRecent     int            `json:"failures_recent"`
	WindowS            int            `json:"window_s"`
	ErrorKinds         map[string]int `json:"error_kinds"`
	CooldownRemainingS float64        `json:"cooldown_remaining_s"`
}

var channelHealthSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"channel": map[string]any{
			"type":        "string",
			"description": "Channel to inspect (claude|codex|agy|ollama). Omit for all channels.",
		},
	},
}

// handleChannelHealth reports circuit/failure/cooldown state per channel from the
// existing usage log — a consumer can avoid a flaky provider before dispatch.
func handleChannelHealth(ctx context.Context, t *budget.Tracker, a channelHealthArgs) ([]channelHealthResult, error) {
	channels := defaultChannelNames
	if a.Channel != "" {
		channels = []string{a.Channel}
	}
	out := make([]channelHealthResult, 0, len(channels))
	for _, ch := range channels {
		h, err := t.ChannelHealth(ctx, ch, budget.BreakerThreshold, budget.BreakerWindow)
		if err != nil {
			return nil, fmt.Errorf("channel_health %s: %w", ch, err)
		}
		out = append(out, channelHealthResult{
			Channel:            h.Channel,
			CircuitOpen:        h.CircuitOpen,
			FailuresRecent:     h.FailuresRecent,
			WindowS:            h.WindowSeconds,
			ErrorKinds:         h.ErrorKinds,
			CooldownRemainingS: h.CooldownRemainingSeconds,
		})
	}
	return out, nil
}

// resolveProjectStrict resolves an MCP project argument via the shared target
// resolver (alias or path) WITHOUT any cwd fallback — an MCP server's cwd is not
// the caller's project. Empty or unknown project is a classified error, never a
// silent default.
func resolveProjectStrict(name string) (config.Project, error) {
	if strings.TrimSpace(name) == "" {
		return config.Project{}, &channel.ClassifiedError{
			Kind: channel.ErrKindOther,
			Err:  errors.New("project is required"),
		}
	}
	proj, err := target.Resolve(target.Spec{Alias: name})
	if err != nil {
		return config.Project{}, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
	}
	return proj, nil
}

type getIntelArgs struct {
	Project string `json:"project"`
	Section string `json:"section"`
}

type refreshIntelArgs struct {
	Project string `json:"project"`
}

// intelResult carries either the whole index (section == "") or exactly one
// section slice. Stale/StalenessReason always report freshness; a read never
// rebuilds.
type intelResult struct {
	Project         string       `json:"project"`
	Stale           bool         `json:"stale"`
	StalenessReason string       `json:"staleness_reason,omitempty"`
	Section         string       `json:"section,omitempty"`
	Index           *intel.Index `json:"index,omitempty"`

	Conventions   *intel.Conventions `json:"conventions,omitempty"`
	KeySymbols    []intel.KeySymbol  `json:"key_symbols,omitempty"`
	Modules       []intel.Module     `json:"modules,omitempty"`
	FileTree      []string           `json:"file_tree,omitempty"`
	RecentCommits []intel.Commit     `json:"recent_commits,omitempty"`
	OpenTodos     []intel.Todo       `json:"open_todos,omitempty"`
}

var getIntelSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"project": map[string]any{"type": "string", "description": "Registered project alias or path (required)."},
		"section": map[string]any{
			"type":        "string",
			"description": "Optional slice: conventions | key_symbols | modules | file_tree | recent_commits | open_todos. Omit for the whole index.",
		},
	},
	"required": []any{"project"},
}

var refreshIntelSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"project": map[string]any{"type": "string", "description": "Registered project alias or path (required)."},
	},
	"required": []any{"project"},
}

// handleGetIntel loads the persisted index (never rebuilds on read), reports
// staleness, and returns the whole index or one section.
func handleGetIntel(ctx context.Context, proj config.Project, section string) (intelResult, error) {
	idx, err := intel.Load(proj)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return intelResult{Project: proj.Name, Stale: true, StalenessReason: "no index built yet"}, nil
		}
		return intelResult{}, fmt.Errorf("get_intel load %s: %w", proj.Name, err)
	}
	stale, reason := intel.Staleness(proj, idx)
	res := intelResult{Project: proj.Name, Stale: stale, StalenessReason: reason, Section: section}
	switch section {
	case "":
		res.Index = idx
	case "conventions":
		c := idx.Conventions
		res.Conventions = &c
	case "key_symbols":
		res.KeySymbols = idx.KeySymbols
	case "modules":
		res.Modules = idx.Modules
	case "file_tree":
		res.FileTree = idx.FileTree
	case "recent_commits":
		res.RecentCommits = idx.RecentCommits
	case "open_todos":
		res.OpenTodos = idx.OpenTodos
	default:
		return intelResult{}, fmt.Errorf("get_intel: unknown section %q", section)
	}
	return res, nil
}

// handleRefreshIntel rebuilds the index via agy, rewrites .claude/context.md, and
// returns the fresh result. This is the deliberate write/refresh path.
func handleRefreshIntel(ctx context.Context, proj config.Project, agy intel.AgyClient, prog *progress.Tracker) (intelResult, error) {
	idx, err := intel.Build(ctx, proj, agy, prog)
	if err != nil {
		return intelResult{}, fmt.Errorf("refresh_intel build %s: %w", proj.Name, err)
	}
	if _, err := intel.WriteContextMD(proj.Path, intel.ToMarkdown(idx)); err != nil {
		return intelResult{}, fmt.Errorf("refresh_intel write context %s: %w", proj.Name, err)
	}
	stale, reason := intel.Staleness(proj, idx)
	return intelResult{Project: proj.Name, Stale: stale, StalenessReason: reason, Index: idx}, nil
}

const defaultRecallK = 5

type recallArgs struct {
	Project string `json:"project"`
	Query   string `json:"query"`
	K       int    `json:"k"`
}

type recallHit struct {
	Text       string  `json:"text"`
	Kind       string  `json:"kind"`
	Source     string  `json:"source,omitempty"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
}

type recallResult struct {
	Project string      `json:"project"`
	Hits    []recallHit `json:"hits"`
}

var recallSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"project": map[string]any{"type": "string", "description": "Registered project alias or path (required)."},
		"query":   map[string]any{"type": "string", "description": "What to recall (required)."},
		"k":       map[string]any{"type": "integer", "description": "Max results (default 5)."},
	},
	"required": []any{"project", "query"},
}

// handleRecall returns decayed top-k project + global memory. It degrades LOUD:
// any Recall failure (notably ollama embeddings down) becomes a classified error,
// never an empty result presented as success.
func handleRecall(ctx context.Context, proj config.Project, emb memory.Embedder, projStore, globalStore *memory.Store, a recallArgs) (recallResult, error) {
	if strings.TrimSpace(a.Query) == "" {
		return recallResult{}, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: errors.New("recall: query is required")}
	}
	k := a.K
	if k <= 0 {
		k = defaultRecallK
	}
	hits, err := memory.Recall(ctx, emb, a.Query, k, projStore, globalStore)
	if err != nil {
		return recallResult{}, &channel.ClassifiedError{
			Kind: channel.ErrKindOther,
			Err:  fmt.Errorf("recall unavailable (ollama embeddings): %w", err),
		}
	}
	out := recallResult{Project: proj.Name, Hits: make([]recallHit, 0, len(hits))}
	for _, h := range hits {
		out.Hits = append(out.Hits, recallHit{
			Text:       h.Item.Text,
			Kind:       string(h.Item.Kind),
			Source:     h.Item.Source,
			Score:      h.Score,
			Confidence: h.Item.Confidence,
		})
	}
	return out, nil
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
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &in); err != nil {
						return nil, fmt.Errorf("route: invalid arguments: %w", err)
					}
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
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &in); err != nil {
						return nil, fmt.Errorf("record_usage: invalid arguments: %w", err)
					}
				}
				return handleRecordUsage(ctx, a.tracker, in)
			},
		},
		{
			Name:        "channel_health",
			Description: "Report each channel's circuit-breaker state, recent failure count, per-kind error buckets, and remaining cooldown — so a consumer can avoid a flaky provider before dispatch.",
			InputSchema: channelHealthSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in channelHealthArgs
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &in); err != nil {
						return nil, fmt.Errorf("channel_health: invalid arguments: %w", err)
					}
				}
				return handleChannelHealth(ctx, a.tracker, in)
			},
		},
		{
			Name:        "get_intel",
			Description: "Return the per-project codebase intelligence index styx maintains (file tree, module summaries, conventions, key symbols, recent commits, open TODOs). Pass section to return one slice. Reports staleness; never rebuilds on read.",
			InputSchema: getIntelSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in getIntelArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("get_intel: invalid arguments: %w", err)
				}
				proj, err := resolveProjectStrict(in.Project)
				if err != nil {
					return nil, err
				}
				return handleGetIntel(ctx, proj, in.Section)
			},
		},
		{
			Name:        "refresh_intel",
			Serial:      true,
			Description: "Rebuild the per-project intelligence index (walk + convention sniff + agy module/key-symbol summaries) and return the fresh result. The deliberate write path.",
			InputSchema: refreshIntelSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in refreshIntelArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("refresh_intel: invalid arguments: %w", err)
				}
				proj, err := resolveProjectStrict(in.Project)
				if err != nil {
					return nil, err
				}
				ag, ok := a.channels["agy"]
				if !ok {
					return nil, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: errors.New("refresh_intel: agy channel unavailable")}
				}
				return handleRefreshIntel(ctx, proj, &agyAdapter{ch: rawChannel(ag)}, a.progress)
			},
		},
		{
			Name:        "recall",
			Description: "Recall the top-k project-scoped long-term memories (decisions, facts, preferences) via semantic similarity with recency/confidence decay. Requires local Ollama embeddings; returns a loud error if unavailable.",
			InputSchema: recallSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in recallArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("recall: invalid arguments: %w", err)
				}
				proj, err := resolveProjectStrict(in.Project)
				if err != nil {
					return nil, err
				}
				memDir, err := paths.MemoryDir()
				if err != nil {
					return nil, fmt.Errorf("recall: memory dir: %w", err)
				}
				if err := paths.EnsureDir(memDir); err != nil {
					return nil, fmt.Errorf("recall: ensure memory dir: %w", err)
				}
				projStore, err := memory.Open(filepath.Join(memDir, proj.ID+".db"))
				if err != nil {
					return nil, fmt.Errorf("recall: open project memory: %w", err)
				}
				defer projStore.Close()
				globalStore, err := memory.Open(filepath.Join(memDir, "global.db"))
				if err != nil {
					return nil, fmt.Errorf("recall: open global memory: %w", err)
				}
				defer globalStore.Close()
				emb := memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel)
				return handleRecall(ctx, proj, emb, projStore, globalStore, in)
			},
		},
	}
}

// cmdMCP runs styx as an MCP stdio server. stdout carries the JSON-RPC
// protocol; status goes to stderr via logStatus. The server runs until the
// host closes stdin (EOF); cancel then reaps background dispatch goroutines —
// background work lives and dies with this process.
func cmdMCP(a *app, args []string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := newConductorDeps(a, ctx)
	if dir, err := paths.TasksDir(); err != nil {
		logStatus("task state dir unavailable: %v", err)
	} else if err := paths.EnsureDir(dir); err != nil {
		logStatus("task state dir unavailable: %v", err)
	} else {
		d.reg.dir = dir
		if orphans := adoptOrphanedTaskFiles(dir, 7*24*time.Hour); len(orphans) > 0 {
			d.reg.adoptOrphans(orphans)
			logStatus("%d background task(s) from a previous session were lost — collect reports them", len(orphans))
		}
	}
	tools := withBackgroundStatus(append(mcpTools(a), conductorTools(d)...), d.reg)
	srv := mcpserver.New("styx", mcpServerVersion, tools)
	logStatus("mcp server ready on stdio (route, budget_status, record_usage, channel_health, get_intel, refresh_intel, recall, dispatch, dispatch_parallel, thread_status, memory_save, pipeline_run, rate_dispatch, collect)")
	go preloadOllamaModels(a) // best-effort: overlaps model load with the host handshake
	// The server owns the real stdout; point os.Stdout at stderr so a stray
	// fmt.Printf in a reused REPL/CLI code path (e.g. a pipeline's
	// "✓ Brief saved" line) can never interleave with the JSON-RPC stream.
	protocolOut := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = protocolOut }()
	err := srv.Serve(ctx, os.Stdin, protocolOut)
	// Remove the watch mirror file on shutdown (mirrors the REPL's identical
	// cleanup) so a later `styx watch` shows the "no live activity" nudge
	// instead of this server's stale final frame.
	d.removeMirror()
	return err
}

// preloadOllamaModels warms the brain + embedding models with keep_alive so
// the first real dispatch/recall doesn't pay a 3-10s cold load. Best-effort:
// failures are narrated, never fatal (ollama may simply be down).
func preloadOllamaModels(a *app) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, m := range []string{a.routing.Brain.Model, a.routing.Brain.EmbedModel} {
		if m == "" {
			continue
		}
		body, _ := json.Marshal(map[string]any{"model": m, "keep_alive": "30m"})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			"http://localhost:11434/api/generate", bytes.NewReader(body))
		if err != nil {
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logStatus("ollama preload %s skipped: %v", m, err)
			continue
		}
		resp.Body.Close()
	}
}
