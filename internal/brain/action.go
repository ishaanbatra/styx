// Package brain implements the styx REPL's routing brain: a small local
// ollama model emitting schema-constrained Actions, with claude-haiku
// escalation when confidence is low.
package brain

import "encoding/json"

// ActionType enumerates what the brain can decide to do with one utterance.
type ActionType string

const (
	ActionReply            ActionType = "reply"             // answer directly, no dispatch
	ActionDispatch         ActionType = "dispatch"          // send to one agent thread
	ActionParallelDispatch ActionType = "parallel_dispatch" // send to several threads at once
	ActionPipeline         ActionType = "pipeline"          // run an existing styx pipeline verb
	ActionHandoff          ActionType = "handoff"           // open interactive claude on the thread
	ActionRemember         ActionType = "remember"          // store a memory item
	ActionEscalate         ActionType = "escalate"          // brain is unsure; escalate routing
)

// Dispatch is one outbound message to an agent thread.
type Dispatch struct {
	Thread     string   `json:"thread"`                // claude | codex | agy | ollama
	Model      string   `json:"model,omitempty"`       // tier (fable|opus|sonnet|haiku) or ollama model
	Message    string   `json:"message"`               // what to send the agent
	CLIOptions []string `json:"cli_options,omitempty"` // extra CLI flags, e.g. --add-dir
	Rationale  string   `json:"rationale,omitempty"`   // one line, shown to the user
}

// Action is the brain's full decision for one turn.
type Action struct {
	Action     ActionType `json:"action"`
	Dispatches []Dispatch `json:"dispatches,omitempty"`
	Pipeline   string     `json:"pipeline,omitempty"` // research | auto | review | intel
	Reply      string     `json:"reply,omitempty"`
	Remember   string     `json:"remember,omitempty"`
	Confidence float64    `json:"confidence"`
}

var validThreads = map[string]bool{"claude": true, "codex": true, "agy": true, "ollama": true}
var validPipelines = map[string]bool{"research": true, "auto": true, "review": true, "intel": true}

// Valid reports whether the action is structurally usable. The REPL treats
// invalid actions like a brain failure (retry, then ask the user).
func (a Action) Valid() bool {
	switch a.Action {
	case ActionReply:
		return a.Reply != ""
	case ActionDispatch, ActionParallelDispatch:
		if len(a.Dispatches) == 0 {
			return false
		}
		for _, d := range a.Dispatches {
			if !validThreads[d.Thread] || d.Message == "" {
				return false
			}
		}
		return true
	case ActionPipeline:
		return validPipelines[a.Pipeline]
	case ActionRemember:
		return a.Remember != ""
	case ActionHandoff, ActionEscalate:
		return true
	default:
		return false
	}
}

// ActionSchema is the JSON schema sent as ollama's `format` parameter so the
// model can only emit valid Action JSON (structured outputs).
var ActionSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["reply", "dispatch", "parallel_dispatch", "pipeline", "handoff", "remember", "escalate"]
    },
    "dispatches": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "thread": {"type": "string", "enum": ["claude", "codex", "agy", "ollama"]},
          "model": {"type": "string"},
          "message": {"type": "string"},
          "cli_options": {"type": "array", "items": {"type": "string"}},
          "rationale": {"type": "string"}
        },
        "required": ["thread", "message"]
      }
    },
    "pipeline": {"type": "string", "enum": ["research", "auto", "review", "intel", ""]},
    "reply": {"type": "string"},
    "remember": {"type": "string"},
    "confidence": {"type": "number"}
  },
  "required": ["action", "confidence"]
}`)
