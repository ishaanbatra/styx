package agent

import (
	"path/filepath"
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
