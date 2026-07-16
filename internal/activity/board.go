// Package activity is styx's live dispatch-observability substrate. Every
// agent turn writes its parsed events here as one-line summaries; renderers,
// the ollama watcher, and the cross-process disk mirror all read from it. It
// holds strings and timestamps only (no agent.Event) so it imports nothing
// from internal/agent — the dependency runs one way, agent -> activity.
package activity

import (
	"sync"
	"time"
)

// recentCap bounds the per-agent ring buffer the ollama watcher reads, keeping
// its prompt small.
const recentCap = 20

// AgentState is an immutable snapshot of one agent's liveness.
type AgentState struct {
	Label   string
	Last    string
	LastAt  time.Time
	Done    bool
	Elapsed time.Duration
	Recent  []string

	recentEvents []recentEvent
}

// recentEvent is one timestamped entry in the watcher's bounded activity
// window. It stays in memory; the cross-process mirror shape is unchanged.
type recentEvent struct {
	At      time.Time
	Summary string
}

type agentRec struct {
	last    string
	lastAt  time.Time
	done    bool
	elapsed time.Duration
	recent  []recentEvent
}

// Board is the shared, concurrency-safe liveness map for one styx session,
// keyed by agent label (thread name / task id).
type Board struct {
	mu    sync.Mutex
	now   func() time.Time
	order []string
	ag    map[string]*agentRec
	note  string
}

// NewBoard returns an empty board using the wall clock.
func NewBoard() *Board {
	return &Board{now: time.Now, ag: map[string]*agentRec{}}
}

// SetClock overrides the time source (tests).
func (b *Board) SetClock(fn func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = fn
}

// Record stamps one activity line for label, marking the agent live again.
func (b *Board) Record(label, summary string) {
	if summary == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.ag[label]
	if r == nil {
		r = &agentRec{}
		b.ag[label] = r
		b.order = append(b.order, label)
	}
	now := b.now()
	r.last = summary
	r.lastAt = now
	r.done = false
	r.recent = append(r.recent, recentEvent{At: now, Summary: summary})
	if len(r.recent) > recentCap {
		r.recent = r.recent[len(r.recent)-recentCap:]
	}
}

// Done marks label finished with its total elapsed time.
func (b *Board) Done(label string, elapsed time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.ag[label]
	if r == nil {
		r = &agentRec{}
		b.ag[label] = r
		b.order = append(b.order, label)
	}
	r.done = true
	r.elapsed = elapsed
}

// Snapshot returns per-agent state in first-seen order.
func (b *Board) Snapshot() []AgentState {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]AgentState, 0, len(b.order))
	for _, label := range b.order {
		r := b.ag[label]
		recent := make([]string, len(r.recent))
		events := make([]recentEvent, len(r.recent))
		copy(events, r.recent)
		for i, ev := range events {
			recent[i] = ev.Summary
		}
		out = append(out, AgentState{
			Label: label, Last: r.last, LastAt: r.lastAt,
			Done: r.done, Elapsed: r.elapsed, Recent: recent, recentEvents: events,
		})
	}
	return out
}

// Recent returns a copy of label's recent activity lines (oldest first).
func (b *Board) Recent(label string) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.ag[label]
	if r == nil {
		return nil
	}
	out := make([]string, len(r.recent))
	for i, ev := range r.recent {
		out[i] = ev.Summary
	}
	return out
}

// RecentEvents returns a copy of label's timestamped recent activity (oldest
// first). The entries are in-memory watcher input and are never mirrored.
func (b *Board) RecentEvents(label string) []recentEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.ag[label]
	if r == nil {
		return nil
	}
	out := make([]recentEvent, len(r.recent))
	copy(out, r.recent)
	return out
}

// clockNow reads the board's injected clock (tests) under the board lock.
func (b *Board) clockNow() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.now()
}

// SetWatcherNote stores the ollama watcher's latest health read.
func (b *Board) SetWatcherNote(note string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.note = note
}

// WatcherNote returns the latest health read ("" if none).
func (b *Board) WatcherNote() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.note
}
