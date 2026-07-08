package activity

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// mirrorFile is the on-disk shape a second process reads (styx watch).
// Timestamps survive the round trip so the reader computes idle/stall
// against its own clock, in its own process.
type mirrorFile struct {
	Note   string        `json:"note"`
	States []mirrorState `json:"states"`
}

type mirrorState struct {
	Label   string        `json:"label"`
	Last    string        `json:"last"`
	LastAt  time.Time     `json:"last_at"`
	Done    bool          `json:"done"`
	Elapsed time.Duration `json:"elapsed"`
}

// WriteMirror writes the board snapshot to path atomically (tmp + rename),
// so a concurrent reader (styx watch) never observes a partial file.
func WriteMirror(path string, states []AgentState, note string) error {
	mf := mirrorFile{Note: note}
	for _, s := range states {
		mf.States = append(mf.States, mirrorState{
			Label: s.Label, Last: s.Last, LastAt: s.LastAt, Done: s.Done, Elapsed: s.Elapsed,
		})
	}
	b, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal mirror: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write mirror tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename mirror: %w", err)
	}
	return nil
}

// ReadMirror loads a board snapshot written by WriteMirror. When path does not
// exist, the returned error wraps the underlying os.ReadFile error so callers
// can test for absence with errors.Is(err, fs.ErrNotExist).
func ReadMirror(path string) ([]AgentState, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("read mirror: %w", err)
	}
	var mf mirrorFile
	if err := json.Unmarshal(b, &mf); err != nil {
		return nil, "", fmt.Errorf("parse mirror: %w", err)
	}
	out := make([]AgentState, 0, len(mf.States))
	for _, s := range mf.States {
		out = append(out, AgentState{
			Label: s.Label, Last: s.Last, LastAt: s.LastAt, Done: s.Done, Elapsed: s.Elapsed,
		})
	}
	return out, mf.Note, nil
}

// MirrorThrottle returns a debounced writer: calling it mirrors b to path at
// most once per min interval (leading-edge — the first call always writes).
// Write failures are returned to the caller to narrate, never swallowed. The
// returned func is safe for concurrent use.
func MirrorThrottle(b *Board, path string, min time.Duration) func() error {
	var mu sync.Mutex
	var last time.Time
	return func() error {
		mu.Lock()
		defer mu.Unlock()
		now := time.Now()
		if !last.IsZero() && now.Sub(last) < min {
			return nil
		}
		last = now
		return WriteMirror(path, b.Snapshot(), b.WatcherNote())
	}
}
