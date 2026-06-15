package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/brain"
	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
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
	tiers      map[string]string
	fableCap   int
	tracker    *budget.Tracker
	pipelines  map[string]func(ctx context.Context, arg string) error
	ollamaSend func(ctx context.Context, model, prompt string) (string, error)
	in         *bufio.Reader
	out        io.Writer
	outMu      sync.Mutex
	summary    string
	recent     []string
	lastAction *brain.Action
}

// turn runs one full loop iteration: recall -> decide -> act.
func (s *replSession) turn(ctx context.Context, utterance string) error {
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
	return s.execute(ctx, utterance, act)
}

func (s *replSession) execute(ctx context.Context, utterance string, act brain.Action) error {
	switch act.Action {
	case brain.ActionReply:
		s.println(act.Reply)
		s.pushRecent(utterance, act.Reply)
		return nil
	case brain.ActionDispatch, brain.ActionParallelDispatch:
		return s.runDispatches(ctx, utterance, act.Dispatches)
	case brain.ActionPipeline:
		s.println(fmt.Sprintf("◆ pipeline › %s", act.Pipeline))
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
			line += " (fable hot this week -> opus)"
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
		Thread:  d.Thread,
		CLI:     d.Thread,
		Model:   model,
		Message: d.Message,
		Extra:   d.CLIOptions,
	}, s.printEvent)
	if err != nil {
		return fmt.Errorf("%s: %w", d.Thread, err)
	}
	if ad, ok := s.mgr.Adapters[d.Thread]; ok && !ad.SupportsStream() {
		s.println(res.Text)
	}
	return nil
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
	if strings.HasPrefix(text, "routing-preference:") {
		kind = memory.KindRoutingPreference
	}
	vec, err := s.emb.Embed(ctx, text)
	if err != nil {
		return fmt.Errorf("embed memory: %w", err)
	}
	if _, err := s.mem.Add(ctx, memory.Item{Kind: kind, Text: text, Source: "repl", Embedding: vec}); err != nil {
		return fmt.Errorf("save memory: %w", err)
	}
	s.println("◆ remembered")
	return nil
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

func renderHits(hits []memory.Hit) []string {
	var out []string
	for _, h := range hits {
		out = append(out, fmt.Sprintf("[%s] %s", h.Item.Kind, h.Item.Text))
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
