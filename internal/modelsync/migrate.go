package modelsync

import (
	"regexp"
	"strings"
)

// Change is one token rewrite the migration applied.
type Change struct{ Old, New string }

var tokenRe = regexp.MustCompile(`(codex|claude):[A-Za-z0-9._-]+`)

// MigrateText de-pins legacy version-pinned routing tokens:
//   - codex:<version>  -> codex
//   - claude:<version> -> claude:<alias>
//
// codex:interactive, claude:interactive, claude:<alias>, and any non-codex/
// non-claude token are left untouched. Pure transform; idempotent.
func MigrateText(src string, claudeAliases []string) (string, []Change) {
	aliasSet := map[string]bool{}
	for _, a := range claudeAliases {
		aliasSet[a] = true
	}
	var changes []Change
	out := tokenRe.ReplaceAllStringFunc(src, func(tok string) string {
		idx := strings.Index(tok, ":")
		ch, model := tok[:idx], tok[idx+1:]
		if model == "interactive" {
			return tok
		}
		switch ch {
		case "codex":
			changes = append(changes, Change{Old: tok, New: "codex"})
			return "codex"
		case "claude":
			if aliasSet[model] {
				return tok
			}
			alias := classAlias(model, claudeAliases)
			if alias == "" {
				return tok
			}
			newTok := "claude:" + alias
			changes = append(changes, Change{Old: tok, New: newTok})
			return newTok
		}
		return tok
	})
	return out, changes
}

// classAlias returns the alias whose name prefixes the pinned model
// (e.g. "opus-4-7" -> "opus"), or "" if none match.
func classAlias(model string, aliases []string) string {
	for _, a := range aliases {
		if strings.HasPrefix(model, a) {
			return a
		}
	}
	return ""
}
