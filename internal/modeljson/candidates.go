// Package modeljson contains small helpers for recovering structured JSON from
// model output without weakening the schema validation owned by each pathway.
package modeljson

import (
	"regexp"
	"strings"
)

var fencedJSONRE = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// Candidates returns the strict output, fenced payloads, and the widest
// brace-delimited payload in deterministic order. Callers still unmarshal into
// their own concrete envelope and validate every entry independently.
func Candidates(raw string) []string {
	candidates := []string{strings.TrimSpace(raw)}
	for _, match := range fencedJSONRE.FindAllStringSubmatch(raw, -1) {
		if len(match) == 2 {
			candidates = append(candidates, strings.TrimSpace(match[1]))
		}
	}
	if start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); start >= 0 && end > start {
		candidates = append(candidates, raw[start:end+1])
	}
	return candidates
}
