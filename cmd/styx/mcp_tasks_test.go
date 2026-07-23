package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/mcpserver"
	"github.com/ishaanbatra/styx/internal/memguard"
)

// blockingRun returns a run func that reports it started and blocks until
// released — the test controls task lifetimes without sleeps.
func blockingRun(result map[string]any) (run func(context.Context, string) (map[string]any, error), started chan struct{}, release chan struct{}) {
	started = make(chan struct{})
	release = make(chan struct{})
	run = func(context.Context, string) (map[string]any, error) {
		close(started)
		<-release
		return result, nil
	}
	return run, started, release
}

// waitFor polls cond up to 2s — registry completions happen on goroutines.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func state(r *taskRegistry, id string) string {
	tk, _ := r.Get(id)
	return tk.State
}

func TestRegistryCapQueuesExcessTasks(t *testing.T) {
	r := newTaskRegistry(context.Background(), 1, nil)
	run1, started1, release1 := blockingRun(map[string]any{"text": "one"})
	run2, started2, release2 := blockingRun(map[string]any{"text": "two"})

	id1, st1 := r.Spawn(taskSpec{ProjectID: "p1", Thread: "a", CLI: "codex", Risk: "read"}, run1)
	if st1 != taskRunning {
		t.Fatalf("first task must run immediately, got %q", st1)
	}
	<-started1
	id2, st2 := r.Spawn(taskSpec{ProjectID: "p2", Thread: "b", CLI: "codex", Risk: "read"}, run2)
	if st2 != taskQueued {
		t.Fatalf("over-cap task must queue, got %q", st2)
	}
	close(release1)
	waitFor(t, "t1 done", func() bool { return state(r, id1) == taskDone })
	waitFor(t, "t2 promoted", func() bool { return state(r, id2) == taskRunning })
	<-started2
	close(release2)
	waitFor(t, "t2 done", func() bool { return state(r, id2) == taskDone })
	tk, _ := r.Get(id2)
	if tk.Result["text"] != "two" {
		t.Fatalf("result must be captured, got %v", tk.Result)
	}
}

func TestRegistryMemoryPressureGateRechecksOnSpawn(t *testing.T) {
	level := memguard.Critical
	r := newTaskRegistry(context.Background(), 2, nil, func() memguard.Level { return level })
	run1, started1, release1 := blockingRun(nil)

	id1, got := r.Spawn(taskSpec{ProjectID: "p1", Thread: "one", CLI: "codex", Risk: "read"}, run1)
	if got != taskQueued {
		t.Fatalf("task at critical pressure = %q, want queued", got)
	}
	select {
	case <-started1:
		t.Fatal("critical pressure must not execute a queued task")
	default:
	}

	level = memguard.Normal
	run2, started2, release2 := blockingRun(nil)
	id2, got := r.Spawn(taskSpec{ProjectID: "p2", Thread: "two", CLI: "claude", Risk: "read"}, run2)
	if got != taskRunning {
		t.Fatalf("spawn after recovery = %q, want running", got)
	}
	waitFor(t, "original queued task promoted after recovery spawn", func() bool {
		return state(r, id1) == taskRunning
	})
	<-started1
	<-started2
	close(release1)
	close(release2)
	waitFor(t, "recovered tasks done", func() bool {
		return state(r, id1) == taskDone && state(r, id2) == taskDone
	})
}

func TestRegistryMemoryPressureGateRechecksOnCompletion(t *testing.T) {
	level := memguard.Normal
	r := newTaskRegistry(context.Background(), 1, nil, func() memguard.Level { return level })
	run1, started1, release1 := blockingRun(nil)
	id1, got := r.Spawn(taskSpec{ProjectID: "p1", Thread: "one", CLI: "codex", Risk: "read"}, run1)
	if got != taskRunning {
		t.Fatalf("first task = %q, want running", got)
	}
	<-started1

	level = memguard.Critical
	run2, started2, release2 := blockingRun(nil)
	id2, got := r.Spawn(taskSpec{ProjectID: "p2", Thread: "two", CLI: "claude", Risk: "read"}, run2)
	if got != taskQueued {
		t.Fatalf("task spawned at critical pressure = %q, want queued", got)
	}
	select {
	case <-started2:
		t.Fatal("critical pressure must not execute the queued task")
	default:
	}

	level = memguard.Normal
	close(release1)
	waitFor(t, "first task completion", func() bool { return state(r, id1) == taskDone })
	waitFor(t, "queued task promoted after recovery completion", func() bool {
		return state(r, id2) == taskRunning
	})
	<-started2
	close(release2)
	waitFor(t, "second task completion", func() bool { return state(r, id2) == taskDone })
}

func TestRegistryThreadSerialization(t *testing.T) {
	r := newTaskRegistry(context.Background(), 4, nil)
	run1, started1, release1 := blockingRun(nil)
	run2, _, release2 := blockingRun(nil)
	id1, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"}, run1)
	<-started1
	id2, st2 := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"}, run2)
	if st2 != taskQueued {
		t.Fatalf("same project+thread must serialize, got %q", st2)
	}
	if tk, _ := r.Get(id2); tk.QueuedBehind != id1 {
		t.Fatalf("queued task must name its blocker, got %q", tk.QueuedBehind)
	}
	// A different thread on the same project (read risk) runs freely.
	run3, started3, release3 := blockingRun(nil)
	_, st3 := r.Spawn(taskSpec{ProjectID: "p1", Thread: "other", CLI: "claude", Risk: "read"}, run3)
	if st3 != taskRunning {
		t.Fatalf("read-risk tasks on distinct threads must run in parallel, got %q", st3)
	}
	<-started3
	close(release1)
	waitFor(t, "t2 promoted", func() bool { return state(r, id2) == taskRunning })
	close(release2)
	close(release3)
}

func TestRegistryProjectWriteQueue(t *testing.T) {
	r := newTaskRegistry(context.Background(), 4, nil)
	run1, started1, release1 := blockingRun(nil)
	id1, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "edit"}, run1)
	<-started1
	// Second edit-risk task on the same project queues behind the first,
	// even on a different thread.
	run2, _, release2 := blockingRun(nil)
	id2, st2 := r.Spawn(taskSpec{ProjectID: "p1", Thread: "claude", CLI: "claude", Risk: "edit"}, run2)
	if st2 != taskQueued {
		t.Fatalf("second edit-risk task on one project must queue, got %q", st2)
	}
	if tk, _ := r.Get(id2); tk.QueuedBehind != id1 {
		t.Fatalf("write-queued task must show queued behind %s, got %q", id1, tk.QueuedBehind)
	}
	// Edit-risk on a DIFFERENT project runs.
	run3, started3, release3 := blockingRun(nil)
	_, st3 := r.Spawn(taskSpec{ProjectID: "p2", Thread: "codex", CLI: "codex", Risk: "edit"}, run3)
	if st3 != taskRunning {
		t.Fatalf("edit-risk on another project must run, got %q", st3)
	}
	<-started3
	close(release1)
	waitFor(t, "write queue drains", func() bool { return state(r, id2) == taskRunning })
	close(release2)
	close(release3)
}

func TestRegistryErrorsCollectAndClaim(t *testing.T) {
	r := newTaskRegistry(context.Background(), 4, nil)
	id, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"},
		func(context.Context, string) (map[string]any, error) {
			return nil, context.DeadlineExceeded
		})
	waitFor(t, "error state", func() bool { return state(r, id) == taskError })
	tk, ok := r.Get(id)
	if !ok || tk.Err == "" {
		t.Fatalf("failed task must carry its error text, got %+v", tk)
	}
	if line := r.doneStatusLine(); !strings.Contains(line, "DONE: "+id+" error") {
		t.Fatalf("unclaimed error must appear in the done line, got %q", line)
	}
	r.Claim(id)
	if line := r.doneStatusLine(); line != "" {
		t.Fatalf("claimed task must leave the done line, got %q", line)
	}
}

func TestTaskStateMirrorAndOrphanScan(t *testing.T) {
	dir := t.TempDir()
	r := newTaskRegistry(context.Background(), 4, nil)
	r.dir = dir
	run1, started1, release1 := blockingRun(map[string]any{"text": "hi"})
	id, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "edit"}, run1)
	<-started1

	// Spawn mirrored the running task to disk.
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("want 1 state file after spawn, got %d", len(files))
	}
	var tf taskFile
	b, _ := os.ReadFile(files[0])
	if err := json.Unmarshal(b, &tf); err != nil {
		t.Fatalf("state file must be JSON: %v", err)
	}
	if tf.State != taskRunning || tf.ID != id || tf.CLI != "codex" {
		t.Fatalf("state file mismatch: %+v", tf)
	}

	close(release1)
	waitFor(t, "done mirrored", func() bool {
		b, _ := os.ReadFile(files[0])
		json.Unmarshal(b, &tf)
		return tf.State == taskDone
	})

	// A NEW server adopting this dir treats the unclaimed done file as an
	// orphan (results are in-memory only — losses are loud, never silent).
	orphans := adoptOrphanedTaskFiles(dir, 7*24*time.Hour)
	if len(orphans) != 1 {
		t.Fatalf("want 1 orphan, got %d", len(orphans))
	}
	r2 := newTaskRegistry(context.Background(), 4, nil)
	r2.dir = dir
	r2.adoptOrphans(orphans)
	snap := r2.Snapshot()
	if len(snap) != 1 || snap[0].State != taskOrphaned || snap[0].ID != "o1" {
		t.Fatalf("adopted orphan mismatch: %+v", snap)
	}
	if !strings.Contains(snap[0].Err, "styx mcp exited") {
		t.Fatalf("orphan must explain the loss, got %q", snap[0].Err)
	}
	if line := r2.doneStatusLine(); !strings.Contains(line, "DONE: o1 orphaned") {
		t.Fatalf("orphans must be reported in the done line, got %q", line)
	}

	// The on-disk file was flipped to orphaned; a third scan adopts it again
	// (still unclaimed), and claiming persists.
	if again := adoptOrphanedTaskFiles(dir, 7*24*time.Hour); len(again) != 1 || again[0].State != taskOrphaned {
		t.Fatalf("unclaimed orphan file must keep resurfacing, got %+v", again)
	}
	r2.Claim("o1")
	if left := adoptOrphanedTaskFiles(dir, 7*24*time.Hour); len(left) != 0 {
		t.Fatalf("claimed orphan must not resurface, got %+v", left)
	}
}

func TestLiveVsDoneStatusLine(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name          string
		tasks         []*bgTask
		wantLiveParts []string
		wantDone      string
	}{
		{name: "empty"},
		{name: "running and queued", tasks: []*bgTask{
			{ID: "t1", State: taskRunning, Started: now, Spec: taskSpec{CLI: "codex", Thread: "impl"}},
			{ID: "t2", State: taskQueued, Created: now, QueuedBehind: "t1", Spec: taskSpec{CLI: "claude", Thread: "review"}},
		}, wantLiveParts: []string{"t1 running", "t2 queued behind t1"}, wantDone: ""},
		{name: "done", tasks: []*bgTask{
			{ID: "t3", State: taskDone, Spec: taskSpec{CLI: "codex", Thread: "windows-impl"}},
		}, wantDone: "DONE: t3 (codex, thread windows-impl) — call collect"},
		{name: "error and claimed", tasks: []*bgTask{
			{ID: "t4", State: taskError, Spec: taskSpec{CLI: "claude", Thread: "review"}},
			{ID: "t5", State: taskDone, Claimed: true, Spec: taskSpec{CLI: "codex", Thread: "claimed"}},
		}, wantDone: "DONE: t4 error (claude, thread review) — call collect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newTaskRegistry(context.Background(), 4, nil)
			for _, tk := range tc.tasks {
				r.tasks[tk.ID] = tk
				r.order = append(r.order, tk.ID)
			}
			live, done := r.liveStatusLine(), r.doneStatusLine()
			if len(tc.wantLiveParts) == 0 && live != "" {
				t.Fatalf("liveStatusLine() = %q, want empty", live)
			}
			for _, want := range tc.wantLiveParts {
				if !strings.Contains(live, want) {
					t.Fatalf("liveStatusLine() = %q, want containing %q", live, want)
				}
			}
			if done != tc.wantDone {
				t.Fatalf("doneStatusLine() = %q, want %q", done, tc.wantDone)
			}
		})
	}
}

func TestWithBackgroundStatusResultShapes(t *testing.T) {
	type objectResult struct {
		OK bool `json:"ok"`
	}
	reg := newTaskRegistry(context.Background(), 4, nil)
	reg.tasks["t1"] = &bgTask{ID: "t1", State: taskRunning, Started: time.Now(), Spec: taskSpec{CLI: "codex", Thread: "impl"}}
	reg.tasks["t2"] = &bgTask{ID: "t2", State: taskDone, Spec: taskSpec{CLI: "claude", Thread: "review"}}
	reg.order = []string{"t1", "t2"}

	for _, tc := range []struct {
		name             string
		result           any
		err              error
		wantObject       bool
		wantExistingDone bool
	}{
		{name: "map", result: map[string]any{"ok": true}, wantObject: true},
		{name: "map preserves existing done", result: map[string]any{"done": true}, wantObject: true, wantExistingDone: true},
		{name: "struct object", result: objectResult{OK: true}, wantObject: true},
		{name: "slice", result: []string{"one"}},
		{name: "error", err: errors.New("boom")},
		{name: "nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := withBackgroundStatus([]mcpserver.Tool{{
				Name: "test", Handler: func(context.Context, json.RawMessage) (any, error) {
					return tc.result, tc.err
				},
			}}, reg)
			got, err := tools[0].Handler(context.Background(), nil)
			if tc.err != nil {
				if !errors.Is(err, tc.err) || got != tc.result {
					t.Fatalf("error result changed: got=%v err=%v", got, err)
				}
				return
			}
			m, ok := got.(map[string]any)
			if ok != tc.wantObject {
				t.Fatalf("object-shaped = %v, want %v (result %T)", ok, tc.wantObject, got)
			}
			if tc.wantObject {
				if !strings.Contains(m["bg"].(string), "t1 running") {
					t.Fatalf("bg missing live task: %v", m)
				}
				if m["background_done"] != "DONE: t2 (claude, thread review) — call collect" {
					t.Fatalf("background completion notice mismatch: %v", m)
				}
				if tc.wantExistingDone && m["done"] != true {
					t.Fatalf("existing done result was overwritten: %v", m)
				}
			}
		})
	}
}

func collectHandler(t *testing.T, d *conductorDeps) func(context.Context, json.RawMessage) (any, error) {
	t.Helper()
	for _, tool := range conductorTools(d) {
		if tool.Name == "collect" {
			return tool.Handler
		}
	}
	t.Fatal("collect tool not registered")
	return nil
}

func TestCollectWaitTaskID(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg}
	run, started, release := blockingRun(map[string]any{"text": "done"})
	id, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "impl", CLI: "codex", Risk: "read"}, run)
	<-started

	type response struct {
		value any
		err   error
	}
	ch := make(chan response, 1)
	raw, _ := json.Marshal(map[string]any{"task_id": id, "wait": true})
	go func() {
		value, err := collectHandler(t, d)(context.Background(), raw)
		ch <- response{value: value, err: err}
	}()
	close(release)
	got := <-ch
	if got.err != nil {
		t.Fatal(got.err)
	}
	m := got.value.(map[string]any)
	if m["status"] != taskDone || m["text"] != "done" {
		t.Fatalf("wait result = %v", m)
	}
	if tk, _ := reg.Get(id); !tk.Claimed {
		t.Fatal("waited task must be claimed")
	}
}

func TestCollectWaitAllOutstanding(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg}
	run1, started1, release1 := blockingRun(map[string]any{"text": "one"})
	run2, started2, release2 := blockingRun(map[string]any{"text": "two"})
	id1, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "one", CLI: "codex", Risk: "read"}, run1)
	id2, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "two", CLI: "claude", Risk: "read"}, run2)
	<-started1
	<-started2

	raw, _ := json.Marshal(map[string]any{"wait": true})
	// Let the handler take its one-time outstanding-task snapshot before the
	// controlled runs finish; otherwise this becomes a non-waiting sweep test.
	time.AfterFunc(50*time.Millisecond, func() {
		close(release1)
		close(release2)
	})
	value, err := collectHandler(t, d)(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	m := value.(map[string]any)
	results := m["results"].([]map[string]any)
	if len(results) != 2 || results[0]["task_id"] != id1 || results[1]["task_id"] != id2 {
		t.Fatalf("wait-all results = %v", results)
	}
	for _, id := range []string{id1, id2} {
		if tk, _ := reg.Get(id); !tk.Claimed {
			t.Fatalf("task %s not claimed", id)
		}
	}
}

func TestCollectWaitTimeout(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg}
	run, started, release := blockingRun(map[string]any{"text": "late"})
	id, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "impl", CLI: "codex", Risk: "read"}, run)
	<-started

	res, err := callTool(t, d, "collect", map[string]any{"task_id": id, "wait": true, "timeout_s": 1})
	if err != nil {
		t.Fatal(err)
	}
	if res["timed_out"] != true || res["status"] != taskRunning {
		t.Fatalf("timeout result = %v", res)
	}
	if tk, _ := reg.Get(id); tk.Claimed || tk.State != taskRunning {
		t.Fatalf("timed-out task = %+v", tk)
	}
	close(release)
	waitFor(t, "timed-out task completion", func() bool { return state(reg, id) == taskDone })
	collected, err := callTool(t, d, "collect", map[string]any{"task_id": id})
	if err != nil || collected["status"] != taskDone || collected["text"] != "late" {
		t.Fatalf("post-timeout collect = %v, err %v", collected, err)
	}
}

func TestCollectWaitNoOutstanding(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg}
	id, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "done", CLI: "codex", Risk: "read"},
		func(context.Context, string) (map[string]any, error) { return map[string]any{"text": "ready"}, nil })
	waitFor(t, "task done", func() bool { return state(reg, id) == taskDone })
	res, err := callTool(t, d, "collect", map[string]any{"wait": true})
	if err != nil {
		t.Fatal(err)
	}
	results := res["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["task_id"] != id {
		t.Fatalf("immediate results = %v", res)
	}
}

func TestCollectWaitCancel(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg}
	run, started, release := blockingRun(nil)
	id, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "impl", CLI: "codex", Risk: "read"}, run)
	<-started
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	raw, _ := json.Marshal(map[string]any{"task_id": id, "wait": true})
	type response struct {
		value any
		err   error
	}
	ch := make(chan response, 1)
	go func() {
		value, err := collectHandler(t, d)(ctx, raw)
		ch <- response{value: value, err: err}
	}()
	cancel()
	got := <-ch
	if got.err != nil || got.value.(map[string]any)["detached"] != true {
		t.Fatalf("cancel result = %v, err %v", got.value, got.err)
	}
	if tk, _ := reg.Get(id); tk.Claimed || tk.State != taskRunning {
		t.Fatalf("detached task = %+v", tk)
	}
}

func TestCollectNoWaitUnchanged(t *testing.T) {
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg}
	run, started, release := blockingRun(nil)
	id, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "impl", CLI: "codex", Risk: "read"}, run)
	<-started
	defer close(release)
	res, err := callTool(t, d, "collect", map[string]any{"task_id": id})
	if err != nil || res["status"] != taskRunning {
		t.Fatalf("single non-wait = %v, err %v", res, err)
	}
	all, err := callTool(t, d, "collect", map[string]any{})
	if err != nil || len(all["pending"].([]any)) != 1 {
		t.Fatalf("bare non-wait = %v, err %v", all, err)
	}
}

func TestOrphanPruneOldClaimedFiles(t *testing.T) {
	dir := t.TempDir()
	old := taskFile{RunID: "run-old", ID: "t9", State: taskDone, Claimed: true,
		Finished: time.Now().Add(-8 * 24 * time.Hour)}
	b, _ := json.Marshal(old)
	if err := os.WriteFile(filepath.Join(dir, "run-old.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	if orphans := adoptOrphanedTaskFiles(dir, 7*24*time.Hour); len(orphans) != 0 {
		t.Fatalf("claimed files are never orphans, got %+v", orphans)
	}
	if _, err := os.Stat(filepath.Join(dir, "run-old.json")); !os.IsNotExist(err) {
		t.Fatal("claimed file older than 7 days must be pruned")
	}
}
