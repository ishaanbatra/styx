package agent

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
	Window  int    // override for tests; 0 means 200000
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
	return 200000
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

// NewCodexAdapter drives `codex exec`.
func NewCodexAdapter() *PlainAdapter {
	return &PlainAdapter{
		CLIName: "codex",
		BinPath: "codex",
		Window:  200000,
		ArgsFn: func(msg, model string, extra []string) []string {
			args := []string{}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, extra...)
			return append(args, "exec", msg)
		},
	}
}

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
