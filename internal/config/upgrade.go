package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// rewriteRE matches gemini:flash or gemini:pro inside use/fallback/parallel/synthesize_with values.
var rewriteRE = regexp.MustCompile(`gemini:(?:flash|pro)`)

// fallbackRE matches a TOML fallback array line, capturing the bracketed list.
var fallbackRE = regexp.MustCompile(`^(\s*fallback\s*=\s*\[)([^\]]*)\]`)

// dedupeFallback removes duplicate quoted entries from a fallback line while
// preserving first-occurrence order. Returns the (possibly rewritten) line.
func dedupeFallback(line string) string {
	m := fallbackRE.FindStringSubmatchIndex(line)
	if m == nil {
		return line
	}
	prefix := line[m[2]:m[3]] // e.g. `fallback = [`
	inner := line[m[4]:m[5]]  // e.g. `"agy:default", "agy:default", "ollama:…"`

	// Split on commas, trim whitespace/quotes, track seen
	rawItems := strings.Split(inner, ",")
	// Count non-empty raw items before deduplication
	nonEmpty := 0
	for _, raw := range rawItems {
		if strings.TrimSpace(raw) != "" {
			nonEmpty++
		}
	}

	seen := make(map[string]bool)
	var kept []string
	for _, raw := range rawItems {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if !seen[trimmed] {
			seen[trimmed] = true
			kept = append(kept, trimmed)
		}
	}

	// Nothing was removed — return the original line unchanged to avoid
	// spurious cosmetic rewrites (different spacing, etc.) that would
	// cause UpgradeRoutingFile to treat a clean file as modified.
	if len(kept) == nonEmpty {
		return line
	}
	return prefix + strings.Join(kept, ", ") + "]"
}

// budgetMsgDefaults holds the default message limits injected during migration.
// ollama is intentionally omitted (unlimited).
var budgetMsgDefaults = []struct {
	channel string
	per5h   int
	perWeek int
}{
	{"claude", 45, 225},
	{"codex", 50, 250},
	{"agy", 100, 500},
}

// rewriteBudgetBlock processes lines of a [budget] section:
//   - drops gemini_free.cap_pct and gemini_paid.cap_pct lines
//   - inserts agy.cap_pct = 80 after the [budget] header line if not already present
//   - injects <channel>.messages_per_5h and <channel>.messages_per_week for each
//     of claude/codex/agy if those keys are not already present
func rewriteBudgetBlock(lines []string) []string {
	hasAgy := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "agy.cap_pct") {
			hasAgy = true
			break
		}
	}

	// Detect which channels already have message-limit keys
	hasMsgPer5h := map[string]bool{}
	for _, entry := range budgetMsgDefaults {
		key := entry.channel + ".messages_per_5h"
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), key) {
				hasMsgPer5h[entry.channel] = true
				break
			}
		}
	}

	var out []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "gemini_free.cap_pct") || strings.HasPrefix(trimmed, "gemini_paid.cap_pct") {
			// drop these stale keys
			continue
		}
		out = append(out, l)
		// After the [budget] header, inject agy.cap_pct if missing
		if !hasAgy && strings.TrimSpace(l) == "[budget]" {
			out = append(out, "agy.cap_pct = 80")
			hasAgy = true
		}
	}

	// Append missing message-limit keys at the end of the block.
	// We do this after the main pass to keep all existing lines intact.
	for _, entry := range budgetMsgDefaults {
		if !hasMsgPer5h[entry.channel] {
			out = append(out,
				fmt.Sprintf("%s.messages_per_5h   = %d", entry.channel, entry.per5h),
				fmt.Sprintf("%s.messages_per_week = %d", entry.channel, entry.perWeek),
			)
		}
	}

	return out
}

// implementRulesBlock is the v0.3 `implement` verb routing, injected into
// pre-v0.3 configs by EnsureImplementRules. Kept byte-identical to the block in
// cmd/styx/default_routing.go so seeded and upgraded configs agree.
const implementRulesBlock = `
# ── implement (autonomous code application from a plan) ──
# Added in v0.3. A detailed plan already exists by this point, so the work is
# well-scoped: codex is the primary implementer ("faster to a first diff").
# Ambiguous / multi-file architectural work (the "complex" signal) stays on
# claude, which reasons more before acting. claude is always the fallback.
[[rule]]
verb = "implement"
signals = ["complex"]
use  = "claude:sonnet-4-6"
fallback = ["codex:gpt-5", "claude:opus-4-7"]

[[rule]]
verb = "implement"
use  = "codex:gpt-5"
fallback = ["claude:sonnet-4-6"]
`

// EnsureImplementRules appends the `implement` verb rules (v0.3) when the config
// has no `implement` rule yet. Returns the new content and whether it changed.
// Idempotent: a config that already routes `implement` is returned untouched.
// Appending is safe because routing is first-match and only `implement` requests
// match these rules, so their position relative to other verbs is irrelevant.
func EnsureImplementRules(content string) (string, bool) {
	if implementVerbRE.MatchString(content) {
		return content, false
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n" + implementRulesBlock, true
}

// implementVerbRE matches a routing rule whose verb is "implement".
var implementVerbRE = regexp.MustCompile(`(?m)^\s*verb\s*=\s*"implement"`)

const debugRulesHeader = `
# ── debug (ultraFerdDebug: agy sweep, codex+claude review) ──
`

var debugRuleBlocks = []struct {
	verb  string
	block string
}{
	{"debug.sweep", `[[rule]]
verb = "debug.sweep"
use  = "agy:default"
fallback = ["claude:sonnet"]
`},
	{"debug.review.codex", `[[rule]]
verb = "debug.review.codex"
use  = "codex"
effort = "high"
`},
	{"debug.review.claude", `[[rule]]
verb = "debug.review.claude"
use  = "claude:sonnet"
`},
}

// EnsureDebugRules appends each missing ultraFerdDebug routing rule. Existing
// rules are preserved verbatim so a user's custom target is never overwritten.
func EnsureDebugRules(content string) (string, bool) {
	missing := make([]string, 0, len(debugRuleBlocks))
	for _, rule := range debugRuleBlocks {
		re := regexp.MustCompile(`(?m)^\s*verb\s*=\s*"` + regexp.QuoteMeta(rule.verb) + `"`)
		if !re.MatchString(content) {
			missing = append(missing, rule.block)
		}
	}
	if len(missing) == 0 {
		return content, false
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n" + debugRulesHeader + strings.Join(missing, "\n"), true
}

// EnsureFableTier upgrades the exact seeded suspension-era mapping
// `fable  = "opus"` to `fable  = "fable"`. Only the seeded spelling is
// rewritten — any user-customized mapping is preserved. Returns the new
// content and whether a rewrite happened.
func EnsureFableTier(content string) (string, bool) {
	const old = `fable  = "opus"`
	const new_ = `fable  = "fable"`
	if !strings.Contains(content, old) {
		return content, false
	}
	return strings.Replace(content, old, new_, 1), true
}

// conductorTaskCapKnob is the seeded max_background_tasks knob, injected into
// pre-B1 configs by EnsureConductorTaskCap. Kept identical in spirit to the
// block in cmd/styx/default_routing.go so seeded and upgraded configs agree.
const conductorTaskCapKnob = `# max concurrent background dispatches; over-cap tasks queue (collect shows position)
max_background_tasks = 4`

// conductorHostKnob is the seeded interactive host selection, injected into
// existing configs by EnsureConductorHost. Keep this aligned with the
// [conductor] block in cmd/styx/default_routing.go.
const conductorHostKnob = `# interactive host CLI: claude | codex
host = "claude"`

// EnsureConductorTaskCap injects the [conductor] max_background_tasks knob
// (B1) when absent. A config already carrying the key — any value — is left
// alone. If the [conductor] section exists the knob lands at its end; with no
// section at all, a whole seeded section is appended. Returns the new content
// and whether a rewrite happened.
func EnsureConductorTaskCap(content string) (string, bool) {
	if strings.Contains(content, "max_background_tasks") {
		return content, false
	}
	lines := strings.Split(content, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "[conductor]" {
			continue
		}
		// Find the section end: next section header or EOF.
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if len(t) > 0 && t[0] == '[' {
				end = j
				break
			}
		}
		// Trim trailing blank lines inside the section so the knob sits with it.
		insert := end
		for insert > i+1 && strings.TrimSpace(lines[insert-1]) == "" {
			insert--
		}
		out := append(append(append([]string{}, lines[:insert]...), conductorTaskCapKnob), lines[insert:]...)
		return strings.Join(out, "\n"), true
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n\n# ── Conductor (frontier-brain launcher + MCP toolbelt) ──\n[conductor]\nship_gate = \"handshake\"\n" + conductorTaskCapKnob + "\n", true
}

// EnsureConductorHost injects the [conductor] host knob when absent. A config
// already carrying the key in that section is left alone so user selection is
// preserved. EnsureConductorTaskCap runs first in the upgrade pipeline and
// guarantees the section exists there; the standalone no-section case still
// appends a complete minimal section.
func EnsureConductorHost(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "[conductor]" {
			continue
		}
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			trimmed := strings.TrimSpace(lines[j])
			if len(trimmed) > 0 && trimmed[0] == '[' {
				end = j
				break
			}
		}
		for _, conductorLine := range lines[i+1 : end] {
			key, _, found := strings.Cut(strings.TrimSpace(conductorLine), "=")
			if found && strings.TrimSpace(key) == "host" {
				return content, false
			}
		}
		out := make([]string, 0, len(lines)+2)
		out = append(out, lines[:i+1]...)
		out = append(out, strings.Split(conductorHostKnob, "\n")...)
		out = append(out, lines[i+1:]...)
		return strings.Join(out, "\n"), true
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n\n# ── Conductor (frontier-brain launcher + MCP toolbelt) ──\n[conductor]\n" + conductorHostKnob + "\n", true
}

// watchSectionRE matches a [watch] section header line.
var watchSectionRE = regexp.MustCompile(`(?m)^\s*\[watch\]\s*$`)

// watchSectionBlock is the seeded [watch] section (live dispatch
// observability), injected into pre-C5 configs by EnsureWatchSection. Kept
// byte-identical to the block in cmd/styx/default_routing.go so seeded and
// upgraded configs agree.
const watchSectionBlock = `
[watch]
stall_threshold_seconds = 90
interval_seconds = 15
ollama_enabled = true
`

// EnsureWatchSection injects the [watch] section (stall threshold, watcher
// interval, ollama toggle) when absent. A config that already has a [watch]
// section — with any contents — is left alone. Returns the new content and
// whether a rewrite happened.
func EnsureWatchSection(content string) (string, bool) {
	if watchSectionRE.MatchString(content) {
		return content, false
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n" + watchSectionBlock, true
}

// RewriteRoutingGeminiToAgy substitutes gemini:flash and gemini:pro with
// agy:default in the input. It also cleans up [budget] blocks (drops stale
// gemini_free/gemini_paid cap_pct keys, injects agy.cap_pct = 80 if absent)
// and deduplicates fallback arrays in [[rule]] blocks.
// Returns the new content and the gemini-rule substitution count (n).
func RewriteRoutingGeminiToAgy(content string) (string, int) {
	lines := strings.Split(content, "\n")
	var out []string
	count := 0
	i := 0
	for i < len(lines) {
		line := lines[i]
		trim := strings.TrimSpace(line)

		// Handle [budget] block (but not [[rule]] or other [[ sections)
		if trim == "[budget]" {
			// Collect the whole budget block
			blockStart := i
			blockEnd := len(lines)
			for j := i + 1; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if len(t) > 0 && t[0] == '[' {
					blockEnd = j
					break
				}
			}
			budgetLines := lines[blockStart:blockEnd]
			rewritten := rewriteBudgetBlock(budgetLines)
			out = append(out, rewritten...)
			i = blockEnd
			continue
		}

		// Handle [[rule]] blocks
		if trim == "[[rule]]" {
			ruleStart := i
			ruleEnd := len(lines)
			for j := i + 1; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if t == "[[rule]]" || (len(t) > 0 && t[0] == '[') {
					ruleEnd = j
					break
				}
			}
			ruleLines := lines[ruleStart:ruleEnd]
			joined := strings.Join(ruleLines, "\n")

			// Rewrite gemini:* references
			if rewriteRE.MatchString(joined) {
				count += len(rewriteRE.FindAllString(joined, -1))
				joined = rewriteRE.ReplaceAllString(joined, "agy:default")
				out = append(out, "# migrated from gemini-cli to agy in v0.2")
				ruleLines = strings.Split(joined, "\n")
			}

			// Dedupe fallback lines within the rule block
			for _, rl := range ruleLines {
				out = append(out, dedupeFallback(rl))
			}

			i = ruleEnd
			continue
		}

		out = append(out, line)
		i++
	}

	result := strings.Join(out, "\n")

	// If nothing changed at all, return original to satisfy the no-op contract
	if result == content {
		return content, 0
	}
	return result, count
}

// UpgradeRoutingFile reads routingPath, rewrites gemini:* to agy:default (v0.2),
// injects the `implement` verb rules if missing (v0.3), restores the seeded fable
// tier mapping (v0.4), seeds the [conductor] max_background_tasks cap (B1) and
// interactive host, seeds the [watch] section (C5), injects ultraFerdDebug
// rules, cleans stale budget keys, dedupes fallback arrays,
// backs up the original to routing.v0.1.toml.bak, and atomically writes the new
// content.
// Returns the gemini-rule substitution count, whether implement rules were
// injected, whether the fable tier was restored, whether the conductor task
// cap and host were injected, whether the [watch] section was injected, and
// whether any debug rules were injected. Missing-file is not an error.
func UpgradeRoutingFile(routingPath string) (geminiN int, implementInjected, fableRestored, taskCapInjected, hostInjected, watchInjected, debugInjected bool, err error) {
	b, err := os.ReadFile(routingPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, false, false, false, false, false, false, nil
		}
		return 0, false, false, false, false, false, false, fmt.Errorf("read routing: %w", err)
	}
	newContent, n := RewriteRoutingGeminiToAgy(string(b))
	newContent, injected := EnsureImplementRules(newContent)
	newContent, fable := EnsureFableTier(newContent)
	newContent, taskCap := EnsureConductorTaskCap(newContent)
	newContent, host := EnsureConductorHost(newContent)
	newContent, watch := EnsureWatchSection(newContent)
	newContent, debug := EnsureDebugRules(newContent)
	// Use content comparison: skip write if nothing changed at all
	if newContent == string(b) {
		return 0, false, false, false, false, false, false, nil
	}
	backup := filepath.Join(filepath.Dir(routingPath), "routing.v0.1.toml.bak")
	if err := os.WriteFile(backup, b, 0o644); err != nil {
		return 0, false, false, false, false, false, false, fmt.Errorf("write backup %s: %w", backup, err)
	}
	tmp := routingPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(newContent), 0o644); err != nil {
		return 0, false, false, false, false, false, false, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, routingPath); err != nil {
		return 0, false, false, false, false, false, false, fmt.Errorf("atomic rename: %w", err)
	}
	return n, injected, fable, taskCap, host, watch, debug, nil
}
