package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const watcherSystem = `You are a watch process observing parallel AI coding agents.
In 1-2 terse sentences, say whether they look healthy or stuck. Call out loops,
repeated identical actions, and long idles. Do not give advice; just report.`

// Watcher periodically summarizes cross-agent activity via local ollama and
// writes the result to the board. Strictly best-effort: every failure path
// leaves the mechanical layer (renderer + stall flag) untouched.
type Watcher struct {
	BaseURL  string // e.g. http://localhost:11434
	Model    string
	Board    *Board
	Interval time.Duration // 0 => 15s

	client *http.Client
}

func (w *Watcher) httpClient() *http.Client {
	if w.client == nil {
		w.client = &http.Client{Timeout: 30 * time.Second}
	}
	return w.client
}

// Run polls until ctx is cancelled. Poll errors are swallowed here on purpose:
// a down ollama must not spam or crash the session; the note simply stays stale.
func (w *Watcher) Run(ctx context.Context) {
	iv := w.Interval
	if iv <= 0 {
		iv = 15 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = w.pollOnce(ctx)
		}
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string         `json:"model"`
	Stream    bool           `json:"stream"`
	Think     bool           `json:"think"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options"`
	Messages  []chatMessage  `json:"messages"`
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// pollOnce runs one watch cycle. It is a no-op (nil error) when no agents are
// live. On success it stores the note; on failure it returns the error and
// leaves the existing note untouched.
func (w *Watcher) pollOnce(ctx context.Context) error {
	snap := w.Board.Snapshot()
	live := snap[:0]
	for _, s := range snap {
		if !s.Done {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return nil
	}

	var u strings.Builder
	for _, s := range live {
		fmt.Fprintf(&u, "agent %s (last: %s):\n", s.Label, s.Last)
		for _, line := range w.Board.Recent(s.Label) {
			fmt.Fprintf(&u, "  - %s\n", line)
		}
	}

	body, err := json.Marshal(chatRequest{
		Model:     w.Model,
		Stream:    false,
		Think:     false,
		KeepAlive: "30m",
		Options:   map[string]any{"temperature": 0},
		Messages: []chatMessage{
			{Role: "system", Content: watcherSystem},
			{Role: "user", Content: u.String()},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal watch request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build watch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("watch call (is ollama up?): %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read watch response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("ollama watch %d: %s", resp.StatusCode, string(raw))
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return fmt.Errorf("parse watch response: %w", err)
	}
	note := strings.TrimSpace(cr.Message.Content)
	if note != "" {
		w.Board.SetWatcherNote(note)
	}
	return nil
}
