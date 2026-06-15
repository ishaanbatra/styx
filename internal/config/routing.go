package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Routing is the parsed routing.toml.
type Routing struct {
	Budget BudgetCaps        `toml:"budget"`
	Rules  []Rule            `toml:"rule"`
	Brain  BrainConfig       `toml:"brain"`
	Tiers  map[string]string `toml:"tiers"`
}

// BudgetCaps holds the per-channel cap percentages.
type BudgetCaps struct {
	Claude ChannelCap `toml:"claude"`
	Codex  ChannelCap `toml:"codex"`
	Agy    ChannelCap `toml:"agy"`
	Ollama ChannelCap `toml:"ollama"`
}

// ChannelCap is the maximum percentage of a channel's budget to use before degrading.
type ChannelCap struct {
	CapPct          float64 `toml:"cap_pct"`
	MessagesPer5h   int     `toml:"messages_per_5h"`
	MessagesPerWeek int     `toml:"messages_per_week"`
	TimeoutMinutes  int     `toml:"timeout_minutes"`
}

// BrainConfig configures the REPL's local routing brain.
type BrainConfig struct {
	Model               string  `toml:"model"`                 // ollama model for routing decisions
	EmbedModel          string  `toml:"embed_model"`           // ollama model for memory embeddings
	ConfidenceThreshold float64 `toml:"confidence_threshold"`  // below this, escalate routing to claude haiku
	ContextThresholdPct float64 `toml:"context_threshold_pct"` // distill-and-restart threads above this
	FableWeeklyCap      int     `toml:"fable_weekly_cap"`      // weekly fable messages before degrading to opus
}

// applyBrainDefaults fills zero-valued brain/tier settings so configs written
// before this section existed keep working.
func applyBrainDefaults(r *Routing) {
	if r.Brain.Model == "" {
		r.Brain.Model = "llama3.2:3b"
	}
	if r.Brain.EmbedModel == "" {
		r.Brain.EmbedModel = "nomic-embed-text"
	}
	if r.Brain.ConfidenceThreshold == 0 {
		r.Brain.ConfidenceThreshold = 0.5
	}
	if r.Brain.ContextThresholdPct == 0 {
		r.Brain.ContextThresholdPct = 70
	}
	if r.Brain.FableWeeklyCap == 0 {
		r.Brain.FableWeeklyCap = 80
	}
	if r.Tiers == nil {
		r.Tiers = map[string]string{}
	}
	for tier, model := range map[string]string{
		// fable -> opus: Fable 5 is suspended worldwide (2026-06-12 US export
		// directive). Opus 4.8 is the top callable model. Restore "fable" if it returns.
		"fable": "opus", "opus": "opus", "sonnet": "sonnet", "haiku": "haiku",
	} {
		if r.Tiers[tier] == "" {
			r.Tiers[tier] = model
		}
	}
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
	applyBrainDefaults(&r)
	return r, nil
}
