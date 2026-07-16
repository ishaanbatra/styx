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

const watcherSystem = `You judge whether parallel AI coding agents are stuck.
You are given only agents that mechanical checks already flagged as suspicious
(a long run of identical actions, a long ping-pong between two actions, or a
long idle). Repeated edits, tests, or reads of the SAME file are NORMAL
forward progress — do not call that a loop.
A real loop is the SAME action (or the same two actions alternating) repeating
with NO state change (same command, same target, same result, over and over).
A stall is a long idle with no new action. For EACH agent, reply with one JSON object per line:
{"agent":"<label>","verdict":"healthy|watch|stuck","reason":"<one short line>"}
Return only JSON lines, nothing else.`

// Watcher periodically summarizes cross-agent activity via local ollama and
// writes the result to the board. Strictly best-effort: every failure path
// leaves the mechanical layer (renderer + stall flag) untouched.
type Watcher struct {
	BaseURL  string // e.g. http://localhost:11434
	Model    string
	Board    *Board
	Interval time.Duration // 0 => 15s
	Stall    time.Duration // 0 => DefaultStall

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

// pollOnce runs one watch cycle. Healthy mechanical signals skip ollama and
// clear stale alarms. On model failure it returns the error and leaves the
// existing note untouched.
func (w *Watcher) pollOnce(ctx context.Context) error {
	snap := w.Board.Snapshot()
	live := snap[:0]
	for _, s := range snap {
		if !s.Done {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		w.Board.SetWatcherNote("")
		return nil
	}
	now := w.Board.clockNow()
	stall := w.Stall
	if stall <= 0 {
		stall = DefaultStall
	}
	type flaggedAgent struct {
		state   AgentState
		signals signalSet
	}
	flagged := make([]flaggedAgent, 0, len(live))
	for _, s := range live {
		signals, result := classify(s, now, stall)
		if result == suspicious {
			flagged = append(flagged, flaggedAgent{state: s, signals: signals})
		}
	}
	if len(flagged) == 0 {
		w.Board.SetWatcherNote("")
		return nil
	}

	var u strings.Builder
	for _, flagged := range flagged {
		s := flagged.state
		sig := flagged.signals
		fmt.Fprintf(&u, "agent %s — idle %s, %d identical in a row, trailing run of %d events within 2 distinct actions, %d distinct recent actions, %d distinct files, %.1f ev/min\n",
			s.Label, short(sig.Idle), sig.ConsecutiveIdentical, sig.TrailingLowVariety, sig.DistinctRecent, sig.DistinctFiles, sig.EventsPerMin)
		for _, ev := range w.Board.RecentEvents(s.Label) {
			fmt.Fprintf(&u, "  -%s  %s\n", short(now.Sub(ev.At)), ev.Summary)
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
	type modelVerdict struct {
		Agent   string `json:"agent"`
		Verdict string `json:"verdict"`
		Reason  string `json:"reason"`
	}
	var notes []string
	usable := 0
	for _, line := range strings.Split(strings.TrimSpace(cr.Message.Content), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v modelVerdict
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			continue
		}
		v.Agent = strings.TrimSpace(v.Agent)
		v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
		v.Reason = strings.TrimSpace(v.Reason)
		if v.Agent == "" || v.Reason == "" || (v.Verdict != "healthy" && v.Verdict != "watch" && v.Verdict != "stuck") {
			continue
		}
		usable++
		if v.Verdict == "watch" || v.Verdict == "stuck" {
			notes = append(notes, fmt.Sprintf("%s %s: %s", v.Verdict, v.Agent, v.Reason))
		}
	}
	if usable > 0 {
		w.Board.SetWatcherNote(strings.Join(notes, " · "))
	}
	return nil
}
