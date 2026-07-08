package main

// Background task registry for conductor dispatches (B1, spec
// docs/superpowers/specs/2026-07-07-styx-async-dispatch-design.md).
// In-memory, mutex-guarded; goroutines derive from the MCP server's root
// context so background work lives and dies with the `styx mcp` process —
// no daemons. State files under ~/.config/styx/state/tasks/ mirror each
// task for crash honesty (orphan reporting), never for resumption.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/agent"
	"github.com/ishaanbatra/styx/internal/mcpserver"
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
	order   []string        // creation order, stable listing
	dir     string          // state-file mirror dir; "" disables persistence (unit tests)
	board   *activity.Board // liveness source for the piggyback bg line; nil ok
}

// newTaskRegistry builds a registry. Goroutines derive from rootCtx — the
// server's root context, NOT any tool call's — so tasks survive the spawning
// call returning and die when the server dies. board (nil ok) is the shared
// conductor liveness board the piggyback status line reads each task's last
// action from.
func newTaskRegistry(rootCtx context.Context, limit int, board *activity.Board) *taskRegistry {
	if limit <= 0 {
		limit = 4
	}
	return &taskRegistry{limit: limit, rootCtx: rootCtx, tasks: map[string]*bgTask{}, board: board}
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
			line := fmt.Sprintf("%s running (%s, %s)", t.ID, t.Spec.CLI, elapsedShort(time.Since(t.Started)))
			if r.board != nil {
				// Match the project-qualified board key, not the bare thread:
				// the board is shared across every project, so two projects each
				// running a "codex" task would otherwise cross-attribute (the
				// Manager keys entries as agent.BoardLabel(projectID, thread)).
				key := agent.BoardLabel(t.Spec.ProjectID, t.Spec.Thread)
				for _, st := range r.board.Snapshot() {
					if st.Label == key && st.Last != "" {
						line += " — " + st.Last
						break
					}
				}
			}
			parts = append(parts, line)
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

// taskLine renders one task for thread_status / collect pending summaries.
func taskLine(t bgTask) string {
	switch t.State {
	case taskRunning:
		return fmt.Sprintf("%s %s (%s, thread %s, %s)", t.ID, t.State, t.Spec.CLI, t.Spec.Thread, elapsedShort(time.Since(t.Started)))
	case taskQueued:
		behind := "at cap"
		if t.QueuedBehind != "" {
			behind = "behind " + t.QueuedBehind
		}
		return fmt.Sprintf("%s queued %s (%s, thread %s, %s)", t.ID, behind, t.Spec.CLI, t.Spec.Thread, elapsedShort(time.Since(t.Created)))
	default:
		claimed := ""
		if !t.Claimed {
			claimed = " unclaimed"
		}
		return fmt.Sprintf("%s %s%s (%s, thread %s)", t.ID, t.State, claimed, t.Spec.CLI, t.Spec.Thread)
	}
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

// persistLocked mirrors task state to disk. No-op when r.dir is unset
// (disabled in unit tests that don't need persistence). Callers hold r.mu.
func (r *taskRegistry) persistLocked(t *bgTask) {
	if r.dir == "" {
		return
	}
	writeTaskFile(r.dir, t)
}

// taskFile is the JSON state mirror of one task. It exists for crash honesty
// — orphan reporting after a dead server — never for resumption: results are
// in-memory only, so an uncollected finish is a reported loss.
type taskFile struct {
	RunID     string    `json:"run_id"`
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Project   string    `json:"project"`
	ProjectID string    `json:"project_id"`
	Thread    string    `json:"thread"`
	CLI       string    `json:"cli"`
	Model     string    `json:"model"`
	Risk      string    `json:"risk"`
	Created   time.Time `json:"created"`
	Started   time.Time `json:"started,omitempty"`
	Finished  time.Time `json:"finished,omitempty"`
	Err       string    `json:"error,omitempty"`
	Claimed   bool      `json:"claimed"`
}

// writeTaskFile mirrors one task to <dir>/<run-id>.json (atomic tmp+rename).
// Best-effort: a mirror failure is narrated, never fails the task.
func writeTaskFile(dir string, t *bgTask) {
	tf := taskFile{
		RunID: t.RunID, ID: t.ID, State: t.State,
		Project: t.Spec.Project, ProjectID: t.Spec.ProjectID, Thread: t.Spec.Thread,
		CLI: t.Spec.CLI, Model: t.Spec.Model, Risk: t.Spec.Risk,
		Created: t.Created, Started: t.Started, Finished: t.Finished,
		Err: t.Err, Claimed: t.Claimed,
	}
	b, err := json.MarshalIndent(tf, "", "  ")
	if err != nil {
		logStatus("task mirror %s: %v", t.ID, err)
		return
	}
	path := filepath.Join(dir, tf.RunID+".json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		logStatus("task mirror %s: %v", t.ID, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		logStatus("task mirror %s: %v", t.ID, err)
	}
}

// adoptOrphanedTaskFiles scans dir at server start: every UNCLAIMED file from
// a previous lifetime — whatever its state — is a loss (queued/running died
// with the server; done/error results lived only in that server's memory).
// Each is flipped to orphaned on disk and returned for adoption. Claimed
// files finished more than maxClaimedAge ago are pruned. Best-effort
// throughout: unreadable files are narrated and skipped.
//
// Deviation from the task brief: the brief's on-disk orphan-flip write
// (MarshalIndent/WriteFile/Rename) swallowed its errors. That violates the
// project's never-swallow-errors invariant, so both failure paths are
// narrated via logStatus below, matching writeTaskFile's convention. A
// MarshalIndent failure skips the write but still returns the in-memory
// orphan — the loss is real and must be reported this session even if the
// on-disk flip couldn't be persisted; the next scan will retry the flip.
func adoptOrphanedTaskFiles(dir string, maxClaimedAge time.Duration) []taskFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			logStatus("task scan: %v", err)
		}
		return nil
	}
	var orphans []taskFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			logStatus("task scan %s: %v", e.Name(), err)
			continue
		}
		var tf taskFile
		if err := json.Unmarshal(b, &tf); err != nil {
			logStatus("task scan %s: %v", e.Name(), err)
			continue
		}
		if tf.Claimed {
			if !tf.Finished.IsZero() && time.Since(tf.Finished) > maxClaimedAge {
				if err := os.Remove(path); err != nil {
					logStatus("task prune %s: %v", e.Name(), err)
				}
			}
			continue
		}
		prior := tf.State
		tf.State = taskOrphaned
		if tf.Err == "" || prior == taskQueued || prior == taskRunning || prior == taskDone {
			tf.Err = fmt.Sprintf("lost when styx mcp exited (state was %q); no result — re-dispatch if still needed", prior)
		}
		nb, err := json.MarshalIndent(tf, "", "  ")
		if err != nil {
			logStatus("task orphan-flip %s: %v", e.Name(), err)
		} else {
			tmp := path + ".tmp"
			if err := os.WriteFile(tmp, nb, 0o644); err != nil {
				logStatus("task orphan-flip %s: %v", e.Name(), err)
			} else if err := os.Rename(tmp, path); err != nil {
				logStatus("task orphan-flip %s: %v", e.Name(), err)
			}
		}
		orphans = append(orphans, tf)
	}
	return orphans
}

// adoptOrphans inserts prior-lifetime orphans as o1, o2, … entries so
// collect and the piggyback line report them. Claiming persists to their
// original run-id file.
func (r *taskRegistry) adoptOrphans(files []taskFile) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, tf := range files {
		id := fmt.Sprintf("o%d", i+1)
		t := &bgTask{
			ID: id, RunID: tf.RunID,
			Spec: taskSpec{Project: tf.Project, ProjectID: tf.ProjectID, Thread: tf.Thread,
				CLI: tf.CLI, Model: tf.Model, Risk: tf.Risk},
			State: taskOrphaned, Created: tf.Created, Started: tf.Started,
			Finished: tf.Finished, Err: tf.Err,
		}
		r.tasks[id] = t
		r.order = append(r.order, id)
	}
}

// withBackgroundStatus is the piggyback decoration point (spec §Piggyback):
// whenever the registry holds live or unclaimed tasks, every conductor tool's
// map-shaped result gains a compact "bg" status line, so any tool activity
// resurfaces background work — the conductor cannot forget for long. Non-map
// results (shipgate token relays) and errors pass through untouched; the
// JSON-RPC transport is untouched.
func withBackgroundStatus(tools []mcpserver.Tool, reg *taskRegistry) []mcpserver.Tool {
	out := make([]mcpserver.Tool, len(tools))
	for i, tl := range tools {
		inner := tl.Handler
		tl.Handler = func(ctx context.Context, raw json.RawMessage) (any, error) {
			res, err := inner(ctx, raw)
			if err != nil {
				return res, err
			}
			if line := reg.StatusLine(); line != "" {
				if m, ok := res.(map[string]any); ok {
					m["bg"] = line
				}
			}
			return res, nil
		}
		out[i] = tl
	}
	return out
}
