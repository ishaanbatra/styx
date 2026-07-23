package signals

import "testing"

func TestTierOf(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		model   string
		want    Tier
	}{
		{"ollama local", "ollama", "qwen2.5-coder:14b", TierLocal},
		{"mlx local", "mlx", "mlx-community/Qwen2.5-Coder-7B-Instruct-4bit", TierLocal},
		{"claude opus", "claude", "opus", TierOpus},
		{"claude opus versioned", "claude", "opus-4-7", TierOpus},
		{"claude sonnet", "claude", "sonnet", TierSonnet},
		{"claude haiku", "claude", "haiku", TierHaiku},
		{"claude interactive falls to sonnet", "claude", "interactive", TierSonnet},
		{"codex is capable", "codex", "gpt-5", TierSonnet},
		{"codex bare", "codex", "", TierSonnet},
		{"agy is capable", "agy", "default", TierSonnet},
		{"unknown cloud stays capable", "mystery", "x", TierSonnet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TierOf(c.channel, c.model); got != c.want {
				t.Errorf("TierOf(%q,%q) = %v, want %v", c.channel, c.model, got, c.want)
			}
		})
	}
}

func TestFloor(t *testing.T) {
	cases := []struct {
		name string
		sigs []string
		want Tier
	}{
		{"no signals -> no floor", nil, TierLocal},
		{"trivial imposes no floor", []string{SigTrivial}, TierLocal},
		{"lang only -> no floor", []string{"lang:go"}, TierLocal},
		{"complex -> sonnet floor", []string{SigComplex}, TierSonnet},
		{"deep -> sonnet floor", []string{SigDeep}, TierSonnet},
		{"debug -> sonnet floor", []string{SigDebug}, TierSonnet},
		{"complex + lang -> sonnet", []string{SigComplex, "lang:go"}, TierSonnet},
		{"complex + deep -> highest (sonnet)", []string{SigComplex, SigDeep}, TierSonnet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Floor(c.sigs); got != c.want {
				t.Errorf("Floor(%v) = %v, want %v", c.sigs, got, c.want)
			}
		})
	}
}

func TestTierString(t *testing.T) {
	for tier, want := range map[Tier]string{
		TierLocal: "local", TierHaiku: "haiku", TierSonnet: "sonnet", TierOpus: "opus",
	} {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q, want %q", tier, got, want)
		}
	}
}
