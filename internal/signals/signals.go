// Package signals classifies a request into routing tags consumed by the
// router's rule evaluator. Pure function; no I/O.
package signals

import (
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
)

const trivialMaxChars = 50

var complexKeywords = []string{
	"architecture", "refactor", "migrate", "redesign", "rewrite",
}

// Extract turns (verb, args, project) into a deduplicated, sorted set of signals.
func Extract(verb string, args []string, proj config.Project) []string {
	set := map[string]struct{}{}
	add := func(s string) { set[s] = struct{}{} }
	joined := strings.ToLower(strings.Join(args, " "))

	if proj.Language != "" {
		add("lang:" + proj.Language)
	}

	switch verb {
	case "build":
		add("interactive")
	case "grunt":
		if len(joined) > 0 && len(joined) < trivialMaxChars {
			add("trivial")
		}
	case "think":
		if strings.HasPrefix(joined, "deep:") || strings.Contains(joined, "deep think") {
			add("deep")
		}
	}

	for _, kw := range complexKeywords {
		if strings.Contains(joined, kw) {
			add("complex")
			break
		}
	}

	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}
