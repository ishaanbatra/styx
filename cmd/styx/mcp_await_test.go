package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/activity"
	"github.com/ishaanbatra/styx/internal/agent"
)

// fastAwait shrinks the awaiter tick for tests. Not parallel-safe: tests
// using it must not call t.Parallel().
func fastAwait(t *testing.T) {
	t.Helper()
	old := awaitTick
	awaitTick = 5 * time.Millisecond
	t.Cleanup(func() { awaitTick = old })
}

func TestTaskHeartbeat(t *testing.T) {
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	label := agent.BoardLabel("p1", "claude")
	for _, tc := range []struct {
		name   string
		tk     bgTask
		states map[string]activity.AgentState
		want   string
	}{
		{"queued behind", bgTask{ID: "t2", State: taskQueued, QueuedBehind: "t1",
			Spec: taskSpec{CLI: "claude"}, Created: now.Add(-12 * time.Second)},
			nil, "t2 claude queued behind t1 (12s)"},
		{"queued at cap", bgTask{ID: "t3", State: taskQueued,
			Spec: taskSpec{CLI: "codex"}, Created: now.Add(-3 * time.Second)},
			nil, "t3 codex queued at cap (3s)"},
		{"running with fresh board entry", bgTask{ID: "t1", State: taskRunning,
			Spec: taskSpec{ProjectID: "p1", Thread: "claude", CLI: "claude"}},
			map[string]activity.AgentState{label: {Label: label, Last: "Edit: internal/router/router.go", LastAt: now.Add(-4 * time.Second)}},
			"t1 claude ▸ Edit: internal/router/router.go (4s)"},
		{"running stalled", bgTask{ID: "t1", State: taskRunning,
			Spec: taskSpec{ProjectID: "p1", Thread: "claude", CLI: "claude"}},
			map[string]activity.AgentState{label: {Label: label, Last: "go test ./...", LastAt: now.Add(-96 * time.Second)}},
			"t1 claude ⚠ idle 1m36s (last: go test ./...)"},
		{"running without board entry", bgTask{ID: "t1", State: taskRunning,
			Spec: taskSpec{ProjectID: "p1", Thread: "claude", CLI: "claude"}, Started: now.Add(-7 * time.Second)},
			nil, "t1 claude ▸ running (7s)"},
		{"terminal", bgTask{ID: "t1", State: taskDone, Spec: taskSpec{CLI: "claude"}},
			nil, "t1 claude ✓ done"},
		{"terminal error", bgTask{ID: "t1", State: taskError, Spec: taskSpec{CLI: "claude"}},
			nil, "t1 claude ✗ error"},
		{"terminal orphaned", bgTask{ID: "t1", State: taskOrphaned, Spec: taskSpec{CLI: "claude"}},
			nil, "t1 claude ✗ orphaned"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskHeartbeat(tc.tk, tc.states, now); got != tc.want {
				t.Fatalf("heartbeat = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAwaitTasksCollectsAndClaims(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg, board: activity.NewBoard()}
	run1, started1, release1 := blockingRun(map[string]any{"text": "one", "cli": "claude"})
	run2, started2, release2 := blockingRun(map[string]any{"text": "two", "cli": "codex"})
	id1, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "a", CLI: "claude", Risk: "read"}, run1)
	id2, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "b", CLI: "codex", Risk: "read"}, run2)
	<-started1
	<-started2

	var mu sync.Mutex
	var lines []string
	notify := func(_ float64, msg string) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	}
	outCh := make(chan awaitOutcome, 1)
	go func() { outCh <- d.awaitTasks(context.Background(), []string{id1, id2}, notify) }()

	waitFor(t, "progress emitted", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) > 0 && strings.Contains(lines[0], "0/2 done")
	})
	close(release1)
	close(release2)
	out := <-outCh
	if out.Detached {
		t.Fatal("completed await must not report detached")
	}
	if len(out.Results) != 2 || out.Results[0]["text"] != "one" || out.Results[1]["text"] != "two" {
		t.Fatalf("results mismatch: %v", out.Results)
	}
	for _, id := range []string{id1, id2} {
		if tk, _ := reg.Get(id); !tk.Claimed {
			t.Fatalf("awaited task %s must be claimed (results were delivered inline)", id)
		}
	}
}

func TestAwaitTasksDetachOnCancel(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg, board: activity.NewBoard()}
	run1, started1, release1 := blockingRun(map[string]any{"text": "late"})
	id1, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "a", CLI: "claude", Risk: "read"}, run1)
	<-started1
	defer close(release1)

	ctx, cancel := context.WithCancel(context.Background())
	outCh := make(chan awaitOutcome, 1)
	go func() { outCh <- d.awaitTasks(ctx, []string{id1}, nil) }()
	cancel()
	out := <-outCh
	if !out.Detached || out.Results != nil {
		t.Fatalf("cancelled await must detach with no results, got %+v", out)
	}
	tk, _ := reg.Get(id1)
	if tk.Claimed || tk.State != taskRunning {
		t.Fatalf("detached task must keep running unclaimed, got claimed=%v state=%s", tk.Claimed, tk.State)
	}
}

func TestAwaitTasksAnnouncesUnrelatedCompletions(t *testing.T) {
	fastAwait(t)
	reg := newTaskRegistry(context.Background(), 4, nil)
	d := &conductorDeps{reg: reg, board: activity.NewBoard()}
	// Unrelated task, already terminal BEFORE the await starts: never announced.
	runOld, startedOld, releaseOld := blockingRun(map[string]any{"text": "old"})
	idOld, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "old", CLI: "codex", Risk: "read"}, runOld)
	<-startedOld
	close(releaseOld)
	waitFor(t, "old task done", func() bool { return state(reg, idOld) == taskDone })

	runAw, startedAw, releaseAw := blockingRun(map[string]any{"text": "aw"})
	idAw, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "aw", CLI: "claude", Risk: "read"}, runAw)
	runBg, startedBg, releaseBg := blockingRun(map[string]any{"text": "bg"})
	idBg, _ := reg.Spawn(taskSpec{ProjectID: "p1", Thread: "bg", CLI: "codex", Risk: "read"}, runBg)
	<-startedAw
	<-startedBg

	var mu sync.Mutex
	var lines []string
	notify := func(_ float64, msg string) {
		mu.Lock()
		lines = append(lines, msg)
		mu.Unlock()
	}
	outCh := make(chan awaitOutcome, 1)
	go func() { outCh <- d.awaitTasks(context.Background(), []string{idAw}, notify) }()

	// Synchronize on the observer's first progress line before releasing the
	// unrelated task: awaitTasks seeds its "already terminal" set once, at
	// the top of the call, from a single goroutine launched by `go func`
	// above with no ordering guarantee against this goroutine. Without this
	// wait, close(releaseBg) below races that seed step — if idBg finishes
	// and flips to taskDone in the registry before awaitTasks's goroutine
	// even runs its first statement, the seed loop wrongly treats it as
	// pre-existing (not a "mid-await" completion) and the notice this test
	// asserts on never fires. This waitFor (same primitive already used
	// above for the "progress emitted" sync) pins the ordering the test's
	// name promises: "mid-await", i.e. strictly after observation begins.
	waitFor(t, "await loop observing", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(lines) > 0
	})
	close(releaseBg) // unrelated completion mid-await
	waitFor(t, "unrelated completion announced", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, l := range lines {
			if strings.Contains(l, idBg+" done — collect") {
				return true
			}
		}
		return false
	})
	close(releaseAw)
	<-outCh

	mu.Lock()
	defer mu.Unlock()
	announced := 0
	for _, l := range lines {
		if strings.Contains(l, idBg+" done — collect") {
			announced++
		}
		if strings.Contains(l, idOld) {
			t.Fatalf("pre-terminal task %s must never be announced: %q", idOld, l)
		}
	}
	if announced != 1 {
		t.Fatalf("unrelated completion must be announced exactly once, got %d in %q", announced, lines)
	}
	if tk, _ := reg.Get(idBg); tk.Claimed {
		t.Fatal("announcing an unrelated completion must not claim it")
	}
}
