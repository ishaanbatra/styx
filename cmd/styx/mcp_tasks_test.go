package main

import (
	"context"
	"strings"
	"testing"
	"time"
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
	r := newTaskRegistry(context.Background(), 1)
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

func TestRegistryThreadSerialization(t *testing.T) {
	r := newTaskRegistry(context.Background(), 4)
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
	r := newTaskRegistry(context.Background(), 4)
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
	r := newTaskRegistry(context.Background(), 4)
	id, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "read"},
		func(context.Context, string) (map[string]any, error) {
			return nil, context.DeadlineExceeded
		})
	waitFor(t, "error state", func() bool { return state(r, id) == taskError })
	tk, ok := r.Get(id)
	if !ok || tk.Err == "" {
		t.Fatalf("failed task must carry its error text, got %+v", tk)
	}
	if line := r.StatusLine(); !strings.Contains(line, id) || !strings.Contains(line, "unclaimed") {
		t.Fatalf("unclaimed error must appear in the status line, got %q", line)
	}
	r.Claim(id)
	if line := r.StatusLine(); line != "" {
		t.Fatalf("claimed task must leave the status line, got %q", line)
	}
}

func TestRegistryBusyAndNilSafety(t *testing.T) {
	var nilReg *taskRegistry
	if _, busy := nilReg.Busy("p1", "codex", "edit"); busy {
		t.Fatal("nil registry must report not busy")
	}
	if nilReg.StatusLine() != "" || nilReg.Snapshot() != nil {
		t.Fatal("nil registry reads must be safe no-ops")
	}

	r := newTaskRegistry(context.Background(), 4)
	run1, started1, release1 := blockingRun(nil)
	id1, _ := r.Spawn(taskSpec{ProjectID: "p1", Thread: "codex", CLI: "codex", Risk: "edit"}, run1)
	<-started1
	if blocker, busy := r.Busy("p1", "codex", "read"); !busy || blocker != id1 {
		t.Fatalf("same thread must be busy, got %q %v", blocker, busy)
	}
	if blocker, busy := r.Busy("p1", "other", "edit"); !busy || blocker != id1 {
		t.Fatalf("edit against a running edit on the project must be busy, got %q %v", blocker, busy)
	}
	if _, busy := r.Busy("p1", "other", "read"); busy {
		t.Fatal("read on another thread must not be busy")
	}
	if _, busy := r.Busy("p2", "codex", "edit"); busy {
		t.Fatal("another project must not be busy")
	}
	close(release1)
}
