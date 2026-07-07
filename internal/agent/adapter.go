package agent

import "os"

// Adapter encodes how to drive one CLI agent: argument construction, stream
// parsing, and what session features it supports. `styx doctor` reports which
// mode (native-resume vs styx-maintained continuity) each adapter runs in.
type Adapter interface {
	CLI() string
	Bin() string
	SupportsResume() bool // CLI persists sessions; per-turn --resume works
	SupportsStream() bool // CLI emits parseable per-line JSON events
	ContextWindow() int   // tokens; drives the distill threshold
	BuildArgs(msg, sessionID, model string, extra []string, readOnly bool) []string
	ParseEvent(line []byte) (Event, bool)
}

// ClaudeAdapter drives the claude CLI in headless stream-json mode.
// Headless dispatches run with permissions pre-granted, matching the existing
// `execute` verb behavior; interactive handoff keeps native prompts.
type ClaudeAdapter struct {
	BinPath string // override for tests; "" means "claude" on PATH
	Window  int    // override for tests; 0 means 1M (200k if CLAUDE_CODE_DISABLE_1M_CONTEXT=1)
}

// NewClaudeAdapter returns the production claude adapter.
func NewClaudeAdapter() *ClaudeAdapter { return &ClaudeAdapter{} }

func (a *ClaudeAdapter) CLI() string { return "claude" }

func (a *ClaudeAdapter) Bin() string {
	if a.BinPath != "" {
		return a.BinPath
	}
	return "claude"
}

func (a *ClaudeAdapter) SupportsResume() bool { return true }
func (a *ClaudeAdapter) SupportsStream() bool { return true }

func (a *ClaudeAdapter) ContextWindow() int {
	if a.Window > 0 {
		return a.Window
	}
	// Opus 4.8 / Sonnet 5 / Fable 5 run the 1M window on the Anthropic API
	// and Max plans; honor Claude Code's own opt-out env. Haiku threads
	// (rare) over-estimate their window — acceptable: distill still fires,
	// just later; claude's own compaction is the backstop.
	if os.Getenv("CLAUDE_CODE_DISABLE_1M_CONTEXT") == "1" {
		return 200000
	}
	return 1000000
}

func (a *ClaudeAdapter) BuildArgs(msg, sessionID, model string, extra []string, readOnly bool) []string {
	return claudeArgs(sessionID, model, msg, extra, readOnly)
}

// claudeArgs builds the headless claude invocation. Read-only turns omit the
// pre-granted write permission.
func claudeArgs(sessionID, model, msg string, extra []string, readOnly bool) []string {
	args := []string{}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, extra...)
	args = append(args, "-p", msg, "--output-format", "stream-json", "--verbose")
	if !readOnly {
		args = append(args, "--dangerously-skip-permissions")
	}
	return args
}

func (a *ClaudeAdapter) ParseEvent(line []byte) (Event, bool) { return ParseClaudeEvent(line) }

// PlainAdapter drives CLIs without session resume or stream-json (codex, agy
// in v1). The whole stdout becomes one result; conversation continuity is
// styx-maintained via the thread's rolling summary.
type PlainAdapter struct {
	CLIName string
	BinPath string
	Window  int
	ArgsFn  func(msg, model string, extra []string) []string
}

func (a *PlainAdapter) CLI() string { return a.CLIName }

func (a *PlainAdapter) Bin() string {
	if a.BinPath != "" {
		return a.BinPath
	}
	return a.CLIName
}

func (a *PlainAdapter) SupportsResume() bool                 { return false }
func (a *PlainAdapter) SupportsStream() bool                 { return false }
func (a *PlainAdapter) ContextWindow() int                   { return a.Window }
func (a *PlainAdapter) ParseEvent(line []byte) (Event, bool) { return Event{}, false }
func (a *PlainAdapter) BuildArgs(msg, sessionID, model string, extra []string, readOnly bool) []string {
	return a.ArgsFn(msg, model, extra)
}

// CodexAdapter drives `codex exec --json` with native session resume
// (`codex exec resume <thread_id>`). Usage comes from turn.completed events —
// exact tokens, not estimates. Edit-risk turns run --sandbox workspace-write
// (codex exec defaults to read-only); read-risk turns keep the default.
type CodexAdapter struct {
	BinPath string // override for tests; "" means "codex" on PATH
	Window  int    // override for tests; 0 means 400000 (GPT-5.5 in Codex)
}

// NewCodexAdapter returns the production codex adapter.
func NewCodexAdapter() *CodexAdapter { return &CodexAdapter{} }

func (a *CodexAdapter) CLI() string { return "codex" }

func (a *CodexAdapter) Bin() string {
	if a.BinPath != "" {
		return a.BinPath
	}
	return "codex"
}

func (a *CodexAdapter) SupportsResume() bool { return true }
func (a *CodexAdapter) SupportsStream() bool { return true }

func (a *CodexAdapter) ContextWindow() int {
	if a.Window > 0 {
		return a.Window
	}
	return 400000
}

func (a *CodexAdapter) BuildArgs(msg, sessionID, model string, extra []string, readOnly bool) []string {
	args := []string{}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, "exec")
	if sessionID != "" {
		args = append(args, "resume", sessionID)
	}
	args = append(args, "--json")
	if !readOnly {
		args = append(args, "--sandbox", "workspace-write")
	}
	args = append(args, extra...)
	return append(args, msg)
}

func (a *CodexAdapter) ParseEvent(line []byte) (Event, bool) { return ParseCodexEvent(line) }

// NewAgyAdapter drives `agy -p` (Antigravity). Always headless-permissive,
// matching the existing agy channel.
func NewAgyAdapter() *PlainAdapter {
	return &PlainAdapter{
		CLIName: "agy",
		BinPath: "agy",
		Window:  1000000,
		ArgsFn: func(msg, model string, extra []string) []string {
			args := []string{"-p", msg, "--dangerously-skip-permissions"}
			return append(args, extra...)
		},
	}
}
