package onboard

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/ishaanbatra/styx/internal/config"
)

// Subscription identifies one routing channel the user can access.
type Subscription string

const (
	Claude Subscription = "claude"
	Codex  Subscription = "codex"
	Agy    Subscription = "agy"
	Ollama Subscription = "ollama"
)

// Subscriptions is the set of channels selected during onboarding.
type Subscriptions map[Subscription]bool

var defaultTarget = map[Subscription]string{
	Claude: "claude:sonnet",
	Codex:  "codex",
	Agy:    "agy:default",
	Ollama: "ollama:qwen2.5-coder:14b",
}

var replacementOrder = map[Subscription][]Subscription{
	Claude: {Claude, Codex, Ollama, Agy},
	Codex:  {Codex, Claude, Ollama, Agy},
	Agy:    {Agy, Ollama, Claude, Codex},
	Ollama: {Ollama, Claude, Codex, Agy},
}

// TailorRouting returns defaultContent with routing targets adjusted to use
// only the selected subscriptions. It is deterministic and has no side
// effects. An empty set preserves the default because there is no valid
// channel to which a rule can be redirected.
func TailorRouting(defaultContent string, subscriptions Subscriptions) (string, error) {
	var routing config.Routing
	if _, err := toml.Decode(defaultContent, &routing); err != nil {
		return "", fmt.Errorf("parse default routing: %w", err)
	}
	if !anySubscription(subscriptions) {
		return defaultContent, nil
	}

	original := append([]config.Rule(nil), routing.Rules...)
	for i := range routing.Rules {
		tailorRule(&routing.Rules[i], subscriptions)
	}
	if reflect.DeepEqual(original, routing.Rules) {
		return defaultContent, nil
	}
	return renderRules(defaultContent, routing.Rules)
}

func anySubscription(subscriptions Subscriptions) bool {
	for _, selected := range subscriptions {
		if selected {
			return true
		}
	}
	return false
}

func tailorRule(rule *config.Rule, subscriptions Subscriptions) {
	if len(rule.Parallel) > 0 {
		tailorParallelRule(rule, subscriptions)
		return
	}

	primaryChannel := targetChannel(rule.Use)
	if subscriptions[primaryChannel] {
		rule.Fallback = filterTargets(rule.Fallback, subscriptions)
		return
	}

	existing := append([]string{rule.Use}, rule.Fallback...)
	candidates := replacementTargets(primaryChannel, existing, subscriptions)
	rule.Use = candidates[0]
	rule.Fallback = candidates[1:]
}

func tailorParallelRule(rule *config.Rule, subscriptions Subscriptions) {
	filtered := filterTargets(rule.Parallel, subscriptions)
	if len(filtered) == 0 {
		base := targetChannel(rule.SynthesizeWith)
		filtered = replacementTargets(base, rule.Parallel, subscriptions)
	}
	rule.Parallel = filtered
	if subscriptions[targetChannel(rule.SynthesizeWith)] && containsTarget(filtered, rule.SynthesizeWith) {
		return
	}
	rule.SynthesizeWith = filtered[0]
}

func replacementTargets(primary Subscription, existing []string, subscriptions Subscriptions) []string {
	byChannel := make(map[Subscription]string, len(existing))
	for _, target := range existing {
		channel := targetChannel(target)
		if _, ok := byChannel[channel]; !ok {
			byChannel[channel] = target
		}
	}

	order := replacementOrder[primary]
	if len(order) == 0 {
		order = []Subscription{Claude, Codex, Agy, Ollama}
	}
	var targets []string
	for _, channel := range order {
		if !subscriptions[channel] {
			continue
		}
		target := byChannel[channel]
		if target == "" {
			target = defaultTarget[channel]
		}
		targets = append(targets, target)
	}
	return targets
}

func filterTargets(targets []string, subscriptions Subscriptions) []string {
	filtered := make([]string, 0, len(targets))
	for _, target := range targets {
		if subscriptions[targetChannel(target)] {
			filtered = append(filtered, target)
		}
	}
	return filtered
}

func targetChannel(target string) Subscription {
	channel, _, _ := strings.Cut(target, ":")
	return Subscription(channel)
}

func containsTarget(targets []string, target string) bool {
	for _, candidate := range targets {
		if candidate == target {
			return true
		}
	}
	return false
}

func renderRules(content string, rules []config.Rule) (string, error) {
	lines := strings.Split(content, "\n")
	ruleIndex := -1
	for i := 0; i < len(lines); {
		if strings.TrimSpace(lines[i]) != "[[rule]]" {
			i++
			continue
		}
		ruleIndex++
		if ruleIndex >= len(rules) {
			return "", fmt.Errorf("render tailored routing: more rule blocks than parsed rules")
		}
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[end]), "[") {
			end++
		}
		block := rewriteRuleBlock(lines[i:end], rules[ruleIndex])
		lines = append(lines[:i], append(block, lines[end:]...)...)
		i += len(block)
	}
	if ruleIndex+1 != len(rules) {
		return "", fmt.Errorf("render tailored routing: found %d rule blocks for %d parsed rules", ruleIndex+1, len(rules))
	}
	return strings.Join(lines, "\n"), nil
}

func rewriteRuleBlock(block []string, rule config.Rule) []string {
	insertAt := -1
	kept := make([]string, 0, len(block)+2)
	for _, line := range block {
		key := strings.TrimSpace(line)
		if isRoutingLine(key) {
			if insertAt < 0 {
				insertAt = len(kept)
			}
			continue
		}
		kept = append(kept, line)
	}
	if insertAt < 0 {
		insertAt = len(kept)
	}

	var replacement []string
	if len(rule.Parallel) > 0 {
		replacement = []string{
			"parallel = " + quoteList(rule.Parallel),
			"synthesize_with = " + strconv.Quote(rule.SynthesizeWith),
		}
	} else {
		replacement = append(replacement, "use  = "+strconv.Quote(rule.Use))
		if len(rule.Fallback) > 0 {
			replacement = append(replacement, "fallback = "+quoteList(rule.Fallback))
		}
	}
	return append(kept[:insertAt], append(replacement, kept[insertAt:]...)...)
}

func isRoutingLine(line string) bool {
	for _, key := range []string{"use", "fallback", "parallel", "synthesize_with"} {
		if strings.HasPrefix(line, key+" ") || strings.HasPrefix(line, key+"=") {
			return true
		}
	}
	return false
}

func quoteList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
