package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLoadRoutingFile(t *testing.T) {
	got, err := LoadRoutingFile("../../testdata/routing/basic.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := Routing{
		Budget: BudgetCaps{
			Claude: ChannelCap{CapPct: 80},
			Codex:  ChannelCap{CapPct: 75},
		},
		Rules: []Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus-4-7", Fallback: []string{"claude:sonnet-4-6"}},
			{Verb: "plan", Use: "claude:sonnet-4-6", Fallback: []string{"codex:gpt-5", "ollama:qwen2.5-coder:14b"}},
			{Verb: "review", Parallel: []string{"claude:sonnet-4-6", "codex:gpt-5"}, SynthesizeWith: "claude:sonnet-4-6"},
		},
		Brain: defaultBrainForTest(),
		Tiers: defaultTiersForTest(),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadRoutingFile_Missing(t *testing.T) {
	_, err := LoadRoutingFile("/nonexistent/path.toml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestLoadRoutingFile_MessageLimits(t *testing.T) {
	got, err := LoadRoutingFile("../../testdata/routing/with_msg_limits.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := Routing{
		Budget: BudgetCaps{
			Claude: ChannelCap{CapPct: 80, MessagesPer5h: 45, MessagesPerWeek: 225},
			Codex:  ChannelCap{CapPct: 80, MessagesPer5h: 50, MessagesPerWeek: 250},
			Agy:    ChannelCap{CapPct: 80, MessagesPer5h: 100, MessagesPerWeek: 500},
			Ollama: ChannelCap{CapPct: 0},
		},
		Rules: []Rule{
			{Verb: "plan", Use: "claude:sonnet-4-6"},
		},
		Brain: defaultBrainForTest(),
		Tiers: defaultTiersForTest(),
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestBrainAndTiersConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routing.toml")
	content := `
[brain]
model = "qwen3:4b"
embed_model = "nomic-embed-text"
confidence_threshold = 0.6
context_threshold_pct = 75
fable_weekly_cap = 50

[tiers]
fable = "fable"
opus = "opus"
sonnet = "sonnet"
haiku = "haiku"

[budget]
claude.cap_pct = 80
claude.timeout_minutes = 12
`
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRoutingFile(p)
	if err != nil {
		t.Fatalf("LoadRoutingFile: %v", err)
	}
	if r.Brain.Model != "qwen3:4b" || r.Brain.EmbedModel != "nomic-embed-text" {
		t.Errorf("brain models = %q/%q", r.Brain.Model, r.Brain.EmbedModel)
	}
	if r.Brain.ConfidenceThreshold != 0.6 || r.Brain.ContextThresholdPct != 75 {
		t.Errorf("brain thresholds = %v/%v", r.Brain.ConfidenceThreshold, r.Brain.ContextThresholdPct)
	}
	if r.Brain.FableWeeklyCap != 50 {
		t.Errorf("FableWeeklyCap = %d", r.Brain.FableWeeklyCap)
	}
	if r.Tiers["sonnet"] != "sonnet" {
		t.Errorf("Tiers[sonnet] = %q", r.Tiers["sonnet"])
	}
	if r.Budget.Claude.TimeoutMinutes != 12 {
		t.Errorf("TimeoutMinutes = %d", r.Budget.Claude.TimeoutMinutes)
	}
}

func TestBrainDefaultsAppliedWhenSectionMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(p, []byte("[budget]\nclaude.cap_pct = 80\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRoutingFile(p)
	if err != nil {
		t.Fatalf("LoadRoutingFile: %v", err)
	}
	if r.Brain.Model != "qwen2.5-coder:7b" {
		t.Errorf("default brain model = %q, want qwen2.5-coder:7b", r.Brain.Model)
	}
	if r.Brain.EmbedModel != "nomic-embed-text" {
		t.Errorf("default embed model = %q", r.Brain.EmbedModel)
	}
	if r.Brain.ConfidenceThreshold != 0.5 {
		t.Errorf("default confidence = %v, want 0.5", r.Brain.ConfidenceThreshold)
	}
	if r.Brain.ContextThresholdPct != 70 {
		t.Errorf("default context threshold = %v, want 70", r.Brain.ContextThresholdPct)
	}
	if r.Brain.FableWeeklyCap != 80 {
		t.Errorf("default fable cap = %d, want 80", r.Brain.FableWeeklyCap)
	}
	if r.Tiers["fable"] != "opus" || r.Tiers["haiku"] != "haiku" {
		t.Errorf("default tiers = %v", r.Tiers)
	}
}

func defaultBrainForTest() BrainConfig {
	return BrainConfig{
		Model:               "qwen2.5-coder:7b",
		EmbedModel:          "nomic-embed-text",
		ConfidenceThreshold: 0.5,
		ContextThresholdPct: 70,
		FableWeeklyCap:      80,
	}
}

func defaultTiersForTest() map[string]string {
	return map[string]string{
		"fable":  "opus",
		"opus":   "opus",
		"sonnet": "sonnet",
		"haiku":  "haiku",
	}
}
