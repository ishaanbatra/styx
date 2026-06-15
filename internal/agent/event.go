// Package agent implements persistent agent threads: durable named
// conversations with CLI agents (claude/codex/agy), resumed per turn via the
// CLI's own session store. Styx never grows its own tool loop: the CLIs are the
// agents; this package aims them and tracks their lifecycle.
package agent

import "encoding/json"

// EventType labels a streamed agent event.
type EventType string

const (
	EventInit   EventType = "init"   // session started; SessionID set
	EventText   EventType = "text"   // intermediate assistant text
	EventResult EventType = "result" // final result; Text + token usage set
)

// Event is one parsed line of an agent's stream output.
type Event struct {
	Type         EventType
	SessionID    string
	Text         string
	InputTokens  int // total context tokens (input + cache creation + cache reads)
	OutputTokens int
	IsError      bool
}

// claudeLine mirrors the subset of claude's stream-json protocol styx reads.
type claudeLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Message   struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		OutputTokens             int `json:"output_tokens"`
	} `json:"usage"`
}

// ParseClaudeEvent parses one stream-json line. ok is false for lines styx
// does not care about (tool use, hooks, malformed input).
func ParseClaudeEvent(line []byte) (Event, bool) {
	var l claudeLine
	if err := json.Unmarshal(line, &l); err != nil {
		return Event{}, false
	}
	switch l.Type {
	case "system":
		if l.Subtype != "init" || l.SessionID == "" {
			return Event{}, false
		}
		return Event{Type: EventInit, SessionID: l.SessionID}, true
	case "assistant":
		text := ""
		for _, c := range l.Message.Content {
			if c.Type == "text" {
				text += c.Text
			}
		}
		if text == "" {
			return Event{}, false
		}
		return Event{Type: EventText, Text: text}, true
	case "result":
		return Event{
			Type:         EventResult,
			SessionID:    l.SessionID,
			Text:         l.Result,
			InputTokens:  l.Usage.InputTokens + l.Usage.CacheCreationInputTokens + l.Usage.CacheReadInputTokens,
			OutputTokens: l.Usage.OutputTokens,
			IsError:      l.IsError,
		}, true
	}
	return Event{}, false
}
