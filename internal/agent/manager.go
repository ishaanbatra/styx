package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/memory"
)

// distillPrompt asks a thread for a structured handoff before restart.
const distillPrompt = `Summarize the state of this work as a structured handoff for a fresh session: decisions made, files touched, work in flight, and dead ends to avoid. Short bullet lists; be dense.`

// DispatchSpec is one routed message to an agent thread.
type DispatchSpec struct {
	Thread     string // thread name; "" defaults to the CLI name
	CLI        string // claude | codex | agy
	Model      string // resolved model id (tier mapping already applied)
	Message    string
	Extra      []string // extra CLI options from the brain (e.g. --add-dir)
	ExtraRoots []string // absolute repo roots attached via --add-dir (cross-repo dispatch)
	ReadOnly   bool     // true for read-class work; adapters should avoid pre-granted writes
}

// Manager owns a project's agent threads: lazy start, context metering,
// distill-and-restart, crash recovery, budget recording, interactive handoff.
type Manager struct {
	Project      config.Project
	ProjectID    string // stable id of the bound project; tags budget events
	RunID        string // session run-id; tags budget events
	Threads      *ThreadStore
	Adapters     map[string]Adapter
	Budget       *budget.Tracker                                        // nil ok (tests)
	Mem          *memory.Store                                          // nil ok; distillations land here
	Emb          memory.Embedder                                        // nil ok
	Summarize    func(ctx context.Context, text string) (string, error) // cheap local summarizer
	ThresholdPct float64                                                // distill when context exceeds this percent of window
	DistillModel string                                                 // model for distill turns (haiku tier)
	Timeout      time.Duration
	OnCompact    func(threadName string) // REPL shows "thread compacted"; may be nil
}

// Dispatch sends one message to a thread, lazily creating it, and handles
// the full lifecycle: seeding, crash recovery, real-usage budget recording,
// and distill-and-restart at the context threshold.
func (m *Manager) Dispatch(ctx context.Context, spec DispatchSpec, onEvent func(Event)) (TurnResult, error) {
	ad, ok := m.Adapters[spec.CLI]
	if !ok {
		return TurnResult{}, fmt.Errorf("no adapter for CLI %q", spec.CLI)
	}
	name := spec.Thread
	if name == "" {
		name = spec.CLI
	}
	th := m.Threads.Get(name, spec.CLI)
	run := &Runner{Adapter: ad, Thread: th, WorkDir: m.Project.Path, Timeout: m.Timeout, OnEvent: onEvent}

	extra := append(append([]string{}, spec.Extra...), addDirArgs(spec.ExtraRoots)...)
	msg := m.seedMessage(th, ad, spec.Message)
	res, err := run.Send(ctx, msg, spec.Model, extra, spec.ReadOnly)
	if err != nil && th.SessionID != "" && ad.SupportsResume() {
		// Crash recovery: the CLI's session may be gone. Roll back to the last
		// checkpoint and rebuild from distillation + rolling summary.
		th.SessionID = ""
		msg = m.seedMessage(th, ad, spec.Message)
		res, err = run.Send(ctx, msg, spec.Model, extra, spec.ReadOnly)
	}
	m.record(ctx, spec, res, err)
	if err != nil {
		_ = m.Threads.Save()
		return TurnResult{}, err
	}
	if !ad.SupportsResume() {
		m.updateRollingSummary(ctx, th, spec.Message, res.Text)
	}
	m.maybeDistill(ctx, th, ad)
	if err := m.Threads.Save(); err != nil {
		return res, fmt.Errorf("save threads: %w", err)
	}
	return res, nil
}

// seedMessage prepares the outbound message according to the thread's state:
// fresh threads get a role line, restarted threads get the last distillation,
// non-resume threads get the rolling summary.
func (m *Manager) seedMessage(th *Thread, ad Adapter, msg string) string {
	if ad.SupportsResume() {
		if th.SessionID != "" {
			return msg
		}
		var parts []string
		if th.LastDistillation != "" {
			parts = append(parts, "Handoff from the previous session of this thread:\n"+th.LastDistillation)
		} else if th.Summary != "" {
			// One-time transition: threads created while this CLI was
			// summary-based (pre-native-resume codex) seed from that summary.
			parts = append(parts, "Context from earlier in this conversation:\n"+th.Summary)
		} else if th.Turns == 0 {
			parts = append(parts, fmt.Sprintf(
				"You are the long-running %q agent thread of styx for project %s. Project context auto-loads from .claude/context.md when present.",
				th.Name, m.Project.Name))
		}
		parts = append(parts, msg)
		return strings.Join(parts, "\n\n")
	}
	if th.Summary == "" {
		return msg
	}
	return "Context from earlier in this conversation:\n" + th.Summary + "\n\nUser: " + msg
}

// record logs real token usage (from stream-json events) to the budget DB,
// replacing len/4 estimates for cloud channels.
func (m *Manager) record(ctx context.Context, spec DispatchSpec, res TurnResult, sendErr error) {
	if m.Budget == nil {
		return
	}
	kind := ""
	if sendErr != nil {
		kind = "other"
	}
	_ = m.Budget.Record(ctx, budget.Event{
		Channel:   spec.CLI,
		Verb:      "thread",
		Model:     spec.Model,
		TokensIn:  res.InputTokens,
		TokensOut: res.OutputTokens,
		Success:   sendErr == nil,
		ErrorKind: kind,
		Project:   m.ProjectID,
		RunID:     m.RunID,
	})
}

// maybeDistill restarts a resume-capable thread when its context crosses the
// threshold: ask the session itself for a handoff (cheap tier), save it to
// memory, and clear the session so the next turn seeds fresh.
func (m *Manager) maybeDistill(ctx context.Context, th *Thread, ad Adapter) {
	if !ad.SupportsResume() || th.SessionID == "" || ad.ContextWindow() <= 0 || m.ThresholdPct <= 0 {
		return
	}
	pct := float64(th.ContextTokens) / float64(ad.ContextWindow()) * 100
	if pct < m.ThresholdPct {
		return
	}
	run := &Runner{Adapter: ad, Thread: th, WorkDir: m.Project.Path, Timeout: m.Timeout}
	res, err := run.Send(ctx, distillPrompt, m.DistillModel, nil, false)
	if err != nil || res.Text == "" {
		return // best-effort; the next turn will retry
	}
	th.LastDistillation = res.Text
	th.SessionID = ""
	th.ContextTokens = 0
	if m.OnCompact != nil {
		m.OnCompact(th.Name)
	}
	m.saveMemory(ctx, memory.KindDistillation, res.Text, "thread/"+th.Name)
}

// updateRollingSummary maintains styx-side continuity for non-resume CLIs.
func (m *Manager) updateRollingSummary(ctx context.Context, th *Thread, userMsg, reply string) {
	if m.Summarize == nil {
		return
	}
	convo := th.Summary + "\nUser: " + userMsg + "\nAgent: " + reply
	if sum, err := m.Summarize(ctx, convo); err == nil && sum != "" {
		th.Summary = sum
	}
}

// saveMemory embeds and stores text; failures are non-fatal (memory is an
// enhancement, never a blocker).
func (m *Manager) saveMemory(ctx context.Context, kind memory.Kind, text, source string) {
	if m.Mem == nil || m.Emb == nil {
		return
	}
	vec, err := m.Emb.Embed(ctx, text)
	if err != nil {
		return
	}
	_, _ = m.Mem.Add(ctx, memory.Item{
		Kind: kind, Text: text, Source: source,
		Project: m.Project.Name, Scope: "thread", Confidence: 0.8, Embedding: vec,
	})
}

// addDirArgs renders extra repo roots as repeated --add-dir <root> flags.
// All three agent CLIs (claude, codex, agy) accept --add-dir.
func addDirArgs(roots []string) []string {
	var out []string
	for _, r := range roots {
		if r != "" {
			out = append(out, "--add-dir", r)
		}
	}
	return out
}

// StatusLines renders one line per thread for the brain and /status.
func (m *Manager) StatusLines() []string {
	names := make([]string, 0, len(m.Threads.Threads))
	for n := range m.Threads.Threads {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []string{}
	for _, n := range names {
		th := m.Threads.Threads[n]
		win := 200000
		if ad, ok := m.Adapters[th.CLI]; ok && ad.ContextWindow() > 0 {
			win = ad.ContextWindow()
		}
		pct := float64(th.ContextTokens) / float64(win) * 100
		out = append(out, fmt.Sprintf("%s (%s): %d turns, context %.0f%%", n, th.CLI, th.Turns, pct))
	}
	return out
}

// Handoff opens interactive claude on the thread's session (zoom-in), then
// ingests a summary back into the thread and memory on exit.
//
// Note: claude's interactive --resume forks the session, so the post-handoff
// summary turn sees the pre-handoff context; the ingest is best-effort.
func (m *Manager) Handoff(ctx context.Context, threadName string) error {
	th, ok := m.Threads.Threads[threadName]
	if !ok || th.CLI != "claude" {
		return fmt.Errorf("handoff requires an existing claude thread (got %q)", threadName)
	}
	ad := m.Adapters["claude"]
	args := []string{}
	if th.SessionID != "" {
		args = append(args, "--resume", th.SessionID)
	}
	cmd := exec.CommandContext(ctx, ad.Bin(), args...)
	cmd.Dir = m.Project.Path
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("interactive claude: %w", err)
	}
	run := &Runner{Adapter: ad, Thread: th, WorkDir: m.Project.Path, Timeout: m.Timeout}
	res, err := run.Send(ctx,
		"An interactive working session on this thread just ended. Summarize what was likely accomplished and what follow-ups remain, based on this conversation so far.",
		m.DistillModel, nil, false)
	if err == nil && res.Text != "" {
		th.Summary = res.Text
		m.saveMemory(ctx, memory.KindDistillation, res.Text, "handoff/"+threadName)
	}
	return m.Threads.Save()
}
