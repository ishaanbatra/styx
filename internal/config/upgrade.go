package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

const agyPinnedTarget = "agy:Gemini 3.1 Pro (High)"

var debugRuleBlocks = []struct {
	verb  string
	block string
}{
	{"debug.sweep", `[[rule]]
verb = "debug.sweep"
use  = "agy:Gemini 3.1 Pro (High)"
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

const deadCodeRuleBlock = `
# ── dead-code (agy sweep, deterministic grep, codex spot-check) ──
[[rule]]
verb = "dead-code"
use  = "agy:Gemini 3.1 Pro (High)"
fallback = ["claude:sonnet", "codex"]
`

var deadCodeVerbRE = regexp.MustCompile(`(?m)^\s*verb\s*=\s*"dead-code"`)

// EnsureDeadCodeRule appends the dead-code routing rule when absent. Existing
// custom rules are preserved verbatim and the operation is idempotent.
func EnsureDeadCodeRule(content string) (string, bool) {
	if deadCodeVerbRE.MatchString(content) {
		return content, false
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n" + deadCodeRuleBlock, true
}

const mapImpactRuleBlock = `
# ── map-impact (agy dependency trace, codex edge spot-check) ──
[[rule]]
verb = "map-impact"
use  = "agy:Gemini 3.1 Pro (High)"
fallback = ["claude:sonnet", "codex"]
`

var mapImpactVerbRE = regexp.MustCompile(`(?m)^\s*verb\s*=\s*"map-impact"`)

// EnsureMapImpactRule appends the map-impact routing rule when absent.
// Existing custom rules are preserved verbatim and the operation is idempotent.
func EnsureMapImpactRule(content string) (string, bool) {
	if mapImpactVerbRE.MatchString(content) {
		return content, false
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n" + mapImpactRuleBlock, true
}

const crossRepoRuleBlock = `
# ── cross-repo (agy multi-root link trace, codex spot-check) ──
[[rule]]
verb = "cross-repo"
use  = "agy:Gemini 3.1 Pro (High)"
fallback = ["claude:sonnet", "codex"]
`

var crossRepoVerbRE = regexp.MustCompile(`(?m)^\s*verb\s*=\s*"cross-repo"`)

// EnsureCrossRepoRule appends the cross-repo routing rule when absent.
// Existing custom rules are preserved verbatim and the operation is idempotent.
func EnsureCrossRepoRule(content string) (string, bool) {
	if crossRepoVerbRE.MatchString(content) {
		return content, false
	}
	trimmed := strings.TrimRight(content, "\n")
	return trimmed + "\n" + crossRepoRuleBlock, true
}

const prDraftRulesBlock = `
# ── PR drafting (bounded local prose, one cheap-cloud fallback) ──
[[rule]]
verb = "pr.title"
signals = ["complex"]
use  = "claude:sonnet"
fallback = ["codex"]

[[rule]]
verb = "pr.title"
use  = "mlx:mlx-community/Qwen2.5-Coder-7B-Instruct-4bit"
fallback = ["ollama:qwen2.5-coder:7b", "claude:haiku"]

[[rule]]
verb = "pr.body"
signals = ["complex"]
use  = "claude:sonnet"
fallback = ["codex"]

[[rule]]
verb = "pr.body"
use  = "mlx:mlx-community/Qwen2.5-Coder-7B-Instruct-4bit"
fallback = ["ollama:qwen2.5-coder:7b", "claude:haiku"]
`

// EnsurePRDraftRules appends either missing PR drafting rule while preserving
// any existing custom rule for that verb. The operation is idempotent.
func EnsurePRDraftRules(content string) (string, bool) {
	missing := make([]string, 0, 2)
	for _, verb := range []string{"pr.title", "pr.body"} {
		re := regexp.MustCompile(`(?m)^\s*verb\s*=\s*"` + regexp.QuoteMeta(verb) + `"`)
		if re.MatchString(content) {
			continue
		}
		parts := strings.Split(prDraftRulesBlock, "[[rule]]")
		var blocks []string
		for _, part := range parts[1:] {
			block := "[[rule]]" + part
			if strings.Contains(block, `verb = "`+verb+`"`) {
				blocks = append(blocks, strings.TrimSpace(block))
			}
		}
		missing = append(missing, strings.Join(blocks, "\n\n")+"\n")
	}
	if len(missing) == 0 {
		return content, false
	}
	trimmed := strings.TrimRight(content, "\n")
	header := "\n\n# ── PR drafting (bounded local prose, one cheap-cloud fallback) ──\n"
	return trimmed + header + strings.Join(missing, "\n"), true
}

// EnsureAgyModelPin replaces unpinned agy routing targets with the seeded
// subscription-CLI model label. Agy remembers the user's last interactive
// model choice, so "default" is not deterministic. Explicit custom models are
// preserved. Returns the new content and whether any routing target changed.
func EnsureAgyModelPin(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	changed := false
	for i, line := range lines {
		if !routingTargetLineRE.MatchString(line) {
			continue
		}
		rewritten := strings.ReplaceAll(line, `"agy:default"`, `"`+agyPinnedTarget+`"`)
		rewritten = strings.ReplaceAll(rewritten, `'agy:default'`, `'`+agyPinnedTarget+`'`)
		if rewritten != line {
			lines[i] = rewritten
			changed = true
		}
	}
	if !changed {
		return content, false
	}
	return strings.Join(lines, "\n"), true
}

var routingTargetLineRE = regexp.MustCompile(`^\s*(?:use|fallback|parallel|synthesize_with)\s*=`)

const (
	oldSeededOllamaModel = "ollama:qwen2.5-coder:14b"
	newSeededOllamaModel = "ollama:qwen2.5-coder:7b"
)

var oldSeededOllamaTargetLines = map[string]struct{}{
	`fallback = ["ollama:qwen2.5-coder:14b"]`:                  {},
	`fallback = ["codex", "ollama:qwen2.5-coder:14b"]`:         {},
	`use  = "ollama:qwen2.5-coder:14b"`:                        {},
	`fallback = ["claude:sonnet", "ollama:qwen2.5-coder:14b"]`: {},
}

// RewriteSeededOllamaModel replaces the 14b model only in target lines that
// exactly match the old seeded routing table. Any visible customization,
// including spacing or fallback-chain changes, is preserved.
func RewriteSeededOllamaModel(content string) (string, int) {
	lines := strings.Split(content, "\n")
	rewrites := 0
	for i, line := range lines {
		if _, ok := oldSeededOllamaTargetLines[line]; !ok {
			continue
		}
		lines[i] = strings.Replace(line, oldSeededOllamaModel, newSeededOllamaModel, 1)
		rewrites++
	}
	if rewrites == 0 {
		return content, 0
	}
	return strings.Join(lines, "\n"), rewrites
}

const (
	seededMLXTarget    = "mlx:mlx-community/Qwen2.5-Coder-7B-Instruct-4bit"
	seededOllamaTarget = "ollama:qwen2.5-coder:7b"
)

var seededMLXRuleShapes = []struct {
	oldLines []string
	newLines []string
}{
	{
		oldLines: []string{
			`[[rule]]`,
			`verb = "pr.title"`,
			`use  = "` + seededOllamaTarget + `"`,
			`fallback = ["claude:haiku"]`,
		},
		newLines: []string{
			`[[rule]]`,
			`verb = "pr.title"`,
			`use  = "` + seededMLXTarget + `"`,
			`fallback = ["` + seededOllamaTarget + `", "claude:haiku"]`,
		},
	},
	{
		oldLines: []string{
			`[[rule]]`,
			`verb = "pr.body"`,
			`use  = "` + seededOllamaTarget + `"`,
			`fallback = ["claude:haiku"]`,
		},
		newLines: []string{
			`[[rule]]`,
			`verb = "pr.body"`,
			`use  = "` + seededMLXTarget + `"`,
			`fallback = ["` + seededOllamaTarget + `", "claude:haiku"]`,
		},
	},
	{
		oldLines: []string{
			`[[rule]]`,
			`verb = "grunt"`,
			`signals = ["trivial"]`,
			`use  = "` + seededOllamaTarget + `"`,
		},
		newLines: []string{
			`[[rule]]`,
			`verb = "grunt"`,
			`signals = ["trivial"]`,
			`use  = "` + seededMLXTarget + `"`,
			`fallback = ["` + seededOllamaTarget + `"]`,
		},
	},
	{
		oldLines: []string{
			`[[rule]]`,
			`verb = "grunt"`,
			`use  = "` + seededOllamaTarget + `"`,
		},
		newLines: []string{
			`[[rule]]`,
			`verb = "grunt"`,
			`use  = "` + seededMLXTarget + `"`,
			`fallback = ["` + seededOllamaTarget + `"]`,
		},
	},
}

var seededMLXBurnInComments = map[string]bool{
	"# MLX burn-in alternative (leave disabled until host smoke-testing):": true,
	`# use  = "` + seededMLXTarget + `"`:                                   true,
}

// RewriteSeededMLXPrimaries promotes MLX only for the four untouched seeded
// PR/grunt rule shapes. Matching is exact for every non-comment config line,
// so custom targets, fallbacks, signals, or spacing are preserved. The old
// burn-in comments are removed from rules that are promoted.
func RewriteSeededMLXPrimaries(content string) (string, int) {
	lines := strings.Split(content, "\n")
	rewrites := 0
	for start := 0; start < len(lines); {
		if lines[start] != "[[rule]]" {
			start++
			continue
		}
		end := len(lines)
		for i := start + 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "[[rule]]" || (strings.HasPrefix(trimmed, "[") && trimmed != "") {
				end = i
				break
			}
		}
		configLines := make([]string, 0, end-start)
		for _, line := range lines[start:end] {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			configLines = append(configLines, line)
		}
		var replacement []string
		for _, shape := range seededMLXRuleShapes {
			if slices.Equal(configLines, shape.oldLines) {
				replacement = shape.newLines
				break
			}
		}
		if replacement == nil {
			start = end
			continue
		}
		block := make([]string, 0, end-start+1)
		configIdx := 0
		for _, line := range lines[start:end] {
			trimmed := strings.TrimSpace(line)
			if seededMLXBurnInComments[line] {
				continue
			}
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				block = append(block, line)
				continue
			}
			if configIdx < len(replacement) {
				block = append(block, replacement[configIdx])
			}
			configIdx++
		}
		if configIdx < len(replacement) {
			block = append(block, replacement[configIdx:]...)
		}
		lines = append(append(append([]string{}, lines[:start]...), block...), lines[end:]...)
		end = start + len(block)
		rewrites++
		start = end
	}
	if rewrites == 0 {
		return content, 0
	}
	return strings.Join(lines, "\n"), rewrites
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

// UpgradeResult reports every migration performed by UpgradeRoutingFile.
// Named fields keep callers stable as new idempotent routing migrations are
// added.
type UpgradeResult struct {
	GeminiRewrites    int
	OllamaRewrites    int
	MLXRewrites       int
	ImplementInjected bool
	FableRestored     bool
	TaskCapInjected   bool
	HostInjected      bool
	WatchInjected     bool
	DebugInjected     bool
	DeadCodeInjected  bool
	MapImpactInjected bool
	CrossRepoInjected bool
	PRDraftInjected   bool
	AgyPinned         bool
}

// Changed reports whether the routing file was rewritten by any migration.
func (r UpgradeResult) Changed() bool {
	return r.GeminiRewrites > 0 || r.OllamaRewrites > 0 || r.MLXRewrites > 0 ||
		r.ImplementInjected || r.FableRestored ||
		r.TaskCapInjected || r.HostInjected || r.WatchInjected ||
		r.DebugInjected || r.DeadCodeInjected || r.MapImpactInjected ||
		r.CrossRepoInjected || r.PRDraftInjected || r.AgyPinned
}

// UpgradeRoutingFile reads routingPath, rewrites gemini:* to agy:default (v0.2),
// replaces exact seeded qwen2.5-coder:14b target lines with 7b,
// promotes the four exact seeded local PR/grunt rules from Ollama to MLX,
// injects the `implement` verb rules if missing (v0.3), restores the seeded fable
// tier mapping (v0.4), seeds the [conductor] max_background_tasks cap (B1) and
// interactive host, seeds the [watch] section (C5), injects ultraFerdDebug and
// dead-code, map-impact, cross-repo, and PR drafting rules, cleans stale budget
// keys, dedupes fallback arrays,
// backs up the original to routing.v0.1.toml.bak, and atomically writes the new
// content.
// Returns the gemini, seeded-Ollama, and seeded-MLX rewrite counts, whether
// implement rules were injected, whether the fable tier was restored, whether the
// conductor task cap and host were injected, whether the [watch] section was
// injected, whether any debug/read-pathway/PR drafting rules were injected,
// and whether unpinned agy targets were pinned.
// Missing-file is not an error.
func UpgradeRoutingFile(routingPath string) (UpgradeResult, error) {
	b, err := os.ReadFile(routingPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return UpgradeResult{}, nil
		}
		return UpgradeResult{}, fmt.Errorf("read routing: %w", err)
	}
	newContent, n := RewriteRoutingGeminiToAgy(string(b))
	newContent, ollamaRewrites := RewriteSeededOllamaModel(newContent)
	newContent, mlxRewrites := RewriteSeededMLXPrimaries(newContent)
	newContent, injected := EnsureImplementRules(newContent)
	newContent, fable := EnsureFableTier(newContent)
	newContent, taskCap := EnsureConductorTaskCap(newContent)
	newContent, host := EnsureConductorHost(newContent)
	newContent, watch := EnsureWatchSection(newContent)
	newContent, debug := EnsureDebugRules(newContent)
	newContent, deadCode := EnsureDeadCodeRule(newContent)
	newContent, mapImpact := EnsureMapImpactRule(newContent)
	newContent, crossRepo := EnsureCrossRepoRule(newContent)
	newContent, prDraft := EnsurePRDraftRules(newContent)
	newContent, pinned := EnsureAgyModelPin(newContent)
	// Use content comparison: skip write if nothing changed at all
	if newContent == string(b) {
		return UpgradeResult{}, nil
	}
	backup := filepath.Join(filepath.Dir(routingPath), "routing.v0.1.toml.bak")
	if err := os.WriteFile(backup, b, 0o644); err != nil {
		return UpgradeResult{}, fmt.Errorf("write backup %s: %w", backup, err)
	}
	tmp := routingPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(newContent), 0o644); err != nil {
		return UpgradeResult{}, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, routingPath); err != nil {
		return UpgradeResult{}, fmt.Errorf("atomic rename: %w", err)
	}
	return UpgradeResult{
		GeminiRewrites: n, OllamaRewrites: ollamaRewrites, MLXRewrites: mlxRewrites,
		ImplementInjected: injected, FableRestored: fable,
		TaskCapInjected: taskCap, HostInjected: host, WatchInjected: watch,
		DebugInjected: debug, DeadCodeInjected: deadCode,
		MapImpactInjected: mapImpact, CrossRepoInjected: crossRepo,
		PRDraftInjected: prDraft, AgyPinned: pinned,
	}, nil
}
