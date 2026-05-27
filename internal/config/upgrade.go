package config

import (
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// rewriteRE matches gemini:flash or gemini:pro inside use/fallback/parallel/synthesize_with values.
var rewriteRE = regexp.MustCompile(`gemini:(?:flash|pro)`)

// RewriteRoutingGeminiToAgy substitutes gemini:flash and gemini:pro with
// agy:default in the input. Returns the new content and the substitution count.
// A migration comment is inserted above each [[rule]] block that contained a substitution.
func RewriteRoutingGeminiToAgy(content string) (string, int) {
	lines := strings.Split(content, "\n")
	var out []string
	count := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "[[rule]]" {
			ruleStart := i
			ruleEnd := len(lines)
			for j := i + 1; j < len(lines); j++ {
				trim := strings.TrimSpace(lines[j])
				if trim == "[[rule]]" || (len(trim) > 0 && trim[0] == '[') {
					ruleEnd = j
					break
				}
			}
			ruleLines := lines[ruleStart:ruleEnd]
			joined := strings.Join(ruleLines, "\n")
			if rewriteRE.MatchString(joined) {
				count += len(rewriteRE.FindAllString(joined, -1))
				joined = rewriteRE.ReplaceAllString(joined, "agy:default")
				out = append(out, "# migrated from gemini-cli to agy in v0.2")
				out = append(out, strings.Split(joined, "\n")...)
			} else {
				out = append(out, ruleLines...)
			}
			i = ruleEnd - 1
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), count
}

// UpgradeRoutingFile reads routingPath, rewrites gemini:* to agy:default,
// backs up the original to routing.v0.1.toml.bak, and atomically writes the new content.
// Returns the substitution count. Missing-file is not an error (returns 0, nil).
func UpgradeRoutingFile(routingPath string) (int, error) {
	b, err := os.ReadFile(routingPath)
	if err != nil {
		if stderrors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read routing: %w", err)
	}
	newContent, n := RewriteRoutingGeminiToAgy(string(b))
	if n == 0 {
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
