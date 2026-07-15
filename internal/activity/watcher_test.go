package activity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func loopingBoard() (*Board, time.Time) {
	b := NewBoard()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return now })
	for i := 0; i < loopRun; i++ {
		b.Record("codex/impl", "Bash: go test ./...")
		now = now.Add(10 * time.Second)
	}
	return b, now
}

func TestWatcherSkipsLLMWhenAllHealthy(t *testing.T) {
	b := NewBoard()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return now })
	for _, event := range []string{"Read: a.go", "Edit: a.go", "Bash: go test ./..."} {
		b.Record("codex/impl", event)
		now = now.Add(time.Second)
	}
	var calls atomic.Int32
	w := &Watcher{BaseURL: "http://unused", Model: "test", Board: b, client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, context.Canceled
		}),
	}}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatalf("healthy poll: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("healthy agents triggered %d LLM calls", calls.Load())
	}
}

func TestWatcherClearsStaleAlarm(t *testing.T) {
	b := NewBoard()
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	b.SetClock(func() time.Time { return now })
	b.Record("claude/review", "Read: README.md")
	b.SetWatcherNote("stuck old-agent: stale alarm")
	w := &Watcher{BaseURL: "http://unused", Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.WatcherNote() != "" {
		t.Fatalf("healthy cycle must clear stale alarm, got %q", b.WatcherNote())
	}
}

func TestWatcherPromptOnlySuspicious(t *testing.T) {
	b, now := loopingBoard()
	b.Record("claude/healthy", "Read: internal/a.go")
	now = now.Add(time.Second)
	b.Record("claude/healthy", "Edit: internal/a.go")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		prompt := req.Messages[1].Content
		for _, want := range []string{"agent codex/impl", "4 identical in a row", "ev/min", "-", "Bash: go test ./..."} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt missing %q:\n%s", want, prompt)
			}
		}
		if strings.Contains(prompt, "claude/healthy") {
			t.Errorf("healthy agent leaked into prompt:\n%s", prompt)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": `{"agent":"codex/impl","verdict":"watch","reason":"same test repeats"}`},
		})
	}))
	defer srv.Close()

	w := &Watcher{BaseURL: srv.URL, Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if b.WatcherNote() != "watch codex/impl: same test repeats" {
		t.Fatalf("note = %q", b.WatcherNote())
	}
}

func TestWatcherStructuredVerdictParsed(t *testing.T) {
	b, _ := loopingBoard()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"content": strings.Join([]string{
				`{"agent":"codex/impl","verdict":"stuck","reason":"same command with no change"}`,
				`{"agent":"claude/review","verdict":"healthy","reason":"new files are appearing"}`,
			}, "\n")},
		})
	}))
	defer srv.Close()
	w := &Watcher{BaseURL: srv.URL, Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := b.WatcherNote(); got != "stuck codex/impl: same command with no change" {
		t.Fatalf("note = %q", got)
	}
}

func TestWatcherDegradesWhenOllamaDown(t *testing.T) {
	b, _ := loopingBoard()
	w := &Watcher{BaseURL: "http://127.0.0.1:1", Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err == nil {
		t.Fatalf("expected error when ollama unreachable")
	}
	if b.WatcherNote() != "" {
		t.Fatalf("note should stay empty on failure, got %q", b.WatcherNote())
	}
}

func TestWatcherMalformedResponseLeavesPriorNote(t *testing.T) {
	b, _ := loopingBoard()
	b.SetWatcherNote("watch prior: retained")
	w := &Watcher{BaseURL: "http://ollama", Model: "test", Board: b, client: &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"message":{"content":"not json"}}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.WatcherNote() != "watch prior: retained" {
		t.Fatalf("malformed response changed note to %q", b.WatcherNote())
	}
}

func TestWatcherNoAgentsNoCall(t *testing.T) {
	b := NewBoard()
	b.SetWatcherNote("stale")
	w := &Watcher{BaseURL: "http://127.0.0.1:1", Model: "test", Board: b}
	if err := w.pollOnce(context.Background()); err != nil {
		t.Fatalf("empty board should be a no-op, got %v", err)
	}
	if b.WatcherNote() != "" {
		t.Fatalf("empty board must clear stale note, got %q", b.WatcherNote())
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
