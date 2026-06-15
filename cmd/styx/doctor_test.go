package main

import (
	"reflect"
	"testing"

	"github.com/ishaanbatra/styx/internal/brain"
)

func TestMissingFlags(t *testing.T) {
	card := brain.Card{
		CLI:           "claude",
		ExpectedFlags: []string{"--resume", "--output-format", "--model"},
	}
	tests := []struct {
		name string
		help string
		want []string
	}{
		{
			name: "all present",
			help: "Usage: claude [options]\n  --resume <id>\n  --output-format <fmt>\n  --model <m>\n",
			want: nil,
		},
		{
			name: "one missing",
			help: "Usage: claude [options]\n  --resume <id>\n  --model <m>\n",
			want: []string{"--output-format"},
		},
		{
			name: "all missing",
			help: "Usage: totally-different-tool\n",
			want: []string{"--resume", "--output-format", "--model"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingFlags(tt.help, card); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missingFlags = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOllamaModelsMissing(t *testing.T) {
	tags := `{"models":[{"name":"llama3.2:3b"},{"name":"qwen2.5-coder:14b"}]}`
	got := ollamaModelsMissing(tags, []string{"llama3.2:3b", "nomic-embed-text"})
	want := []string{"nomic-embed-text"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("missing = %v, want %v", got, want)
	}
	// Tag-suffix tolerance: "nomic-embed-text:latest" satisfies "nomic-embed-text".
	tags = `{"models":[{"name":"nomic-embed-text:latest"}]}`
	if got := ollamaModelsMissing(tags, []string{"nomic-embed-text"}); got != nil {
		t.Errorf("missing = %v, want nil (latest tag should match)", got)
	}
}

func TestCheckTiersDeduplicatesAliases(t *testing.T) {
	seen := map[string]int{}
	ok := checkTiersWithProbe(map[string]string{
		"fable":  "opus",
		"opus":   "opus",
		"sonnet": "sonnet",
		"haiku":  "haiku",
	}, func(alias string) bool {
		seen[alias]++
		return alias != "sonnet"
	})
	if ok {
		t.Fatal("checkTiersWithProbe = true, want false")
	}
	want := map[string]int{"opus": 1, "sonnet": 1, "haiku": 1}
	if !reflect.DeepEqual(seen, want) {
		t.Errorf("probed = %v, want %v", seen, want)
	}
}
