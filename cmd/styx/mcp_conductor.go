package main

// Conductor tools give a frontier-brain MCP consumer (e.g. Claude Code) a
// dispatch surface onto styx's existing agent-thread machinery: send work to
// a persistent claude/codex/agy thread (or a one-shot local ollama task) and
// inspect thread status. Ship-risk dispatches are gated by internal/shipgate
// before any project is even resolved. See docs/ARCHITECTURE.md "Conductor
// MCP tools" and the conductor spec (docs/superpowers/specs/).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/mcpserver"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/shipgate"
	"github.com/ishaanbatra/styx/internal/target"
)

// managed is one project's dispatch bundle (mirrors replSession.bind).
type managed struct {
	mgr *agent.Manager
	mem *memory.Store
}

// conductorDeps carries shared state for the conductor tool handlers.
type conductorDeps struct {
	a    *app
	gate *shipgate.Gate
	emb  memory.Embedder

	mu       sync.Mutex
	managers map[string]*managed
}

// newConductorDeps wires conductorDeps the same way cmdMCP wires the rest of
// the app: real ollama embedder, ship gate from routing.toml's
// [conductor] section (default "handshake").
func newConductorDeps(a *app) *conductorDeps {
	return &conductorDeps{
		a:        a,
		gate:     shipgate.New(shipgate.Mode(a.routing.Conductor.ShipGate)),
		emb:      memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel),
		managers: map[string]*managed{},
	}
}

// managerFor lazily binds a project exactly the way the REPL does
// (cmd/styx/repl.go bind()): project memory db, thread store, adapters,
// budget tracker, ollama summarizer, distill threshold. Resolution has no
// cwd fallback — an MCP server's cwd is not the caller's project, matching
// resolveProjectStrict's contract for the other project-scoped tools.
func (d *conductorDeps) managerFor(alias string) (*managed, error) {
	p, err := target.Resolve(target.Spec{Alias: alias})
	if err != nil {
		return nil, fmt.Errorf("resolve project: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if m, ok := d.managers[p.ID]; ok {
		return m, nil
	}
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, err
	}
	mem, err := memory.Open(filepath.Join(memDir, p.ID+".db"))
	if err != nil {
		return nil, fmt.Errorf("open project memory: %w", err)
	}
	threads, err := agent.LoadThreads(p.ID)
	if err != nil {
		mem.Close()
		return nil, err
	}
	timeout := 10 * time.Minute
	if d.a.routing.Budget.Claude.TimeoutMinutes > 0 {
		timeout = time.Duration(d.a.routing.Budget.Claude.TimeoutMinutes) * time.Minute
	}
	och := rawChannel(d.a.channels["ollama"])
	m := &managed{mem: mem, mgr: &agent.Manager{
		Project:   p,
		ProjectID: p.ID,
		RunID:     pipeline.NewRunID("conductor-" + p.Name),
		Threads:   threads,
		Adapters: map[string]agent.Adapter{
			"claude": agent.NewClaudeAdapter(),
			"codex":  agent.NewCodexAdapter(),
			"agy":    agent.NewAgyAdapter(),
		},
		Budget: d.a.tracker,
		Mem:    mem,
		Emb:    d.emb,
		Summarize: func(ctx context.Context, text string) (string, error) {
			resp, err := och.Send(ctx, channel.Request{
				Model:  d.a.routing.Brain.Model,
				Prompt: "Compress this conversation state into a dense summary preserving decisions, open questions, and file references:\n\n" + text,
			})
			return resp.Text, err
		},
		ThresholdPct: d.a.routing.Brain.ContextThresholdPct,
		DistillModel: d.a.routing.Tiers["haiku"],
		Timeout:      timeout,
	}}
	d.managers[p.ID] = m
	return m, nil
}

type dispatchArgs struct {
	Project      string   `json:"project"`
	Thread       string   `json:"thread"`
	CLI          string   `json:"cli"`
	Message      string   `json:"message"`
	Model        string   `json:"model"`
	Risk         string   `json:"risk"`
	ExtraRoots   []string `json:"extra_roots"`
	ConfirmToken string   `json:"confirm_token"`
}

// conductorTools builds the dispatch + thread_status tool set bound to d.
// Task 5 appends memory_save + pipeline_run to the same slice.
func conductorTools(d *conductorDeps) []mcpserver.Tool {
	return []mcpserver.Tool{
		{
			Name: "dispatch",
			Description: "Send work to a persistent agent thread (claude/codex/agy) or a one-shot local ollama task. " +
				"risk: read|edit|ship. ship requires the confirm_token handshake.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":       map[string]any{"type": "string", "description": "registered project alias (required to touch persistent threads; not needed for cli=ollama)"},
					"thread":        map[string]any{"type": "string", "description": "thread name; empty = cli name"},
					"cli":           map[string]any{"type": "string", "enum": []string{"claude", "codex", "agy", "ollama"}},
					"message":       map[string]any{"type": "string"},
					"model":         map[string]any{"type": "string", "description": "tier (opus|sonnet|haiku) or raw model id; empty = channel default"},
					"risk":          map[string]any{"type": "string", "enum": []string{"read", "edit", "ship"}},
					"extra_roots":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"confirm_token": map[string]any{"type": "string"},
				},
				"required": []string{"cli", "message", "risk"},
			},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in dispatchArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("dispatch args: %w", err)
				}
				switch in.CLI {
				case "claude", "codex", "agy", "ollama":
				default:
					return nil, fmt.Errorf("unknown cli %q (claude|codex|agy|ollama)", in.CLI)
				}
				if in.Message == "" {
					return nil, fmt.Errorf("message is required")
				}
				if in.Risk != "read" && in.Risk != "edit" && in.Risk != "ship" {
					return nil, fmt.Errorf("risk must be read|edit|ship, got %q", in.Risk)
				}
				if in.Risk == "ship" {
					res, err := d.gate.Check("dispatch:"+in.CLI, in.ConfirmToken)
					if err != nil {
						return nil, err
					}
					if !res.Allowed {
						return res, nil // brain reads token+message from the result
					}
				}
				if in.CLI == "ollama" { // one-shot, no thread machinery
					resp, err := d.a.channels["ollama"].Send(ctx, channel.Request{
						Model: in.Model, Prompt: in.Message,
					})
					if err != nil {
						return nil, fmt.Errorf("ollama dispatch: %w", err)
					}
					return map[string]any{"cli": "ollama", "text": resp.Text}, nil
				}
				m, err := d.managerFor(in.Project)
				if err != nil {
					return nil, err
				}
				model := in.Model
				if resolved, ok := d.a.routing.Tiers[model]; ok {
					model = resolved
				}
				res, err := m.mgr.Dispatch(ctx, agent.DispatchSpec{
					Thread: in.Thread, CLI: in.CLI, Model: model,
					Message: in.Message, ExtraRoots: in.ExtraRoots,
					ReadOnly: in.Risk == "read",
				}, nil)
				if err != nil {
					return nil, fmt.Errorf("dispatch %s: %w", in.CLI, err)
				}
				thread := in.Thread
				if thread == "" {
					thread = in.CLI
				}
				return map[string]any{
					"thread": thread, "cli": in.CLI, "text": res.Text,
					"tokens_in": res.InputTokens, "tokens_out": res.OutputTokens,
				}, nil
			},
		},
		{
			Name:        "thread_status",
			Description: "List this project's persistent agent threads with turn counts and context usage.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"project": map[string]any{"type": "string"}}},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in struct {
					Project string `json:"project"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("thread_status args: %w", err)
				}
				m, err := d.managerFor(in.Project)
				if err != nil {
					return nil, err
				}
				return map[string]any{"threads": m.mgr.StatusLines()}, nil
			},
		},
	}
}
