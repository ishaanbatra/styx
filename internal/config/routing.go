package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Routing is the parsed routing.toml.
type Routing struct {
	Budget BudgetCaps `toml:"budget"`
	Rules  []Rule     `toml:"rule"`
}

// BudgetCaps holds the per-channel cap percentages.
type BudgetCaps struct {
	Claude     ChannelCap `toml:"claude"`
	Codex      ChannelCap `toml:"codex"`
	GeminiFree ChannelCap `toml:"gemini_free"`
	GeminiPaid ChannelCap `toml:"gemini_paid"`
}

// ChannelCap is the maximum percentage of a channel's budget to use before degrading.
type ChannelCap struct {
	CapPct float64 `toml:"cap_pct"`
}

// Rule is a single routing rule. First match wins.
//
// Either Use (single channel) OR Parallel+SynthesizeWith (multi-channel review pattern) must be set.
type Rule struct {
	Verb           string   `toml:"verb"`
	Signals        []string `toml:"signals"`
	Use            string   `toml:"use"`             // "channel:model" for single-channel rules
	Parallel       []string `toml:"parallel"`        // for parallel review-style verbs
	SynthesizeWith string   `toml:"synthesize_with"` // channel that merges parallel outputs
	Fallback       []string `toml:"fallback"`        // ordered fallback chain
}

// LoadRouting loads routing.toml from the default config path.
func LoadRouting() (Routing, error) {
	p, err := paths.RoutingPath()
	if err != nil {
		return Routing{}, err
	}
	return LoadRoutingFile(p)
}

// LoadRoutingFile loads routing config from an explicit path.
func LoadRoutingFile(path string) (Routing, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Routing{}, fmt.Errorf("read routing config %s: %w", path, err)
	}
	var r Routing
	if err := toml.Unmarshal(b, &r); err != nil {
		return Routing{}, fmt.Errorf("parse routing config %s: %w", path, err)
	}
	return r, nil
}
