package main

// Awaited-dispatch observer (spec: docs/superpowers/specs/
// 2026-07-08-styx-awaited-dispatch-design.md). An awaited dispatch spawns
// ordinary registry background tasks; awaitTasks watches them until every
// one is terminal, streaming a board-derived progress line via MCP
// notifications, then claims each task (results are delivered inline) and
// returns the combined results. Cancellation — the host's Esc arriving as
// notifications/cancelled, or the server's EOF drain — detaches instead:
// the observer returns immediately, claims nothing, and the tasks keep
// running on the server's root context as collectible background work.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/agent"
)

// awaitTick is the observer cadence. The mirror throttle (2s) and the ollama
// watcher note (15s) ride on top of it; identical lines are not re-emitted.
// A var so tests can shrink it.
var awaitTick = 1 * time.Second

// awaitOutcome is what awaiting a set of tasks produced.
type awaitOutcome struct {
	Detached bool
	Results  []map[string]any // per task, in ids order; nil when detached
}

// awaitTasks blocks until every id is terminal or ctx is cancelled.
// notify may be nil (the client sent no progressToken); progress floats
// carry the finished-task count.
func (d *conductorDeps) awaitTasks(ctx context.Context, ids []string, notify func(float64, string)) awaitOutcome {
	awaited := map[string]bool{}
	for _, id := range ids {
		awaited[id] = true
	}
	// Seed announced with everything already terminal so only completions
	// that happen DURING this await are narrated (exactly once each).
	announced := map[string]bool{}
	for _, tk := range d.reg.Snapshot() {
		if !awaited[tk.ID] && isTerminal(tk.State) {
			announced[tk.ID] = true
		}
	}
	lastLine := ""
	t := time.NewTicker(awaitTick)
	defer t.Stop()
	for {
		done := 0
		for _, id := range ids {
			// A missing id (impossible for freshly spawned tasks, but the
			// registry is the source of truth) counts terminal rather than
			// spinning forever.
			if tk, ok := d.reg.Get(id); !ok || isTerminal(tk.State) {
				done++
			}
		}
		if done == len(ids) {
			results := make([]map[string]any, 0, len(ids))
			for _, id := range ids {
				tk, ok := d.reg.Get(id)
				if !ok {
					continue
				}
				results = append(results, collectOne(d.reg, tk)) // claims terminal tasks
			}
			return awaitOutcome{Results: results}
		}
		if line := d.awaitLine(ids, awaited, announced, done); line != lastLine && notify != nil {
			lastLine = line
			notify(float64(done), line)
		}
		select {
		case <-ctx.Done():
			return awaitOutcome{Detached: true}
		case <-t.C:
		}
	}
}

// awaitLine renders one compact progress line: done count, one heartbeat per
// awaited task, one-time notices for unrelated tasks that finished during
// the await, and the ollama watcher note when present. Board access is
// nil-safe (mechanical layer never requires it).
func (d *conductorDeps) awaitLine(ids []string, awaited, announced map[string]bool, done int) string {
	states := map[string]activity.AgentState{}
	note := ""
	if d.board != nil {
		for _, s := range d.board.Snapshot() {
			states[s.Label] = s
		}
		note = d.board.WatcherNote()
	}
	now := time.Now()
	parts := []string{fmt.Sprintf("%d/%d done", done, len(ids))}
	for _, id := range ids {
		if tk, ok := d.reg.Get(id); ok {
			parts = append(parts, taskHeartbeat(tk, states, now))
		}
	}
	for _, tk := range d.reg.Snapshot() {
		if !awaited[tk.ID] && isTerminal(tk.State) && !announced[tk.ID] {
			announced[tk.ID] = true
			parts = append(parts, fmt.Sprintf("%s %s — collect", tk.ID, tk.State))
		}
	}
	if note != "" {
		parts = append(parts, "watch: "+note)
	}
	return strings.Join(parts, " · ")
}

// taskHeartbeat renders one awaited task's live state in the same vocabulary
// as activity.Render (▸ / ⚠ / ✓ / ✗), sourced from the board entry keyed by
// the task's project-qualified thread label.
func taskHeartbeat(tk bgTask, states map[string]activity.AgentState, now time.Time) string {
	switch tk.State {
	case taskQueued:
		behind := "at cap"
		if tk.QueuedBehind != "" {
			behind = "behind " + tk.QueuedBehind
		}
		return fmt.Sprintf("%s %s queued %s (%s)", tk.ID, tk.Spec.CLI, behind, elapsedShort(now.Sub(tk.Created)))
	case taskRunning:
		s, ok := states[agent.BoardLabel(tk.Spec.ProjectID, tk.Spec.Thread)]
		if !ok || s.Last == "" {
			return fmt.Sprintf("%s %s ▸ running (%s)", tk.ID, tk.Spec.CLI, elapsedShort(now.Sub(tk.Started)))
		}
		idle := now.Sub(s.LastAt)
		if idle > activity.DefaultStall {
			return fmt.Sprintf("%s %s ⚠ idle %s (last: %s)", tk.ID, tk.Spec.CLI, elapsedShort(idle), s.Last)
		}
		return fmt.Sprintf("%s %s ▸ %s (%s)", tk.ID, tk.Spec.CLI, s.Last, elapsedShort(idle))
	case taskDone:
		return fmt.Sprintf("%s %s ✓ done", tk.ID, tk.Spec.CLI)
	default: // taskError, taskOrphaned
		return fmt.Sprintf("%s %s ✗ %s", tk.ID, tk.Spec.CLI, tk.State)
	}
}

// isTerminal reports whether a task state is final.
func isTerminal(state string) bool {
	return state == taskDone || state == taskError || state == taskOrphaned
}
