package main

// styx hook <event> is the enforcement seam for conductor routing. The styx
// MCP server can only gate tools it owns (dispatch, pipeline_run); it is
// structurally blind to the conductor's OWN built-in tools (WebSearch,
// WebFetch, Task subagents, Bash-curl), which never cross the MCP boundary.
// The one layer that observes those calls is Claude Code's hook system, and
// since styx launches Claude Code it installs this subcommand as a hook (see
// internal/launcher). PreToolUse denies the crisp "doing substantive work
// myself" tools and redirects to dispatch/pipeline_run; PostToolUse records
// the fuzzy inline tail so the previously-invisible claude burn is auditable.
//
// This runs per matched tool call, so it stays off the app/SQLite/config path:
// decode stdin, decide, optionally one append write, exit. Anything not
// explicitly denied is ALLOWED — a fail-open default so a hook bug (or a
// malformed payload) can never brick a conductor session, which always has
// dispatch as the recorded escape hatch.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/paths"
)

// redirectReason is the message relayed to the conductor on a deny. It names
// the recorded paths so "I really need this" still resolves through a channel
// styx sees and bills, rather than the unrecorded inline path.
const redirectReason = "Route this through styx: pipeline_run research (writes a cited brief) or dispatch cli=claude/codex/agy — it rides a separate subscription and is recorded. Inline WebSearch/WebFetch/Task burns this session's claude quota invisibly."

// hookInput is the subset of Claude Code's hook stdin payload styx reads.
type hookInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	CWD       string          `json:"cwd"`
	SessionID string          `json:"session_id"`
}

// mcpWebPattern matches MCP tool names that are web/research fetchers (e.g.
// mcp__exa__web_search_exa) so they are gated alongside the native WebSearch/
// WebFetch. Non-fetch MCP servers (Gmail, Calendar, Drive, context7) are
// preserved. This is the "keep external MCP, extend the matcher" tradeoff: an
// MCP web tool whose name lacks these keywords slips through.
var mcpWebPattern = regexp.MustCompile(`(?i)(web|search|fetch|research|scrape|crawl)`)

// fetchTool matches an invocation of a remote-fetch CLI. Deliberately excludes
// "http"/"https" as bare words (they collide with URL schemes, which would
// flag every command that merely contains an https:// URL, e.g. git clone).
var fetchTool = regexp.MustCompile(`(?i)\b(curl|wget)\b`)

// urlPattern captures the host of an http(s) URL so localhost fetches (dev
// servers) can be distinguished from external research fetches.
var urlPattern = regexp.MustCompile(`(?i)https?://([^\s/'"|)>]+)`)

// cmdHook implements `styx hook <event>`. It never returns a non-nil error for
// a routing decision — output is on stdout; a nil return + empty stdout means
// "allow".
func cmdHook(args []string) error {
	event := ""
	if len(args) > 0 {
		event = args[0]
	}
	var in hookInput
	// Best-effort decode: a malformed or empty payload falls through to allow.
	if b, err := io.ReadAll(os.Stdin); err == nil {
		_ = json.Unmarshal(b, &in)
	}
	switch event {
	case "pretooluse":
		if deny, reason := preToolDecision(in); deny {
			return emitPreDeny(reason)
		}
		return nil
	case "posttooluse":
		recordInlineActivity(in)
		if nudge, ok := postToolNudge(in); ok {
			return emitPostContext(nudge)
		}
		return nil
	default:
		return nil
	}
}

// preToolDecision returns (deny, reason). Only the crisp "substantive work I'm
// doing myself" markers are denied; everything else (including quick Read/Grep
// and a malformed/unknown tool) is allowed.
func preToolDecision(in hookInput) (bool, string) {
	switch in.ToolName {
	case "WebSearch", "WebFetch", "Task":
		return true, redirectReason
	case "Bash":
		if bashFetchesExternally(in.ToolInput) {
			return true, redirectReason
		}
		return false, ""
	}
	if strings.HasPrefix(in.ToolName, "mcp__") && mcpWebPattern.MatchString(in.ToolName) {
		return true, redirectReason
	}
	return false, ""
}

// bashFetchesExternally reports whether a Bash command shells out to fetch a
// remote URL (curl/wget as a poor-man's WebFetch). Requires BOTH a fetch tool
// and an http(s):// URL to a non-localhost host, so builds, greps, git, and
// localhost dev-server calls are untouched.
func bashFetchesExternally(raw json.RawMessage) bool {
	var ti struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &ti); err != nil || ti.Command == "" {
		return false
	}
	if !fetchTool.MatchString(ti.Command) {
		return false
	}
	for _, m := range urlPattern.FindAllStringSubmatch(ti.Command, -1) {
		if !isLocalHost(m[1]) {
			return true
		}
	}
	return false
}

// isLocalHost reports whether host (possibly host:port) is a loopback address.
func isLocalHost(host string) bool {
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1", "[::1]":
		return true
	}
	return false
}

// postToolNudge returns a soft in-session reminder for high-signal inline work
// (only reachable in audit mode; block mode denies these before PostToolUse).
// Read/Grep are recorded silently — no nudge — to keep the session clean.
func postToolNudge(in hookInput) (string, bool) {
	switch in.ToolName {
	case "WebSearch", "WebFetch", "Task":
		return "styx: prefer pipeline_run research or dispatch for this — inline runs on this session's claude quota and isn't recorded.", true
	case "Bash":
		if bashFetchesExternally(in.ToolInput) {
			return "styx: an external fetch via Bash runs on this session's claude quota inline — prefer pipeline_run research or dispatch.", true
		}
	}
	if strings.HasPrefix(in.ToolName, "mcp__") && mcpWebPattern.MatchString(in.ToolName) {
		return "styx: MCP web tools run inline on this session's quota — prefer pipeline_run research or dispatch.", true
	}
	return "", false
}

// recordInlineActivity appends one line to the inline-activity log so the
// conductor's inline tool use is visible to the self-improvement loop. It is
// deliberately NOT the budget ledger: one row there == one subscription
// message against the 5h/weekly windows, and a tool call is not a message —
// recording per-call would falsely trip cooldowns. Errors are surfaced to
// stderr but never fail the hook (a PostToolUse failure must not break work).
func recordInlineActivity(in hookInput) {
	dir, err := paths.StateDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[styx hook] resolve state dir: %v\n", err)
		return
	}
	if err := appendInlineActivity(dir, in); err != nil {
		fmt.Fprintf(os.Stderr, "[styx hook] record inline activity: %v\n", err)
	}
}

// appendInlineActivity writes one JSONL record to <dir>/inline-activity.jsonl.
// Split from recordInlineActivity so tests can target an explicit temp dir.
func appendInlineActivity(dir string, in hookInput) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure state dir: %w", err)
	}
	line, err := json.Marshal(map[string]string{
		"ts":         time.Now().UTC().Format(time.RFC3339),
		"session_id": in.SessionID,
		"tool":       in.ToolName,
		"cwd":        in.CWD,
	})
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	path := filepath.Join(dir, "inline-activity.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("append %s: %w", path, err)
	}
	return nil
}

// preToolOutput is the PreToolUse hook decision Claude Code reads from stdout.
type preToolOutput struct {
	HookSpecificOutput struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

func emitPreDeny(reason string) error {
	var out preToolOutput
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "deny"
	out.HookSpecificOutput.PermissionDecisionReason = reason
	return json.NewEncoder(os.Stdout).Encode(out)
}

// postToolOutput injects extra context into the conductor's transcript.
type postToolOutput struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

func emitPostContext(ctx string) error {
	var out postToolOutput
	out.HookSpecificOutput.HookEventName = "PostToolUse"
	out.HookSpecificOutput.AdditionalContext = ctx
	return json.NewEncoder(os.Stdout).Encode(out)
}
