package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Thread is a named, durable conversation with one agent, independent of any
// OS process. Conversation state lives in the CLI's session store (resume-
// capable CLIs) or in Summary (styx-maintained continuity).
type Thread struct {
	Name             string    `json:"name"`
	CLI              string    `json:"cli"`
	SessionID        string    `json:"session_id,omitempty"`
	Summary          string    `json:"summary,omitempty"`           // rolling summary for non-resume CLIs
	LastDistillation string    `json:"last_distillation,omitempty"` // checkpoint for restart/recovery
	ContextTokens    int       `json:"context_tokens"`
	Turns            int       `json:"turns"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ThreadStore persists one project's threads as JSON.
type ThreadStore struct {
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
	if th, ok := ts.Threads[name]; ok {
		return th
	}
	th := &Thread{Name: name, CLI: cli, UpdatedAt: time.Now()}
	ts.Threads[name] = th
	return th
}

// Save writes the store atomically (tmp + rename).
func (ts *ThreadStore) Save() error {
	if err := paths.EnsureDir(filepath.Dir(ts.path)); err != nil {
		return fmt.Errorf("ensure threads dir: %w", err)
	}
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
