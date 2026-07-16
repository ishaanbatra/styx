// Package agent implements persistent agent threads: durable named
// conversations with CLI agents (claude/codex/agy), resumed per turn via the
// CLI's own session store. Styx never grows its own tool loop: the CLIs are the
// agents; this package aims them and tracks their lifecycle.
package agent

import (
	"encoding/json"
	"strings"
)

// EventType labels a streamed agent event.
type EventType string

const (
	EventInit   EventType = "init"   // session started; SessionID set
	EventText   EventType = "text"   // intermediate assistant text
	EventTool   EventType = "tool"   // agent invoked a tool; Tool + Text (target) set
	EventResult EventType = "result" // final result; Text + token usage set
)

// Event is one parsed line of an agent's stream output.
type Event struct {
	Type         EventType
	SessionID    string
	Tool         string // tool name for EventTool (e.g. "Bash", "command_execution")
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
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
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
// does not care about (hooks, malformed input).
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
			if c.Type == "tool_use" {
				return Event{Type: EventTool, Tool: c.Name, Text: claudeToolTarget(c.Input)}, true
			}
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

// codexLine mirrors the subset of `codex exec --json` events styx reads:
// thread.started (thread_id = resumable session), item.completed
// agent_message items (assistant text), turn.completed (exact usage),
// turn.failed (error).
type codexLine struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		Command  string `json:"command"`
		Path     string `json:"path"`
		FilePath string `json:"file_path"`
		Changes  []struct {
			Path string `json:"path"`
		} `json:"changes"`
	} `json:"item"`
	Usage struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ParseCodexEvent parses one codex exec --json line. ok is false for lines
// styx does not care about (deltas, malformed input).
func ParseCodexEvent(line []byte) (Event, bool) {
	var l codexLine
	if err := json.Unmarshal(line, &l); err != nil {
		return Event{}, false
	}
	switch l.Type {
	case "thread.started":
		if l.ThreadID == "" {
			return Event{}, false
		}
		return Event{Type: EventInit, SessionID: l.ThreadID}, true
	case "item.completed":
		switch l.Item.Type {
		case "agent_message":
			if l.Item.Text == "" {
				return Event{}, false
			}
			return Event{Type: EventText, Text: l.Item.Text}, true
		case "":
			return Event{}, false
		case "command_execution":
			return Event{Type: EventTool, Tool: "Bash", Text: firstLine(l.Item.Command)}, true
		case "file_change":
			target := l.Item.Path
			if target == "" {
				target = l.Item.FilePath
			}
			if target == "" && len(l.Item.Changes) > 0 {
				target = l.Item.Changes[0].Path
			}
			return Event{Type: EventTool, Tool: "Edit", Text: firstLine(target)}, true
		default:
			// Any non-message completed item is tool/command activity. codex item
			// types include mcp_tool_call; exact sub-field names vary by codex
			// version, so surface the item type plus a best-effort command string.
			return Event{Type: EventTool, Tool: l.Item.Type, Text: firstLine(l.Item.Command)}, true
		}
	case "turn.completed":
		return Event{
			Type:         EventResult,
			InputTokens:  l.Usage.InputTokens + l.Usage.CachedInputTokens,
			OutputTokens: l.Usage.OutputTokens,
		}, true
	case "turn.failed":
		return Event{Type: EventResult, Text: l.Error.Message, IsError: true}, true
	}
	return Event{}, false
}

// claudeToolTarget pulls a best-effort target out of a tool_use input block:
// the shell command, the file path, the URL, or the search pattern — whichever
// is present. Empty when the tool takes none of these (target-less tools still
// surface via their name).
func claudeToolTarget(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Path     string `json:"path"`
		URL      string `json:"url"`
		Pattern  string `json:"pattern"`
	}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	switch {
	case m.Command != "":
		return firstLine(m.Command)
	case m.FilePath != "":
		return m.FilePath
	case m.Path != "":
		return m.Path
	case m.URL != "":
		return m.URL
	case m.Pattern != "":
		return m.Pattern
	}
	return ""
}

// firstLine returns the first line of s, trimmed, capped at 80 runes.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 80 {
		return string([]rune(s)[:80]) + "…"
	}
	return s
}
