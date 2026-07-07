package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Thread is a named, durable conversation with one agent, independent of any
// OS process. Conversation state lives in the CLI's session store (resume-
// capable CLIs) or in Summary (styx-maintained continuity).
//
// mu guards the mutable fields below: background dispatch (async B1, see
// docs/superpowers/specs/2026-07-07-styx-async-dispatch-design.md) runs a
// thread's turn on its own goroutine while a concurrent thread_status call
// (Manager.StatusLines) can read the same *Thread at the same time — every
// access outside of construction must hold mu.
type Thread struct {
	mu               sync.Mutex
	Name             string    `json:"name"`
	CLI              string    `json:"cli"`
	SessionID        string    `json:"session_id,omitempty"`
	Summary          string    `json:"summary,omitempty"`           // rolling summary for non-resume CLIs
	LastDistillation string    `json:"last_distillation,omitempty"` // checkpoint for restart/recovery
	ContextTokens    int       `json:"context_tokens"`
	Turns            int       `json:"turns"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// MarshalJSON locks before encoding so ThreadStore.Save (which may run
// concurrently with a background dispatch's in-place field mutations) never
// reads a torn Thread.
func (t *Thread) MarshalJSON() ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	type alias Thread
	return json.Marshal((*alias)(t))
}

// ThreadStore persists one project's threads as JSON.
type ThreadStore struct {
	mu      sync.Mutex // guards the Threads map structure; see Thread.mu for per-thread field guards
	path    string
	Threads map[string]*Thread
}

// LoadThreads opens the thread store for a project by name.
func LoadThreads(projectName string) (*ThreadStore, error) {
	dir, err := paths.ThreadsDir()
	if err != nil {
		return nil, err
	}
	return LoadThreadsFrom(filepath.Join(dir, projectName+".json"))
}

// LoadThreadsFrom opens a thread store at an explicit path (tests).
func LoadThreadsFrom(path string) (*ThreadStore, error) {
	ts := &ThreadStore{path: path, Threads: map[string]*Thread{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return ts, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read threads %s: %w", path, err)
	}
	if err := json.Unmarshal(b, &ts.Threads); err != nil {
		return nil, fmt.Errorf("parse threads %s: %w", path, err)
	}
	return ts, nil
}

// Get returns the named thread, creating it (lazily, unsaved) if missing.
func (ts *ThreadStore) Get(name, cli string) *Thread {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if th, ok := ts.Threads[name]; ok {
		return th
	}
	th := &Thread{Name: name, CLI: cli, UpdatedAt: time.Now()}
	ts.Threads[name] = th
	return th
}

// Save writes the store atomically (tmp + rename). ts.mu is held across the
// entire marshal+write+rename: background dispatch (B1) can run two
// goroutines on different threads of the same project concurrently (Busy
// only guards same-thread reentrancy), and both call Save() on this same
// cached *ThreadStore. tmp is a fixed path (ts.path + ".tmp"), so two
// interleaved WriteFile/Rename pairs on it would corrupt the tmp file or
// race the final rename. Serializing the whole body here fixes that: writes
// to disk happen one at a time. This does not create new lock nesting — the
// established order is ThreadStore.mu -> Thread.mu (MarshalIndent invokes
// each Thread.MarshalJSON, which takes th.mu), and os.WriteFile/os.Rename
// take no other lock, so holding ts.mu across them is safe. No caller of
// Save holds a Thread.mu when calling it (see manager.go).
func (ts *ThreadStore) Save() error {
	if err := paths.EnsureDir(filepath.Dir(ts.path)); err != nil {
		return fmt.Errorf("ensure threads dir: %w", err)
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	b, err := json.MarshalIndent(ts.Threads, "", "  ")
	if err != nil {
		return fmt.Errorf("encode threads: %w", err)
	}
	tmp := ts.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write threads tmp: %w", err)
	}
	if err := os.Rename(tmp, ts.path); err != nil {
		return fmt.Errorf("rename threads file: %w", err)
	}
	return nil
}
