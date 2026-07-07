package main

// Background task registry for conductor dispatches (B1, spec
// docs/superpowers/specs/2026-07-07-styx-async-dispatch-design.md).
// In-memory, mutex-guarded; goroutines derive from the MCP server's root
// context so background work lives and dies with the `styx mcp` process —
// no daemons. State files under ~/.config/styx/state/tasks/ mirror each
// task for crash honesty (orphan reporting), never for resumption.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/pipeline"
)

// Task states. queued|running are "live"; done|error|orphaned are terminal
// and stay visible until claimed via collect.
const (
	taskQueued   = "queued"
	taskRunning  = "running"
	taskDone     = "done"
	taskError    = "error"
	taskOrphaned = "orphaned"
)

// taskSpec is what the registry needs about a dispatch for ordering rules
// and status rendering.
type taskSpec struct {
	Project   string // registry alias as passed ("" = launch-dir project)
	ProjectID string // resolved stable project id (ordering key)
	Thread    string // resolved thread name (cli-defaulted)
	CLI       string
	Model     string
	Risk      string // read | edit (ship is rejected at spawn)
}

// bgTask is one background dispatch. Fields are guarded by the registry
// mutex; Get/Snapshot hand out copies.
type bgTask struct {
	ID           string // t1, t2, … monotonic within a server lifetime (o<n> for adopted orphans)
	RunID        string // collision-free id used for the state file name
	Spec         taskSpec
	State        string
	QueuedBehind string // blocking task id ("" = waiting on capacity, or not queued)
	Created      time.Time
	Started      time.Time
	Finished     time.Time
	Result       map[string]any
	Err          string
	Claimed      bool

	run func(context.Context, string) (map[string]any, error)
}

// taskRegistry owns every background task of one `styx mcp` process.
type taskRegistry struct {
	mu      sync.Mutex
	limit   int
	seq     int
	rootCtx context.Context
	tasks   map[string]*bgTask
	order   []string // creation order, stable listing
	dir     string   // state-file mirror dir; "" disables persistence (unit tests)
}

// newTaskRegistry builds a registry. Goroutines derive from rootCtx — the
// server's root context, NOT any tool call's — so tasks survive the spawning
// call returning and die when the server dies.
func newTaskRegistry(rootCtx context.Context, limit int) *taskRegistry {
	if limit <= 0 {
		limit = 4
	}
	return &taskRegistry{limit: limit, rootCtx: rootCtx, tasks: map[string]*bgTask{}}
}

// Spawn registers a task and starts it immediately when the cap and ordering
// rules allow, else leaves it queued. Returns the task id and initial state.
// The run func receives the assigned id so completion bookkeeping (outcome
// rows) can reference the task.
func (r *taskRegistry) Spawn(spec taskSpec, run func(context.Context, string) (map[string]any, error)) (string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("t%d", r.seq)
	t := &bgTask{
		ID:      id,
		RunID:   pipeline.NewRunID("task-" + id + "-" + spec.CLI),
		Spec:    spec,
		State:   taskQueued,
		Created: time.Now(),
		run:     run,
	}
	r.tasks[id] = t
	r.order = append(r.order, id)
	r.startEligibleLocked()
	r.persistLocked(t)
	return id, t.State
}

// startEligibleLocked promotes queued tasks in creation order while capacity
// and the ordering rules allow. Callers hold r.mu.
func (r *taskRegistry) startEligibleLocked() {
	running := 0
	for _, id := range r.order {
		if r.tasks[id].State == taskRunning {
			running++
		}
	}
	for _, id := range r.order {
		t := r.tasks[id]
		if t.State != taskQueued {
			continue
		}
		if blocker := r.conflictLocked(t); blocker != "" {
			t.QueuedBehind = blocker
			continue
		}
		if running >= r.limit {
			t.QueuedBehind = "" // waiting on capacity, not on a specific task
			continue
		}
		t.State = taskRunning
		t.QueuedBehind = ""
		t.Started = time.Now()
		running++
		logStatus("task %s started (%s, thread %s)", t.ID, t.Spec.CLI, t.Spec.Thread)
		go r.execute(t)
	}
}

// conflictLocked returns the id of the running task that blocks t, or "".
// Rule 1 — per-thread serialization: same project+thread never runs
// concurrently (session resume is stateful). Rule 2 — per-project write
// queue: an edit-risk task waits for any running edit-risk task on the same
// project; read-risk tasks run freely in parallel.
func (r *taskRegistry) conflictLocked(t *bgTask) string {
	for _, id := range r.order {
		o := r.tasks[id]
		if o.State != taskRunning {
			continue
		}
		if o.Spec.ProjectID == t.Spec.ProjectID && o.Spec.Thread == t.Spec.Thread {
			return o.ID
		}
		if t.Spec.Risk != "read" && o.Spec.Risk != "read" && o.Spec.ProjectID == t.Spec.ProjectID {
			return o.ID
		}
	}
	return ""
}

// execute runs one task to completion on its own goroutine, then promotes
// whatever its completion unblocked.
func (r *taskRegistry) execute(t *bgTask) {
	res, err := t.run(r.rootCtx, t.ID)
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Finished = time.Now()
	if err != nil {
		t.State = taskError
		t.Err = err.Error()
		logStatus("task %s failed: %v", t.ID, err)
	} else {
		t.State = taskDone
		t.Result = res
		logStatus("task %s done (%s) — collect to read it", t.ID, t.Spec.CLI)
	}
	r.persistLocked(t)
	r.startEligibleLocked()
}

// Busy reports the live (queued or running) task that would collide with a
// SYNCHRONOUS dispatch on (projectID, thread, risk) — the same two ordering
// rules, checked so a sync turn never interleaves with background work on a
// stateful session. Nil-safe.
func (r *taskRegistry) Busy(projectID, thread, risk string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.order {
		o := r.tasks[id]
		if o.State != taskRunning && o.State != taskQueued {
			continue
		}
		if o.Spec.ProjectID == projectID && o.Spec.Thread == thread {
			return o.ID, true
		}
		if risk != "read" && o.Spec.Risk != "read" && o.Spec.ProjectID == projectID {
			return o.ID, true
		}
	}
	return "", false
}

// Get returns a copy of the task (safe to read without the lock).
func (r *taskRegistry) Get(id string) (bgTask, bool) {
	if r == nil {
		return bgTask{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.tasks[id]
	if !ok {
		return bgTask{}, false
	}
	return *t, true
}

// Claim marks a finished task collected so it stops resurfacing.
func (r *taskRegistry) Claim(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tasks[id]; ok {
		t.Claimed = true
		r.persistLocked(t)
	}
}

// Snapshot returns copies of all tasks in creation order. Nil-safe.
func (r *taskRegistry) Snapshot() []bgTask {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]bgTask, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, *r.tasks[id])
	}
	return out
}

// StatusLine renders the compact piggyback line: every live task and every
// unclaimed finished one. "" when there is nothing to report. Nil-safe.
func (r *taskRegistry) StatusLine() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var parts []string
	for _, id := range r.order {
		t := r.tasks[id]
		switch t.State {
		case taskRunning:
			parts = append(parts, fmt.Sprintf("%s running (%s, %s)", t.ID, t.Spec.CLI, elapsedShort(time.Since(t.Started))))
		case taskQueued:
			if t.QueuedBehind != "" {
				parts = append(parts, fmt.Sprintf("%s queued behind %s (%s)", t.ID, t.QueuedBehind, elapsedShort(time.Since(t.Created))))
			} else {
				parts = append(parts, fmt.Sprintf("%s queued at cap (%s)", t.ID, elapsedShort(time.Since(t.Created))))
			}
		case taskDone, taskError, taskOrphaned:
			if !t.Claimed {
				parts = append(parts, fmt.Sprintf("%s %s unclaimed — call collect", t.ID, t.State))
			}
		}
	}
	return strings.Join(parts, "; ")
}

// elapsedShort renders a duration as 12s / 4m03s / 1h12m for status lines.
func elapsedShort(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// persistLocked mirrors task state to disk. No-op until Task 6 wires the
// mirror dir; callers hold r.mu.
func (r *taskRegistry) persistLocked(t *bgTask) {
	if r.dir == "" {
		return
	}
	// Implemented in Task 6 (writeTaskFile).
	writeTaskFile(r.dir, t)
}

// writeTaskFile is implemented in Task 6 (state-file mirror).
func writeTaskFile(dir string, t *bgTask) {}
