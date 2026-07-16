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

var debugKeywords = []string{
	"panic", "crash", "stack trace", "traceback", "failing test",
	"regression", "error:", "nil pointer", "segfault",
}

// Signal name constants. Keeping the emitted strings here (rather than inline
// literals) lets the capability-floor map in floor.go reference the same values.
const (
	SigTrivial     = "trivial"
	SigDeep        = "deep"
	SigComplex     = "complex"
	SigInteractive = "interactive"
	SigDebug       = "debug"
)

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
		add(SigInteractive)
	case "grunt":
		if len(joined) > 0 && len(joined) < trivialMaxChars {
			add(SigTrivial)
		}
	case "think":
		if strings.HasPrefix(joined, "deep:") || strings.Contains(joined, "deep think") {
			add(SigDeep)
		}
	case "debug":
		for _, kw := range debugKeywords {
			if strings.Contains(joined, kw) {
				add(SigDebug)
				break
			}
		}
	}

	for _, kw := range complexKeywords {
		if strings.Contains(joined, kw) {
			add(SigComplex)
			break
		}
	}

	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}
