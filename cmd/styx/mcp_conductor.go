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
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/mcpserver"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/shipgate"
	"github.com/ishaanbatra/styx/internal/signals"
	"github.com/ishaanbatra/styx/internal/target"
)

// managed is one project's dispatch bundle (mirrors replSession.bind).
type managed struct {
	mgr *agent.Manager
	mem *memory.Store
}

// conductorDeps carries shared state for the conductor tool handlers.
type conductorDeps struct {
	a      *app
	gate   *shipgate.Gate
	emb    memory.Embedder
	reg    *taskRegistry   // background dispatch registry (nil-safe on read paths)
	board  *activity.Board // shared session liveness board (fed by every Manager)
	mirror func() error    // Task 9: debounced disk mirror of board; nil if unavailable

	// mirrorPath is the on-disk path d.mirror writes to (mirrors the field
	// above; kept alongside it so shutdown() can remove the file without
	// recomputing the path). Empty when d.mirror is nil.
	mirrorPath string

	mu       sync.Mutex
	managers map[string]*managed
	gmem     *memory.Store // lazy global.db handle for learning kinds
}

// newConductorDeps wires conductorDeps the same way cmdMCP wires the rest of
// the app: real ollama embedder, ship gate + background-task cap from
// routing.toml's [conductor] section, task registry rooted on the server's
// context (background work dies with the server — no daemons).
func newConductorDeps(a *app, rootCtx context.Context) *conductorDeps {
	board := activity.NewBoard()
	d := &conductorDeps{
		a:        a,
		gate:     shipgate.New(shipgate.Mode(a.routing.Conductor.ShipGate)),
		emb:      memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel),
		board:    board,
		reg:      newTaskRegistry(rootCtx, a.routing.Conductor.MaxBackgroundTasks, board),
		managers: map[string]*managed{},
	}
	// Throttled disk mirror of the board (Task 9), keyed by the server's cwd
	// project — the launcher starts `styx mcp` in the project directory
	// (managerFor's comment), the same directory a `styx watch` process
	// invoked alongside it runs in, and `styx watch` resolves its project the
	// same way (resolveGlobalTarget("")). That shared resolution is what
	// guarantees the writer and reader paths agree; best-effort — a
	// resolution or state-dir failure logs and leaves d.mirror nil rather
	// than blocking server startup.
	if proj, perr := resolveGlobalTarget(""); perr != nil {
		logStatus("watch mirror unavailable: %v", perr)
	} else if stateDir, serr := paths.StateDir(); serr != nil {
		logStatus("watch mirror unavailable: %v", serr)
	} else {
		mirrorDir := filepath.Join(stateDir, "watch")
		if err := paths.EnsureDir(mirrorDir); err != nil {
			logStatus("watch mirror dir: %v", err)
		} else {
			d.mirrorPath = filepath.Join(mirrorDir, proj.ID+".json")
			d.mirror = activity.MirrorThrottle(board, d.mirrorPath, 2*time.Second)
		}
	}

	// Mechanical pulse: keeps the disk mirror live for background AND awaited
	// work with no ollama dependency (awaited-dispatch spec). Only wired when
	// the mirror itself is (same best-effort posture).
	if d.mirror != nil {
		go d.pulse(rootCtx)
	}

	// The ollama watcher summarizes cross-agent liveness into the board note,
	// which the piggyback bg line and thread_status surface. Off the server's
	// root context (dies with the server — no daemons); best-effort like the
	// REPL's, gated on Task 5's ollama_enabled upgrade flag.
	if a.routing.Watch.OllamaEnabled {
		w := &activity.Watcher{
			BaseURL:  "http://localhost:11434",
			Model:    a.routing.Brain.Model,
			Board:    board,
			Interval: a.routing.Watch.Interval(),
		}
		go w.Run(rootCtx)
	}
	return d
}

// mirrorNow drives the disk mirror (Task 9), if wired. Debounced internally
// (activity.MirrorThrottle); write failures are narrated, never swallowed.
func (d *conductorDeps) mirrorNow() {
	if d.mirror == nil {
		return
	}
	if err := d.mirror(); err != nil {
		logStatus("watch mirror: %v", err)
	}
}

// removeMirror deletes the disk mirror file on server shutdown so a later
// `styx watch` shows the "no live activity" nudge instead of this session's
// stale final frame (mirrors the REPL's identical cleanup step in repl.go).
// A missing file is not an error; any other removal failure is narrated,
// never fatal — shutdown must still complete.
func (d *conductorDeps) removeMirror() {
	if d.mirrorPath == "" {
		return
	}
	if err := os.Remove(d.mirrorPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		logStatus("watch mirror cleanup: %v", err)
	}
}

// pulseTick is the mechanical mirror cadence; MirrorThrottle (2s) dedupes the
// actual disk writes. A var so tests can shrink it.
var pulseTick = 1 * time.Second

// pulse drives the disk mirror while agents or tasks are live, plus one
// unthrottled flush on the live→idle transition so `styx watch` shows final
// ✓ done states instead of a stale last action. This is the mechanical layer
// closing Task 9's deferred limitation (mirror frozen mid-run for background
// tasks): it runs whenever the server runs — no ollama dependency, no config
// gate — and dies with ctx (the server's root context; no daemons).
func (d *conductorDeps) pulse(ctx context.Context) {
	t := time.NewTicker(pulseTick)
	defer t.Stop()
	wasLive := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			live := d.anyLive()
			switch {
			case live:
				d.mirrorNow()
			case wasLive:
				// Bypass the throttle for the final frame: the debounce could
				// swallow it, and nothing else writes once everything is idle.
				if err := activity.WriteMirror(d.mirrorPath, d.board.Snapshot(), d.board.WatcherNote()); err != nil {
					logStatus("watch mirror: %v", err)
				}
			}
			wasLive = live
		}
	}
}

// anyLive reports whether the board has an unfinished agent or the registry
// a queued/running task.
func (d *conductorDeps) anyLive() bool {
	for _, s := range d.board.Snapshot() {
		if !s.Done {
			return true
		}
	}
	for _, tk := range d.reg.Snapshot() {
		if tk.State == taskQueued || tk.State == taskRunning {
			return true
		}
	}
	return false
}

// capPctFor returns the routing.toml cap_pct for a channel (0 = no cap).
func capPctFor(r config.Routing, cli string) float64 {
	switch cli {
	case "claude":
		return r.Budget.Claude.CapPct
	case "codex":
		return r.Budget.Codex.CapPct
	case "agy":
		return r.Budget.Agy.CapPct
	}
	return 0
}

// spawnBudgetCheck refuses a background spawn when the target channel is
// circuit-open or over its budget cap — spawn-time failures must be
// synchronous errors, never background losses discovered at collect time.
func (d *conductorDeps) spawnBudgetCheck(ctx context.Context, cli string) error {
	if broken, err := d.a.tracker.ShouldCircuitBreak(ctx, cli, budget.BreakerThreshold, budget.BreakerWindow); err != nil {
		return fmt.Errorf("spawn budget check: %w", err)
	} else if broken {
		return fmt.Errorf("channel %s circuit is open (recent failures) — check channel_health before dispatching", cli)
	}
	pct, err := d.a.tracker.UsedPct(ctx, cli)
	if err != nil {
		return fmt.Errorf("spawn budget check: %w", err)
	}
	if cap := capPctFor(d.a.routing, cli); cap > 0 && pct >= cap {
		return fmt.Errorf("channel %s is over budget (%.0f%% used, cap %.0f%%) — pick another channel or wait", cli, pct, cap)
	}
	return nil
}

// managerFor lazily binds a project. An empty alias resolves to the server's
// cwd project — the launcher starts `styx mcp` in the launch directory, so
// cwd IS the caller's project for the conductor (same rule pipeline_run
// already uses). A named alias resolves strictly (no fallback); resolution
// failures list the registry so an MCP consumer can self-correct — it cannot
// "pass --dir" or "cd into a repo".
func (d *conductorDeps) managerFor(alias string) (*managed, error) {
	var p project.Project
	var err error
	if alias == "" {
		p, err = resolveGlobalTarget("")
	} else {
		p, err = target.Resolve(target.Spec{Alias: alias})
	}
	if err != nil {
		return nil, fmt.Errorf("resolve project %q: %w (registered projects: %s)",
			alias, err, registeredProjectNames())
	}
	return d.managerForProject(p)
}

// globalMem lazily opens the shared global memory store. user-preference and
// retrospective memories describe the user, not a repo, so they live in
// global.db — the same store launch-time guidance injection reads.
func (d *conductorDeps) globalMem() (*memory.Store, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gmem != nil {
		return d.gmem, nil
	}
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, err
	}
	s, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		return nil, fmt.Errorf("open global memory: %w", err)
	}
	d.gmem = s
	return s, nil
}

// registeredProjectNames renders the registry for MCP error messages.
func registeredProjectNames() string {
	projs, err := config.LoadProjects()
	if err != nil || len(projs) == 0 {
		return "none"
	}
	names := make([]string, len(projs))
	for i, p := range projs {
		names[i] = p.Name
	}
	return strings.Join(names, ", ")
}

// dispatchSignals tags the dispatch message with routing signals for the
// outcome row (comma-joined). The conductor picks the cli explicitly, so the
// signals are recorded for learning, not for routing.
func dispatchSignals(message string) string {
	return strings.Join(signals.Extract("dispatch", []string{message}, config.Project{}), ",")
}

// outcomeErrKind classifies a dispatch error for the outcome row: the
// channel's classified kind when available, else "other". "" on success.
func outcomeErrKind(err error) string {
	if err == nil {
		return ""
	}
	var ce *channel.ClassifiedError
	if errors.As(err, &ce) {
		return string(ce.Kind)
	}
	return "other"
}

// dispatchMeta carries what finishDispatch needs to append an outcome row
// and shape the result map — the post-dispatch bookkeeping shared by the
// synchronous handler and background task completions.
type dispatchMeta struct {
	ProjectID  string
	Thread     string
	CLI        string
	Model      string
	Risk       string
	Signals    string
	TaskID     string // "" for sync dispatches
	Background bool
	Start      time.Time
}

// finishDispatch appends the outcome row (success and failure alike; record
// errors are narrated, never fail the dispatch — budget events are already
// recorded inside Manager.Dispatch) and shapes the dispatch result map.
func (d *conductorDeps) finishDispatch(ctx context.Context, meta dispatchMeta, res agent.TurnResult, dispatchErr error) (map[string]any, error) {
	durS := math.Round(time.Since(meta.Start).Seconds()*10) / 10
	if rerr := d.a.tracker.RecordOutcome(ctx, budget.Outcome{
		Project: meta.ProjectID, Thread: meta.Thread, TaskID: meta.TaskID,
		CLI: meta.CLI, Model: meta.Model, Signals: meta.Signals, Risk: meta.Risk,
		DurationS: durS, TokensIn: res.InputTokens, TokensOut: res.OutputTokens,
		ErrorKind: outcomeErrKind(dispatchErr), Background: meta.Background,
	}); rerr != nil {
		logStatus("outcome record (%s) failed: %v", meta.CLI, rerr)
	}
	if dispatchErr != nil {
		return nil, fmt.Errorf("dispatch %s: %w", meta.CLI, dispatchErr)
	}
	return map[string]any{
		"thread": meta.Thread, "cli": meta.CLI, "text": res.Text,
		"tokens_in": res.InputTokens, "tokens_out": res.OutputTokens,
		"model": meta.Model, "duration_s": durS,
	}, nil
}

// managerForProject binds an already-resolved project (cached by project ID).
// pipeline_run's research branch uses it with the server's cwd project, which
// the pipelines themselves resolve via resolveGlobalTarget.
func (d *conductorDeps) managerForProject(p project.Project) (*managed, error) {
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
		Board:        d.board,
	}}
	d.managers[p.ID] = m
	return m, nil
}

// collectOne shapes one task's collect payload. Finished tasks (done, error,
// orphaned) are claimed by being collected; live tasks report status only.
func collectOne(reg *taskRegistry, tk bgTask) map[string]any {
	switch tk.State {
	case taskQueued, taskRunning:
		out := map[string]any{
			"task_id": tk.ID, "status": tk.State,
			"elapsed_s": math.Round(time.Since(tk.Created).Seconds()*10) / 10,
		}
		if tk.QueuedBehind != "" {
			out["queued_behind"] = tk.QueuedBehind
		}
		return out
	case taskDone:
		out := map[string]any{"task_id": tk.ID, "status": taskDone}
		for k, v := range tk.Result {
			out[k] = v
		}
		reg.Claim(tk.ID)
		return out
	default: // error, orphaned
		reg.Claim(tk.ID)
		return map[string]any{
			"task_id": tk.ID, "status": tk.State, "error": tk.Err,
			"thread": tk.Spec.Thread, "cli": tk.Spec.CLI,
		}
	}
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
	Background   bool     `json:"background"`
}

// conductorTools builds the dispatch + thread_status tool set bound to d.
// Task 5 appends memory_save + pipeline_run to the same slice.
func conductorTools(d *conductorDeps) []mcpserver.Tool {
	return []mcpserver.Tool{
		{
			Name: "dispatch",
			Description: "Send work to a persistent agent thread (claude/codex/agy) or a one-shot local ollama task. " +
				"By default this WAITS: live progress streams while the agent works and the result returns inline — " +
				"use it for anything the user is waiting on. background:true detaches instead (returns a task_id; " +
				"collect fetches the result) — only when the user explicitly wants to keep working meanwhile. " +
				"For several agents at once use dispatch_parallel. risk: read|edit|ship. ship requires the confirm_token handshake.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"project":       map[string]any{"type": "string", "description": "registered project alias; empty = the project styx was launched in"},
					"thread":        map[string]any{"type": "string", "description": "thread name; empty = cli name"},
					"cli":           map[string]any{"type": "string", "enum": []string{"claude", "codex", "agy", "ollama"}},
					"message":       map[string]any{"type": "string"},
					"model":         map[string]any{"type": "string", "description": "tier (opus|sonnet|haiku) or raw model id; empty = channel default (ollama defaults to the routing brain model)"},
					"risk":          map[string]any{"type": "string", "enum": []string{"read", "edit", "ship"}},
					"extra_roots":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"confirm_token": map[string]any{"type": "string"},
					"background":    map[string]any{"type": "boolean", "description": "true = return a task_id immediately and run in the background; collect fetches the result"},
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
				if in.Background {
					if in.Risk == "ship" {
						return nil, fmt.Errorf("ship-risk dispatch cannot run in background — the confirmation handshake is interactive; dispatch it synchronously")
					}
					if in.CLI == "ollama" {
						return nil, fmt.Errorf("ollama one-shots are synchronous (local and fast) — drop background")
					}
					if d.reg == nil {
						return nil, fmt.Errorf("background dispatch unavailable (no task registry)")
					}
					if err := d.spawnBudgetCheck(ctx, in.CLI); err != nil {
						return nil, err
					}
				}
				start := time.Now()
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
					ch, ok := d.a.channels["ollama"]
					if !ok {
						return nil, fmt.Errorf("dispatch ollama: ollama channel unavailable")
					}
					model := in.Model
					if model == "" {
						model = d.a.routing.Brain.Model // seeded default: qwen2.5-coder:7b
					}
					if model == "" {
						model = "qwen2.5-coder:7b"
					}
					resp, err := ch.Send(ctx, channel.Request{
						Model: model, Prompt: in.Message,
					})
					errKind := ""
					if err != nil {
						errKind = "other"
					}
					if rerr := d.a.tracker.Record(ctx, budget.Event{
						Channel: "ollama", Verb: "one-shot", Model: model,
						TokensIn: resp.EstTokensIn, TokensOut: resp.EstTokensOut,
						Success: err == nil, ErrorKind: errKind,
					}); rerr != nil {
						logStatus("budget record (ollama one-shot) failed: %v", rerr)
					}
					if rerr := d.a.tracker.RecordOutcome(ctx, budget.Outcome{
						CLI: "ollama", Model: model, Signals: dispatchSignals(in.Message),
						Risk: in.Risk, DurationS: math.Round(time.Since(start).Seconds()*10) / 10,
						TokensIn: resp.EstTokensIn, TokensOut: resp.EstTokensOut,
						ErrorKind: outcomeErrKind(err),
					}); rerr != nil {
						logStatus("outcome record (ollama one-shot) failed: %v", rerr)
					}
					if err != nil {
						return nil, fmt.Errorf("ollama dispatch: %w", err)
					}
					return map[string]any{"cli": "ollama", "text": resp.Text,
						"model": model, "duration_s": math.Round(time.Since(start).Seconds()*10) / 10}, nil
				}
				m, err := d.managerFor(in.Project)
				if err != nil {
					return nil, err
				}
				model := in.Model
				if resolved, ok := d.a.routing.Tiers[model]; ok {
					model = resolved
				}
				thread := in.Thread
				if thread == "" {
					thread = in.CLI
				}
				meta := dispatchMeta{
					ProjectID: m.mgr.ProjectID, Thread: thread, CLI: in.CLI,
					Model: model, Risk: in.Risk, Signals: dispatchSignals(in.Message),
					Start: start,
				}
				spec := agent.DispatchSpec{
					Thread: in.Thread, CLI: in.CLI, Model: model,
					Message: in.Message, ExtraRoots: in.ExtraRoots,
					ReadOnly: in.Risk == "read",
				}
				if in.Background {
					runFn := func(bctx context.Context, id string) (map[string]any, error) {
						// bctx is the server's root context: the task survives
						// this tool call returning and dies with the server.
						// The mechanical pulse mirrors mid-run; these brackets
						// keep the start/end frames prompt.
						bmeta := meta
						bmeta.Background = true
						bmeta.TaskID = id
						d.mirrorNow()
						res, derr := m.mgr.Dispatch(bctx, spec, nil)
						d.mirrorNow()
						return d.finishDispatch(bctx, bmeta, res, derr)
					}
					id, state := d.reg.Spawn(taskSpec{
						Project: in.Project, ProjectID: m.mgr.ProjectID, Thread: thread,
						CLI: in.CLI, Model: model, Risk: in.Risk,
					}, runFn)
					return map[string]any{"task_id": id, "thread": thread, "cli": in.CLI, "status": state}, nil
				}
				// Awaited (the default): spawn through the same registry as
				// background work — the ordering rules queue a thread collision
				// instead of erroring — and observe until terminal, streaming
				// board-derived progress. Cancellation (host Esc) detaches: the
				// task keeps running and stays collectible; work is never lost
				// (spec: awaited = observed background).
				if d.reg == nil {
					return nil, fmt.Errorf("dispatch unavailable (no task registry)")
				}
				runFn := func(bctx context.Context, id string) (map[string]any, error) {
					ameta := meta
					ameta.TaskID = id
					d.mirrorNow()
					res, derr := m.mgr.Dispatch(bctx, spec, nil)
					d.mirrorNow()
					return d.finishDispatch(bctx, ameta, res, derr)
				}
				id, _ := d.reg.Spawn(taskSpec{
					Project: in.Project, ProjectID: m.mgr.ProjectID, Thread: thread,
					CLI: in.CLI, Model: model, Risk: in.Risk,
				}, runFn)
				notify, _ := mcpserver.ProgressFn(ctx)
				out := d.awaitTasks(ctx, []string{id}, notify)
				if out.Detached {
					return map[string]any{"detached": true, "task_id": id,
						"note": "await interrupted; the task keeps running — collect fetches its result"}, nil
				}
				r := out.Results[0]
				if r["status"] != taskDone {
					return nil, fmt.Errorf("%v", r["error"])
				}
				return r, nil
			},
		},
		{
			Name:        "thread_status",
			Description: "List this project's persistent agent threads with turn counts and context usage.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"project": map[string]any{"type": "string", "description": "registered project alias; empty = the project styx was launched in"}}},
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
				tasks := []string{}
				for _, tk := range d.reg.Snapshot() {
					if tk.State == taskQueued || tk.State == taskRunning || !tk.Claimed {
						tasks = append(tasks, taskLine(tk))
					}
				}
				return map[string]any{"threads": m.mgr.StatusLines(), "tasks": tasks}, nil
			},
		},
		{
			Name:        "memory_save",
			Description: "Persist a durable fact, decision, todo, or routing preference to styx memory.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"project": map[string]any{"type": "string", "description": "registered project alias; empty = the project styx was launched in"},
				"kind":    map[string]any{"type": "string", "enum": []string{"fact", "decision", "todo", "routing-preference", "user-preference", "retrospective"}},
				"text":    map[string]any{"type": "string"},
				"scope":   map[string]any{"type": "string"},
			}, "required": []string{"kind", "text"}},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in struct{ Project, Kind, Text, Scope string }
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("memory_save args: %w", err)
				}
				switch memory.Kind(in.Kind) {
				case memory.KindFact, memory.KindDecision, memory.KindTodo, memory.KindRoutingPreference,
					memory.KindUserPreference, memory.KindRetrospective:
				default:
					return nil, fmt.Errorf("unknown kind %q", in.Kind)
				}
				if in.Text == "" {
					return nil, fmt.Errorf("text is required")
				}
				kind := memory.Kind(in.Kind)
				var store *memory.Store
				projectName := ""
				defaultScope := "project"
				if kind == memory.KindRoutingPreference || kind == memory.KindUserPreference || kind == memory.KindRetrospective {
					// Routing and learning kinds are about the user/session, not one repo:
					// they live in global.db (no project resolution needed).
					g, err := d.globalMem()
					if err != nil {
						return nil, err
					}
					store, defaultScope = g, "global"
				} else {
					m, err := d.managerFor(in.Project)
					if err != nil {
						return nil, err
					}
					store, projectName = m.mem, m.mgr.Project.Name
				}
				vec, err := d.emb.Embed(ctx, in.Text)
				if err != nil {
					return nil, fmt.Errorf("embed (is ollama up?): %w", err)
				}
				scope := in.Scope
				if scope == "" {
					scope = defaultScope
				}
				id, err := store.Add(ctx, memory.Item{
					Kind: kind, Text: in.Text, Source: "conductor",
					Project: projectName, Scope: scope, Confidence: 0.9, Embedding: vec,
				})
				if err != nil {
					return nil, fmt.Errorf("save memory: %w", err)
				}
				return map[string]any{"saved": true, "id": id}, nil
			},
		},
		{
			Name:        "pipeline_run",
			Serial:      true,
			Description: "Run a styx pipeline: research | review | intel | auto. auto ships (branch→push→PR) and requires the confirm_token handshake.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"pipeline":      map[string]any{"type": "string", "enum": []string{"research", "review", "intel", "auto"}},
				"arg":           map[string]any{"type": "string"},
				"confirm_token": map[string]any{"type": "string"},
			}, "required": []string{"pipeline"}},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in struct{ Pipeline, Arg, ConfirmToken string }
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("pipeline_run args: %w", err)
				}
				switch in.Pipeline {
				case "research", "review", "intel", "auto":
				default:
					return nil, fmt.Errorf("unknown pipeline %q", in.Pipeline)
				}
				if in.Pipeline == "auto" {
					res, err := d.gate.Check("pipeline:auto", in.ConfirmToken)
					if err != nil {
						return nil, err
					}
					if !res.Allowed {
						return res, nil // brain reads token+message from the result
					}
				}
				switch in.Pipeline { // same calls as the REPL pipelines map (repl.go:625)
				case "research":
					if err := cmdResearch(d.a, []string{in.Arg}); err != nil {
						return nil, fmt.Errorf("pipeline research: %w", err)
					}
					// Mirror the REPL's research entry: index the newest brief
					// into project memory for recall. Best-effort like
					// indexNewestBrief itself — failures are narrated on
					// stderr, never fail the already-completed research.
					if proj, err := resolveGlobalTarget(""); err != nil {
						logStatus("pipeline research: brief not indexed: %v", err)
					} else if m, err := d.managerForProject(proj); err != nil {
						logStatus("pipeline research: brief not indexed: %v", err)
					} else {
						indexNewestBrief(ctx, m.mem, d.emb, filepath.Join(proj.Path, proj.ResearchDir))
					}
				case "review":
					if err := cmdReview(d.a, nil); err != nil {
						return nil, fmt.Errorf("pipeline review: %w", err)
					}
				case "intel":
					// Mirror the REPL's intel entry (cmdIntel with the focused
					// project's name): the MCP server's "current" project is
					// its cwd project — the launcher starts `styx mcp` in the
					// project dir — resolved via resolveGlobalTarget exactly
					// like research/review/auto resolve theirs internally.
					proj, err := resolveGlobalTarget("")
					if err != nil {
						return nil, fmt.Errorf("pipeline intel: %w", err)
					}
					if err := cmdIntel(d.a, []string{proj.Name}); err != nil {
						return nil, fmt.Errorf("pipeline intel: %w", err)
					}
				case "auto":
					if err := cmdAuto(d.a, []string{in.Arg}); err != nil {
						return nil, fmt.Errorf("pipeline auto: %w", err)
					}
				}
				return map[string]any{"pipeline": in.Pipeline, "done": true,
					"note": "artifacts under styx/research/ and styx/plans/; runs ls for pipeline state"}, nil
			},
		},
		{
			Name: "rate_dispatch",
			Description: "Rate a recent dispatch outcome as notably good or bad (feeds styx learn). " +
				"Rate only notable outcomes — not every dispatch.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"thread_or_task": map[string]any{"type": "string", "description": "thread name or background task id; the most recent matching outcome is rated"},
				"ok":             map[string]any{"type": "boolean", "description": "true = notably good, false = notably bad"},
				"note":           map[string]any{"type": "string", "description": "one line on why (optional)"},
			}, "required": []string{"thread_or_task", "ok"}},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in struct {
					ThreadOrTask string `json:"thread_or_task"`
					OK           bool   `json:"ok"`
					Note         string `json:"note"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("rate_dispatch args: %w", err)
				}
				if in.ThreadOrTask == "" {
					return nil, fmt.Errorf("thread_or_task is required")
				}
				id, err := d.a.tracker.RateOutcome(ctx, in.ThreadOrTask, in.OK, in.Note)
				if err != nil {
					return nil, err
				}
				return map[string]any{"rated": true, "outcome_id": id, "target": in.ThreadOrTask}, nil
			},
		},
		{
			Name: "collect",
			Description: "Fetch background dispatch results. With task_id: that task's result " +
				"(or its live status). Without: every finished-unclaimed result plus one-line " +
				"summaries of running/queued tasks.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "task id from dispatch background:true; omit to collect everything finished"},
			}},
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in struct {
					TaskID string `json:"task_id"`
				}
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("collect args: %w", err)
				}
				if in.TaskID != "" {
					tk, ok := d.reg.Get(in.TaskID)
					if !ok {
						return nil, fmt.Errorf("unknown task %q — thread_status lists live and unclaimed tasks", in.TaskID)
					}
					return collectOne(d.reg, tk), nil
				}
				results := []map[string]any{}
				pending := []string{}
				for _, tk := range d.reg.Snapshot() {
					switch tk.State {
					case taskQueued, taskRunning:
						pending = append(pending, taskLine(tk))
					default:
						if !tk.Claimed {
							results = append(results, collectOne(d.reg, tk))
						}
					}
				}
				return map[string]any{"results": results, "pending": pending}, nil
			},
		},
	}
}
