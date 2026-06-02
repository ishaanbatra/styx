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

// rewriteBudgetBlock processes lines of a [budget] section:
// - drops gemini_free.cap_pct and gemini_paid.cap_pct lines
// - inserts agy.cap_pct = 80 after the [budget] header line if not already present
func rewriteBudgetBlock(lines []string) []string {
	hasAgy := false
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "agy.cap_pct") {
			hasAgy = true
			break
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
	return out
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

// UpgradeRoutingFile reads routingPath, rewrites gemini:* to agy:default,
// cleans stale budget keys, dedupes fallback arrays,
// backs up the original to routing.v0.1.toml.bak, and atomically writes the new content.
// Returns the gemini-rule substitution count. Missing-file is not an error (returns 0, nil).
func UpgradeRoutingFile(routingPath string) (int, error) {
	b, err := os.ReadFile(routingPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read routing: %w", err)
	}
	newContent, n := RewriteRoutingGeminiToAgy(string(b))
	// Use content comparison: skip write if nothing changed at all
	if newContent == string(b) {
		return 0, nil
	}
	backup := filepath.Join(filepath.Dir(routingPath), "routing.v0.1.toml.bak")
	if err := os.WriteFile(backup, b, 0o644); err != nil {
		return 0, fmt.Errorf("write backup %s: %w", backup, err)
	}
	tmp := routingPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(newContent), 0o644); err != nil {
		return 0, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, routingPath); err != nil {
		return 0, fmt.Errorf("atomic rename: %w", err)
	}
	return n, nil
}
