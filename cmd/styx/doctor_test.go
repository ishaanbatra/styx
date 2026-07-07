package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
	noSub := func(sub string) string {
		t.Fatalf("subHelp probed for %q; card has no subcommand entries", sub)
		return ""
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingFlags(tt.help, card, noSub); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missingFlags = %v, want %v", got, tt.want)
			}
		})
	}
}

// A card may guard a subcommand's flags (e.g. `codex exec --json`), which never
// appear in top-level --help. missingFlags must fold the subcommand's own --help
// into the searched surface — while still catching genuine drift if the flag is
// gone from both.
func TestMissingFlags_Subcommand(t *testing.T) {
	card := brain.Card{
		CLI:           "codex",
		ExpectedFlags: []string{"exec", "--model", "--json"},
	}
	topHelp := "Usage: codex [options]\n  exec   Run headless\n  --model <m>\n"
	tests := []struct {
		name    string
		subHelp func(string) string
		want    []string
	}{
		{
			name: "flag lives in subcommand help",
			subHelp: func(sub string) string {
				if sub == "exec" {
					return "Usage: codex exec\n  --json   emit JSON events\n"
				}
				return ""
			},
			want: nil,
		},
		{
			name:    "flag absent from both helps is flagged",
			subHelp: func(string) string { return "Usage: codex exec\n  --sandbox <s>\n" },
			want:    []string{"--json"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingFlags(topHelp, card, tt.subHelp); !reflect.DeepEqual(got, tt.want) {
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

func TestRunModelRefresh_DePins(t *testing.T) {
	dir := t.TempDir()
	routing := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(routing, []byte("[[rule]]\nverb=\"x\"\nuse=\"claude:opus-4-7\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "models.json")
	if err := runModelRefresh(routing, cache, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(routing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "claude:opus") || strings.Contains(string(got), "opus-4-7") {
		t.Errorf("not de-pinned:\n%s", got)
	}
}
