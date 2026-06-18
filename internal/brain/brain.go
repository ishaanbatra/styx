package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/channel"
)

// Turn is everything the brain sees for one routing decision.
type Turn struct {
	Utterance     string
	Summary       string   // rolling conversation summary
	RecentTurns   []string // rendered recent exchanges, oldest first
	ThreadStatus  []string // one line per live thread
	MemoryHits    []string // rendered top-k memory recalls
	BoundProjects []string // one-liner per repo currently bound to the session
	KnownProjects []string // one-liner per other registered repo
}

// Brain decides what to do with one utterance. Production uses Ollama;
// tests use scripted fakes.
type Brain interface {
	Decide(ctx context.Context, t Turn) (Action, error)
}

// ErrNeedUser means the brain cannot produce a decision (ollama down or
// emitting invalid JSON twice). The REPL must ask the user to route manually:
// it never bricks.
var ErrNeedUser = errors.New("brain unavailable")

// Escalator re-makes a routing decision with a stronger model when the local
// brain's confidence is below threshold.
type Escalator interface {
	Escalate(ctx context.Context, t Turn) (Action, error)
}

// Ollama is the production brain: a small local model with structured output.
type Ollama struct {
	BaseURL             string  // e.g. http://localhost:11434
	Model               string  // e.g. qwen2.5-coder:7b (a fast non-reasoning instruct model)
	ConfidenceThreshold float64 // escalate below this (0 disables)
	Escalator           Escalator

	client *http.Client
}

func (b *Ollama) httpClient() *http.Client {
	if b.client == nil {
		b.client = &http.Client{Timeout: 60 * time.Second}
	}
	return b.client
}

type brainChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type brainChatRequest struct {
	Model    string             `json:"model"`
	Stream   bool               `json:"stream"`
	Think    bool               `json:"think"`
	Format   json.RawMessage    `json:"format"`
	Options  map[string]any     `json:"options"`
	Messages []brainChatMessage `json:"messages"`
}

type brainChatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

// Decide implements Brain. It retries once on invalid output, escalates on
// low confidence, and wraps total failure in ErrNeedUser.
func (b *Ollama) Decide(ctx context.Context, t Turn) (Action, error) {
	sys, user := BuildPrompt(t)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		raw, err := b.chat(ctx, sys, user)
		if err != nil {
			lastErr = err
			continue
		}
		var a Action
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			lastErr = fmt.Errorf("brain emitted invalid JSON: %w", err)
			continue
		}
		if !a.Valid() {
			lastErr = fmt.Errorf("brain emitted invalid action %q", a.Action)
			continue
		}
		needsEscalation := a.Action == ActionEscalate ||
			(b.ConfidenceThreshold > 0 && a.Confidence < b.ConfidenceThreshold)
		if needsEscalation && b.Escalator != nil {
			if esc, err := b.Escalator.Escalate(ctx, t); err == nil && esc.Valid() {
				return esc, nil
			}
			// Escalation failing is not fatal; fall through to the local answer.
		}
		return a, nil
	}
	return Action{}, fmt.Errorf("%w: %v", ErrNeedUser, lastErr)
}

func (b *Ollama) chat(ctx context.Context, system, user string) (string, error) {
	body, err := json.Marshal(brainChatRequest{
		Model:  b.Model,
		Stream: false,
		// Disable reasoning-model thinking (qwen3, r1, …). Routing is a
		// schema-constrained classification, not a reasoning task; thinking adds
		// many seconds per turn (blowing the sub-second target and the request
		// timeout) and bleeds into the structured output, mis-slotting fields.
		Think:   false,
		Format:  ActionSchema,
		Options: map[string]any{"temperature": 0},
		Messages: []brainChatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal brain request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.BaseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build brain request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("brain call: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read brain response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("ollama brain %d: %s", resp.StatusCode, string(raw))
	}
	var cr brainChatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("parse brain response envelope: %w", err)
	}
	return cr.Message.Content, nil
}

// ClaudeEscalator escalates a routing decision to claude (haiku tier): one
// cheap message against the same action vocabulary.
type ClaudeEscalator struct {
	Channel channel.Channel // raw (undecorated) claude channel
	Model   string          // e.g. "haiku"
}

// Escalate implements Escalator.
func (e *ClaudeEscalator) Escalate(ctx context.Context, t Turn) (Action, error) {
	sys, user := BuildPrompt(t)
	resp, err := e.Channel.Send(ctx, channel.Request{
		Model:  e.Model,
		System: sys,
		Prompt: user + "\n\nRespond with ONLY the JSON action object, no prose.",
	})
	if err != nil {
		return Action{}, fmt.Errorf("escalation call: %w", err)
	}
	jsonText := extractJSON(resp.Text)
	if jsonText == "" {
		return Action{}, fmt.Errorf("escalation reply had no JSON object")
	}
	var a Action
	if err := json.Unmarshal([]byte(jsonText), &a); err != nil {
		return Action{}, fmt.Errorf("parse escalation reply: %w", err)
	}
	return a, nil
}

// extractJSON returns the first {...} block in s ("" if none). Frontier
// models sometimes wrap JSON in prose despite instructions.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}
