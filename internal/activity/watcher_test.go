package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWatcherPollWritesNote(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": "both agents healthy, no stalls"},
		})
	}))
	defer srv.Close()

	b := NewBoard()
	b.Record("claude", "Bash: go test")
	w := &Watcher{BaseURL: srv.URL, Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if b.WatcherNote() != "both agents healthy, no stalls" {
		t.Fatalf("note = %q", b.WatcherNote())
	}
}

func TestWatcherDegradesWhenOllamaDown(t *testing.T) {
	b := NewBoard()
	b.Record("claude", "Bash: go test")
	w := &Watcher{BaseURL: "http://127.0.0.1:1", Model: "test", Board: b} // nothing listening
	if err := w.pollOnce(context.Background()); err == nil {
		t.Fatalf("expected error when ollama unreachable")
	}
	if b.WatcherNote() != "" {
		t.Fatalf("note should stay empty on failure, got %q", b.WatcherNote())
	}
}

func TestWatcherNoAgentsNoCall(t *testing.T) {
	b := NewBoard()
	w := &Watcher{BaseURL: "http://127.0.0.1:1", Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatalf("empty board should be a no-op, got %v", err)
	}
}

func TestWatcherRunStopsOnContext(t *testing.T) {
	b := NewBoard()
	w := &Watcher{BaseURL: "http://127.0.0.1:1", Model: "test", Board: b, Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}
