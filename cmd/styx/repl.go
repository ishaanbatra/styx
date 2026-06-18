package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/audit"
	"github.com/ishaanbatra/styx/internal/brain"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
	"github.com/ishaanbatra/styx/internal/paths"
)

const maxRecentTurns = 8

// replSession is one conversational styx session for one project. The brain
// routes each utterance; agent threads, pipelines, and memory do the work.
type replSession struct {
	proj       config.Project
	brain      brain.Brain
	mgr        *agent.Manager
	mem        *memory.Store // per-project
	glob       *memory.Store // cross-project
	emb        memory.Embedder
	audit      *audit.Logger
	tiers      map[string]string
	fableCap   int
	tracker    *budget.Tracker
	pipelines  map[string]func(ctx context.Context, arg string) error
	ollamaSend func(ctx context.Context, model, prompt string) (string, error)
	assumeYes  bool // --yes / non-interactive: skip ship-risk confirmations
	in         *bufio.Reader
	out        io.Writer
	outMu      sync.Mutex
	summary    string
	recent     []string
	lastAction *brain.Action
}

// turn runs one full loop iteration: recall -> decide -> act.
func (s *replSession) turn(ctx context.Context, utterance string) error {
	s.auditf(audit.KindTurn, utterance, nil)
	hits, err := memory.Recall(ctx, s.emb, utterance, 5, s.mem, s.glob)
	if err != nil {
		hits = nil // recall is an enhancement, never a blocker
	}
	t := brain.Turn{
		Utterance:    utterance,
		Summary:      s.summary,
		RecentTurns:  s.recent,
		ThreadStatus: s.mgr.StatusLines(),
		MemoryHits:   renderHits(hits),
	}
	act, err := s.brain.Decide(ctx, t)
	if err != nil {
		if errors.Is(err, brain.ErrNeedUser) {
			return s.askUserRoute(ctx, utterance)
		}
		return err
	}
	s.lastAction = &act
	s.auditf(audit.KindDecision, string(act.Action), map[string]string{
		"risk":       string(act.EffectiveRisk()),
		"confidence": fmt.Sprintf("%.2f", act.Confidence),
	})
	return s.execute(ctx, utterance, act)
}

func (s *replSession) execute(ctx context.Context, utterance string, act brain.Action) error {
	if act.EffectiveRisk() == brain.RiskShip {
		confirmed := s.confirmRisk(act)
		result := "accepted"
		if !confirmed {
			result = "declined"
		}
		s.auditf(audit.KindRiskPrompt, riskSummary(act), map[string]string{"result": result})
		if !confirmed {
			s.println("◆ cancelled - ship-risk action declined")
			s.pushRecent(utterance, "(cancelled: ship-risk)")
			return nil
		}
	}
	switch act.Action {
	case brain.ActionReply:
		s.println(act.Reply)
		s.pushRecent(utterance, act.Reply)
		return nil
	case brain.ActionDispatch, brain.ActionParallelDispatch:
		return s.runDispatches(ctx, utterance, act.Dispatches)
	case brain.ActionPipeline:
		s.println(fmt.Sprintf("◆ pipeline › %s", act.Pipeline))
		s.auditf(audit.KindPipeline, act.Pipeline, nil)
		fn, ok := s.pipelines[act.Pipeline]
		if !ok {
			return fmt.Errorf("no pipeline %q wired", act.Pipeline)
		}
		err := fn(ctx, utterance)
		s.pushRecent(utterance, "(ran "+act.Pipeline+" pipeline)")
		return err
	case brain.ActionHandoff:
		thread := "claude"
		if len(act.Dispatches) > 0 && act.Dispatches[0].Thread != "" {
			thread = act.Dispatches[0].Thread
		}
		s.println(fmt.Sprintf("◆ handoff › opening interactive %s (exit to return to styx)", thread))
		s.mgr.Threads.Get(thread, thread)
		err := s.mgr.Handoff(ctx, thread)
		s.pushRecent(utterance, "(interactive handoff)")
		return err
	case brain.ActionRemember:
		return s.saveMemoryText(ctx, act.Remember)
	default:
		return s.askUserRoute(ctx, utterance)
	}
}

// runDispatches executes one or more dispatches; multiple run concurrently
// with output serialized through s.println.
func (s *replSession) runDispatches(ctx context.Context, utterance string, ds []brain.Dispatch) error {
	if len(ds) == 0 {
		return errors.New("brain returned a dispatch with no dispatches")
	}
	var wg sync.WaitGroup
	errs := make([]error, len(ds))
	for i, d := range ds {
		requestedModel := s.defaultModel(d)
		model, degraded := s.resolveModel(requestedModel)
		line := fmt.Sprintf("◆ %s·%s › %s", d.Thread, requestedModel, d.Rationale)
		if degraded {
			line += " (fable hot this week → opus)"
		}
		s.println(line)
		wg.Add(1)
		go func(i int, d brain.Dispatch, model string) {
			defer wg.Done()
			errs[i] = s.runOneDispatch(ctx, d, model)
		}(i, d, model)
	}
	wg.Wait()
	s.pushRecent(utterance, fmt.Sprintf("(dispatched to %d thread(s))", len(ds)))
	return errors.Join(errs...)
}

func (s *replSession) runOneDispatch(ctx context.Context, d brain.Dispatch, model string) error {
	s.auditf(audit.KindDispatch, d.Thread+"·"+model, map[string]string{"msg": d.Message})
	if d.Thread == "ollama" {
		if s.ollamaSend == nil {
			return errors.New("ollama dispatch not wired")
		}
		text, err := s.ollamaSend(ctx, model, d.Message)
		if err != nil {
			return fmt.Errorf("ollama: %w", err)
		}
		s.println(text)
		return nil
	}
	res, err := s.mgr.Dispatch(ctx, agent.DispatchSpec{
		Thread:   d.Thread,
		CLI:      d.Thread,
		Model:    model,
		Message:  d.Message,
		Extra:    d.CLIOptions,
		ReadOnly: d.Risk == brain.RiskRead,
	}, s.printEvent)
	if err != nil {
		return fmt.Errorf("%s: %w", d.Thread, err)
	}
	if ad, ok := s.mgr.Adapters[d.Thread]; ok && !ad.SupportsStream() {
		s.println(res.Text)
	}
	return nil
}

func (s *replSession) confirmRisk(act brain.Action) bool {
	if s.assumeYes {
		return true
	}
	s.print(fmt.Sprintf("⚠ this will %s - proceed? [y/N]: ", riskSummary(act)))
	line, err := s.in.ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func riskSummary(act brain.Action) string {
	if act.Action == brain.ActionPipeline {
		return "run the " + act.Pipeline + " pipeline (may commit/push/open a PR)"
	}
	return "perform a ship-risk action (commit/push/deploy)"
}

// printEvent renders streamed agent events into the REPL.
func (s *replSession) printEvent(e agent.Event) {
	switch e.Type {
	case agent.EventText:
		s.println(e.Text)
	case agent.EventResult:
		// Final text already streamed as EventText for claude; print nothing.
	}
}

// defaultModel fills d.Model when the brain omitted it.
func (s *replSession) defaultModel(d brain.Dispatch) string {
	if d.Model != "" {
		return d.Model
	}
	if d.Thread == "ollama" {
		return "qwen2.5-coder:14b"
	}
	return "sonnet"
}

// resolveModel maps a tier to a CLI model id, degrading fable -> opus when
// the weekly fable budget runs hot. Non-tier strings pass through.
func (s *replSession) resolveModel(tier string) (string, bool) {
	m, ok := s.tiers[tier]
	if !ok {
		return tier, false
	}
	if tier == "fable" && s.fableHot() {
		if opus, ok := s.tiers["opus"]; ok {
			return opus, true
		}
	}
	return m, false
}

func (s *replSession) fableHot() bool {
	if s.fableCap <= 0 || s.tracker == nil {
		return false
	}
	n, err := s.tracker.ModelCount(context.Background(), "claude", s.tiers["fable"], budget.WindowWeek)
	return err == nil && n >= s.fableCap
}

// askUserRoute is the never-brick path: ollama is down or the brain emitted
// garbage twice, so the user routes this turn manually.
func (s *replSession) askUserRoute(ctx context.Context, utterance string) error {
	s.print("brain unavailable - which thread? [claude/codex/agy/ollama/skip]: ")
	line, err := s.in.ReadString('\n')
	if err != nil {
		return fmt.Errorf("read routing choice: %w", err)
	}
	choice := strings.TrimSpace(line)
	switch choice {
	case "", "skip":
		s.println("skipped")
		return nil
	case "claude", "codex", "agy", "ollama":
		d := brain.Dispatch{Thread: choice, Message: utterance, Rationale: "manual route"}
		return s.runDispatches(ctx, utterance, []brain.Dispatch{d})
	default:
		s.println("unknown thread " + choice + "; skipped")
		return nil
	}
}

// saveMemoryText stores an explicit remember action. Routing corrections
// (prefixed "routing-preference: " by the brain) get their own kind so recall
// can teach the brain this user's preferences.
func (s *replSession) saveMemoryText(ctx context.Context, text string) error {
	kind := memory.KindFact
	scope := "general"
	confidence := 1.0
	if rest, ok := strings.CutPrefix(text, "routing-preference:"); ok {
		kind = memory.KindRoutingPreference
		// One-off corrections start low and decay; the brain only leans on them
		// when they recur. An optional "scope: <x>" hint narrows them.
		confidence = 0.6
		scope = parseScope(rest)
	}
	vec, err := s.emb.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed memory: %w", err)
	}
	if _, err := s.mem.Add(ctx, memory.Item{
		Kind: kind, Text: text, Source: "repl",
		Project: s.proj.Name, Scope: scope, Confidence: confidence, Embedding: vec,
	}); err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	s.auditf(audit.KindMemoryWrite, text, nil)
	s.println("◆ remembered")
	return nil
}

// parseScope pulls an optional "scope: <tag>" hint out of a routing preference
// ("...scope: reviews"); defaults to "general".
func parseScope(s string) string {
	i := strings.Index(s, "scope:")
	if i < 0 {
		return "general"
	}
	tag := strings.TrimSpace(s[i+len("scope:"):])
	if j := strings.IndexAny(tag, ".\n;"); j >= 0 {
		tag = tag[:j]
	}
	if tag = strings.TrimSpace(tag); tag == "" {
		return "general"
	}
	return tag
}

func (s *replSession) pushRecent(utterance, outcome string) {
	s.recent = append(s.recent, "user: "+utterance, "styx: "+outcome)
	if len(s.recent) > maxRecentTurns {
		s.recent = s.recent[len(s.recent)-maxRecentTurns:]
	}
}

func (s *replSession) println(line string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	fmt.Fprintln(s.out, line)
}

func (s *replSession) print(text string) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	fmt.Fprint(s.out, text)
}

func (s *replSession) auditf(kind audit.Kind, detail string, meta map[string]string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Append(audit.Record{Kind: kind, Detail: detail, Meta: meta})
}

func renderHits(hits []memory.Hit) []string {
	var out []string
	for _, h := range hits {
		meta := string(h.Item.Kind)
		if h.Item.Scope != "" && h.Item.Scope != "general" {
			meta += "; scope " + h.Item.Scope
		}
		if h.Item.Confidence > 0 && h.Item.Confidence < 1 {
			meta += fmt.Sprintf("; conf %.1f", h.Item.Confidence)
		}
		out = append(out, fmt.Sprintf("[%s] %s", meta, h.Item.Text))
	}
	return out
}

// lastActionJSON renders the previous routing decision for /why.
func (s *replSession) lastActionJSON() string {
	if s.lastAction == nil {
		return "(no decision yet)"
	}
	b, err := json.MarshalIndent(s.lastAction, "", "  ")
	if err != nil {
		return fmt.Sprintf("(%v)", err)
	}
	return string(b)
}

// newREPLSession wires a production session for the current project. The
// returned cleanup closes the memory stores and audit log.
func newREPLSession(a *app) (*replSession, func(), error) {
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return nil, nil, err
	}
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, nil, err
	}
	mem, err := memory.Open(filepath.Join(memDir, proj.Name+".db"))
	if err != nil {
		return nil, nil, fmt.Errorf("open project memory: %w", err)
	}
	glob, err := memory.Open(filepath.Join(memDir, "global.db"))
	if err != nil {
		mem.Close()
		return nil, nil, fmt.Errorf("open global memory: %w", err)
	}
	cleanup := func() {
		mem.Close()
		glob.Close()
	}

	emb := memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel)
	threads, err := agent.LoadThreads(proj.Name)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	auditDir, err := paths.AuditDir()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	projAudit := filepath.Join(auditDir, proj.Name)
	if err := paths.EnsureDir(projAudit); err != nil {
		cleanup()
		return nil, nil, err
	}
	al, err := audit.Open(filepath.Join(projAudit, time.Now().Format("20060102-150405")+".jsonl"))
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	cleanup = func() {
		mem.Close()
		glob.Close()
		al.Close()
	}

	och := rawChannel(a.channels["ollama"])
	summarize := func(ctx context.Context, text string) (string, error) {
		resp, err := och.Send(ctx, channel.Request{
			Model:  a.routing.Brain.Model,
			Prompt: "Compress this conversation state into a dense summary preserving decisions, open questions, and file references:\n\n" + text,
		})
		return resp.Text, err
	}

	timeout := 10 * time.Minute
	if a.routing.Budget.Claude.TimeoutMinutes > 0 {
		timeout = time.Duration(a.routing.Budget.Claude.TimeoutMinutes) * time.Minute
	}
	mgr := &agent.Manager{
		Project: proj,
		Threads: threads,
		Adapters: map[string]agent.Adapter{
			"claude": agent.NewClaudeAdapter(),
			"codex":  agent.NewCodexAdapter(),
			"agy":    agent.NewAgyAdapter(),
		},
		Budget:       a.tracker,
		Mem:          mem,
		Emb:          emb,
		Summarize:    summarize,
		ThresholdPct: a.routing.Brain.ContextThresholdPct,
		DistillModel: a.routing.Tiers["haiku"],
		Timeout:      timeout,
	}

	b := &brain.Ollama{
		BaseURL:             "http://localhost:11434",
		Model:               a.routing.Brain.Model,
		ConfidenceThreshold: a.routing.Brain.ConfidenceThreshold,
		Escalator: &brain.ClaudeEscalator{
			Channel: rawChannel(a.channels["claude"]),
			Model:   a.routing.Tiers["haiku"],
		},
	}

	s := &replSession{
		proj:     proj,
		brain:    b,
		mgr:      mgr,
		mem:      mem,
		glob:     glob,
		emb:      emb,
		audit:    al,
		tiers:    a.routing.Tiers,
		fableCap: a.routing.Brain.FableWeeklyCap,
		tracker:  a.tracker,
		pipelines: map[string]func(ctx context.Context, arg string) error{
			"research": func(_ context.Context, arg string) error {
				err := cmdResearch(a, []string{arg})
				if err == nil {
					indexNewestBrief(context.Background(), mem, emb, filepath.Join(proj.Path, proj.ResearchDir))
				}
				return err
			},
			"auto":   func(_ context.Context, arg string) error { return cmdAuto(a, []string{arg}) },
			"review": func(_ context.Context, _ string) error { return cmdReview(a, nil) },
			"intel":  func(_ context.Context, _ string) error { return cmdIntel(a, []string{proj.Name}) },
		},
		ollamaSend: func(ctx context.Context, model, prompt string) (string, error) {
			resp, err := a.channels["ollama"].Send(ctx, channel.Request{Model: model, Prompt: prompt})
			return resp.Text, err
		},
		in:  bufio.NewReader(os.Stdin),
		out: os.Stdout,
	}
	mgr.OnCompact = func(name string) { s.println("↻ " + name + " thread compacted") }
	return s, cleanup, nil
}

// indexNewestBrief stores the freshest research brief in memory (best-effort).
func indexNewestBrief(ctx context.Context, mem *memory.Store, emb memory.Embedder, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return
	}
	var newest os.DirEntry
	var newestTime time.Time
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		if info.ModTime().After(newestTime) {
			newest, newestTime = e, info.ModTime()
		}
	}
	if newest == nil {
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, newest.Name()))
	if err != nil {
		return
	}
	text := string(b)
	if len(text) > 4000 {
		text = text[:4000]
	}
	vec, err := emb.Embed(ctx, text)
	if err != nil {
		return
	}
	_, _ = mem.Add(ctx, memory.Item{
		Kind: memory.KindBrief, Text: text, Source: "pipeline/research:" + newest.Name(), Embedding: vec,
	})
}

// cmdREPL is bare `styx`: the persistent conversational session.
func cmdREPL(a *app) error {
	s, cleanup, err := newREPLSession(a)
	if err != nil {
		return err
	}
	defer cleanup()
	fmt.Printf("styx — %s · /status /budget /threads /why /audit /quit\n", s.proj.Name)
	for {
		fmt.Print("styx› ")
		line, err := s.in.ReadString('\n')
		if err != nil {
			s.endSession()
			fmt.Println()
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if quit := s.slash(line); quit {
				s.endSession()
				return nil
			}
			continue
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		if err := s.turn(ctx, line); err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Fprintln(os.Stderr, "✗ interrupted")
			} else {
				fmt.Fprintf(os.Stderr, "✗ %v\n", err)
			}
		}
		stop()
	}
}

// slash handles REPL slash commands; returns true to quit.
func (s *replSession) slash(line string) bool {
	switch strings.Fields(line)[0] {
	case "/quit", "/exit":
		return true
	case "/status", "/threads":
		lines := s.mgr.StatusLines()
		if len(lines) == 0 {
			s.println("no threads yet (they start lazily on first dispatch)")
		}
		for _, l := range lines {
			s.println(meterize(l))
		}
	case "/budget":
		if err := cmdBudget(nil); err != nil {
			s.println("budget: " + err.Error())
		}
	case "/why":
		s.println(s.lastActionJSON())
	case "/audit":
		if s.audit == nil {
			s.println("(audit unavailable)")
			return false
		}
		recs, err := s.audit.Tail(20)
		if err != nil {
			s.println("(audit unavailable: " + err.Error() + ")")
			return false
		}
		for _, r := range recs {
			s.println(fmt.Sprintf("%s  %-12s %s", r.At.Format("15:04:05"), r.Kind, r.Detail))
		}
	default:
		s.println("unknown command (try /status /budget /threads /why /audit /quit)")
	}
	return false
}

// meterize appends a 5-cell context meter to a "... context NN%" status line.
func meterize(line string) string {
	i := strings.LastIndex(line, "context ")
	if i < 0 {
		return line
	}
	var pct float64
	if _, err := fmt.Sscanf(line[i:], "context %f%%", &pct); err != nil {
		return line
	}
	filled := int(pct / 20)
	if filled > 5 {
		filled = 5
	}
	return line + "  " + strings.Repeat("▮", filled) + strings.Repeat("▯", 5-filled)
}

// endSession writes a session-end summary to project memory (best-effort).
func (s *replSession) endSession() {
	if len(s.recent) == 0 || s.mgr.Summarize == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sum, err := s.mgr.Summarize(ctx, strings.Join(s.recent, "\n"))
	if err != nil || sum == "" {
		return
	}
	vec, err := s.emb.Embed(ctx, sum)
	if err != nil {
		return
	}
	_, _ = s.mem.Add(ctx, memory.Item{
		Kind: memory.KindDistillation, Text: sum, Source: "repl-session",
		Project: s.proj.Name, Scope: "thread", Confidence: 0.9, Embedding: vec,
	})
}

// cmdBrainTurn is `styx "..."`: one brain turn, then exit.
func cmdBrainTurn(a *app, utterance string) error {
	s, cleanup, err := newREPLSession(a)
	if err != nil {
		return err
	}
	defer cleanup()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return s.turn(ctx, utterance)
}
