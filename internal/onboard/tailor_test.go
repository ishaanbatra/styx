package onboard

import (
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/ishaanbatra/styx/internal/config"
)

const routingFixture = `# comments remain hand-editable
[budget]
claude.cap_pct = 80
codex.cap_pct = 80
agy.cap_pct = 80
ollama.cap_pct = 0

[[rule]]
verb = "implement"
use = "codex"
fallback = ["claude:sonnet", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "research"
use = "agy:default"
fallback = ["ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "review"
parallel = ["claude:sonnet", "codex"]
synthesize_with = "claude:sonnet"
`

func TestTailorRouting(t *testing.T) {
	tests := []struct {
		name          string
		subscriptions Subscriptions
		want          []config.Rule
		wantUnchanged bool
	}{
		{
			name:          "all subscriptions preserve default",
			subscriptions: Subscriptions{Claude: true, Codex: true, Agy: true, Ollama: true},
			wantUnchanged: true,
		},
		{
			name:          "no subscriptions preserve usable default",
			subscriptions: Subscriptions{},
			wantUnchanged: true,
		},
		{
			name:          "codex primary routes to claude when codex missing",
			subscriptions: Subscriptions{Claude: true, Agy: true, Ollama: true},
			want: []config.Rule{
				{Verb: "implement", Use: "claude:sonnet", Fallback: []string{"ollama:qwen2.5-coder:14b", "agy:default"}},
				{Verb: "research", Use: "agy:default", Fallback: []string{"ollama:qwen2.5-coder:14b"}},
				{Verb: "review", Parallel: []string{"claude:sonnet"}, SynthesizeWith: "claude:sonnet"},
			},
		},
		{
			name:          "agy primary routes to ollama when agy missing",
			subscriptions: Subscriptions{Claude: true, Codex: true, Ollama: true},
			want: []config.Rule{
				{Verb: "implement", Use: "codex", Fallback: []string{"claude:sonnet", "ollama:qwen2.5-coder:14b"}},
				{Verb: "research", Use: "ollama:qwen2.5-coder:14b", Fallback: []string{"claude:sonnet", "codex"}},
				{Verb: "review", Parallel: []string{"claude:sonnet", "codex"}, SynthesizeWith: "claude:sonnet"},
			},
		},
		{
			name:          "only codex redirects every rule",
			subscriptions: Subscriptions{Codex: true},
			want: []config.Rule{
				{Verb: "implement", Use: "codex"},
				{Verb: "research", Use: "codex"},
				{Verb: "review", Parallel: []string{"codex"}, SynthesizeWith: "codex"},
			},
		},
		{
			name:          "only ollama collapses parallel review",
			subscriptions: Subscriptions{Ollama: true},
			want: []config.Rule{
				{Verb: "implement", Use: "ollama:qwen2.5-coder:14b"},
				{Verb: "research", Use: "ollama:qwen2.5-coder:14b"},
				{Verb: "review", Parallel: []string{"ollama:qwen2.5-coder:14b"}, SynthesizeWith: "ollama:qwen2.5-coder:14b"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TailorRouting(routingFixture, tt.subscriptions)
			if err != nil {
				t.Fatalf("TailorRouting: %v", err)
			}
			if tt.wantUnchanged {
				if got != routingFixture {
					t.Fatal("TailorRouting changed an already-compatible default")
				}
				return
			}

			var parsed config.Routing
			if _, err := toml.Decode(got, &parsed); err != nil {
				t.Fatalf("tailored TOML is invalid: %v\n%s", err, got)
			}
			if !reflect.DeepEqual(parsed.Rules, tt.want) {
				t.Errorf("rules = %#v, want %#v", parsed.Rules, tt.want)
			}
			if got[:len("# comments remain hand-editable")] != "# comments remain hand-editable" {
				t.Error("tailoring discarded the default's comments")
			}
		})
	}
}

func TestTailorRoutingRejectsInvalidDefault(t *testing.T) {
	if _, err := TailorRouting("[[rule]", Subscriptions{Claude: true}); err == nil {
		t.Fatal("TailorRouting accepted invalid TOML")
	}
}
