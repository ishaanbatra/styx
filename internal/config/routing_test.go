package config

import (
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
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}
