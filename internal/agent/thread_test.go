package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestThreadStoreRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "proj.json")
	ts, err := LoadThreadsFrom(p)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	th := ts.Get("claude", "claude")
	if th == nil || th.CLI != "claude" {
		t.Fatalf("Get created %+v", th)
	}
	th.SessionID = "sess-9"
	th.ContextTokens = 12345
	th.Turns = 3
	th.LastDistillation = "we decided X"
	if err := ts.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	ts2, err := LoadThreadsFrom(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := ts2.Get("claude", "claude")
	if got.SessionID != "sess-9" || got.ContextTokens != 12345 || got.Turns != 3 || got.LastDistillation != "we decided X" {
		t.Errorf("round-trip = %+v", got)
	}
	// Get is idempotent: same pointer for same name.
	if ts2.Get("claude", "claude") != got {
		t.Error("Get returned a different instance for the same name")
	}
}

// TestThreadStoreConcurrentSave guards against a regression where Save only
// held ts.mu around the JSON marshal and released it before the
// WriteFile+Rename to a fixed tmp path. B1 background dispatch lets two
// goroutines Save() the same cached *ThreadStore concurrently (different
// threads of one project; Busy only guards same-thread reentrancy), and
// interleaved WriteFile/Rename pairs on the same tmp path could corrupt the
// tmp file or fail one goroutine's Rename. This is a file-level race, not a
// memory race, so -race alone would not catch it; the assertion that
// matters is a valid, non-corrupt persisted file after the dust settles.
func TestThreadStoreConcurrentSave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "proj.json")
	ts, err := LoadThreadsFrom(p)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	th := ts.Get("claude", "claude")
	th.SessionID = "sess-1"
	th.Turns = 1

	const n = 8
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- ts.Save()
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Errorf("concurrent Save: %v", err)
		}
	}

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	var reloaded map[string]*Thread
	if err := json.Unmarshal(b, &reloaded); err != nil {
		t.Fatalf("persisted file is not valid JSON: %v\ncontents: %s", err, b)
	}
	got, ok := reloaded["claude"]
	if !ok || got.SessionID != "sess-1" || got.Turns != 1 {
		t.Errorf("persisted thread = %+v (ok=%v)", got, ok)
	}
}
