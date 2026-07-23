// Package research implements the deep-research convergence loop:
// drafter (agy) + critic (codex) iterating until critic returns no
// BLOCKING/IMPORTANT findings or 6 rounds elapse.
package research

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// ErrDegraded reports that Parse had to preserve unstructured input as one
// IMPORTANT finding.
var ErrDegraded = errors.New("critique parse degraded")

// Critique is the structured output expected from the critic.
type Critique struct {
	Blocking  []string `json:"blocking"`
	Important []string `json:"important"`
	Nits      []string `json:"nits"`
}

// Converged returns true when there are no blocking or important findings.
// Nits are non-blocking and do not prevent convergence.
func (c Critique) Converged() bool {
	return len(c.Blocking) == 0 && len(c.Important) == 0
}

// Parse handles three formats in order:
//  1. Strict JSON object
//  2. JSON object embedded in surrounding prose
//  3. Plain-text BLOCKING/IMPORTANT/NIT headed sections
//
// As a last resort, garbage input is treated as a single IMPORTANT finding
// so the loop continues safely rather than silently converging.
func Parse(raw string) (Critique, error) {
	raw = strings.TrimSpace(raw)
	if c, ok := tryStrictJSON(raw); ok {
		return c, nil
	}
	if c, ok := tryEmbeddedJSON(raw); ok {
		return c, nil
	}
	if c, ok := tryKeywordSections(raw); ok {
		return c, nil
	}
	// Garbage fallback.
	return Critique{Important: []string{raw}}, ErrDegraded
}

func tryStrictJSON(s string) (Critique, bool) {
	var c Critique
	if err := json.Unmarshal([]byte(s), &c); err == nil {
		// Distinguish empty-Critique from "all-nil unmarshal succeeded on something else".
		if c.Blocking != nil || c.Important != nil || c.Nits != nil {
			return c, true
		}
		// Strict JSON object with empty arrays.
		if strings.Contains(s, "blocking") && strings.Contains(s, "important") {
			return Critique{
				Blocking:  emptyIfNil(c.Blocking),
				Important: emptyIfNil(c.Important),
				Nits:      emptyIfNil(c.Nits),
			}, true
		}
	}
	return Critique{}, false
}

var jsonObjectRE = regexp.MustCompile(`(?s)\{[^{}]*"blocking"[^{}]*\}`)

func tryEmbeddedJSON(s string) (Critique, bool) {
	m := jsonObjectRE.FindString(s)
	if m == "" {
		return Critique{}, false
	}
	var c Critique
	if err := json.Unmarshal([]byte(m), &c); err == nil {
		return c, true
	}
	return Critique{}, false
}

var sectionRE = regexp.MustCompile(`(?i)^(?:#{1,6}[ \t]*)?(?:\*\*(BLOCKING|IMPORTANT|NIT(?:S)?)[ \t]*:?\*\*|__(BLOCKING|IMPORTANT|NIT(?:S)?)[ \t]*:?__|(BLOCKING|IMPORTANT|NIT(?:S)?)[ \t]*:?)[ \t]*:?[ \t]*$`)

func tryKeywordSections(s string) (Critique, bool) {
	lines := strings.Split(s, "\n")
	var cur string
	sectionStarted := false
	c := Critique{}
	found := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if m := sectionRE.FindStringSubmatch(trim); m != nil {
			cur = firstNonEmpty(m[1:]...)
			cur = strings.ToUpper(cur)
			if cur == "NITS" {
				cur = "NIT"
			}
			sectionStarted = false
			found = true
			continue
		}
		if cur == "" || trim == "" {
			continue
		}
		// Treat bullet-list items as findings.
		item := strings.TrimSpace(strings.TrimLeft(trim, "-*•"))
		if item == "" {
			continue
		}
		if !sectionStarted && isNoneFinding(item) {
			// A None declaration makes the whole section empty; explanatory
			// text after it is not a finding.
			cur = ""
			continue
		}
		sectionStarted = true
		switch cur {
		case "BLOCKING":
			c.Blocking = append(c.Blocking, item)
		case "IMPORTANT":
			c.Important = append(c.Important, item)
		case "NIT":
			c.Nits = append(c.Nits, item)
		}
	}
	return c, found
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func isNoneFinding(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "none") {
		return true
	}
	return s == "n/a" ||
		strings.HasPrefix(s, "n/a.") ||
		strings.HasPrefix(s, "n/a ") ||
		strings.HasPrefix(s, "n/a(")
}

func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
