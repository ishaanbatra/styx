package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func TestDefaultRouting_NoVersionPins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(path, []byte(defaultRoutingTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := config.LoadRoutingFile(path)
	if err != nil {
		t.Fatalf("seeded routing must parse: %v", err)
	}
	if strings.Contains(defaultRoutingTOML, "codex:gpt") {
		t.Error("seeded routing still pins a codex version")
	}
	if strings.Contains(defaultRoutingTOML, "claude:opus-4") || strings.Contains(defaultRoutingTOML, "claude:sonnet-4") {
		t.Error("seeded routing still pins a claude version")
	}
	// Agy is intentionally exempt: its subscription CLI remembers the user's
	// last interactive model, so a pin prevents a sweep from silently using it.
	agyRules := 0
	for _, rule := range r.Rules {
		if !strings.HasPrefix(rule.Use, "agy:") {
			continue
		}
		agyRules++
		if rule.Use != "agy:Gemini 3.1 Pro (High)" {
			t.Errorf("seeded %s route uses %q, want sticky-model agy pin", rule.Verb, rule.Use)
		}
	}
	if agyRules == 0 {
		t.Error("seeded routing has no agy rules")
	}
	if r.Models.RefreshIntervalHours == 0 {
		t.Error("seeded routing missing [models] (defaults not applied?)")
	}
	if r.Ollama.KeepAlive != "5m" || r.Ollama.PreloadModels {
		t.Errorf("seeded ollama policy = %+v, want keep_alive 5m with preload disabled", r.Ollama)
	}
	for _, want := range []string{`[ollama]`, `keep_alive = "5m"`, `preload_models = false`} {
		if !strings.Contains(defaultRoutingTOML, want) {
			t.Errorf("seeded routing missing %q", want)
		}
	}
	if !r.Memory.Guard {
		t.Error("seeded routing must enable the memory guard")
	}
	for _, want := range []string{`[memory]`, `guard = true`} {
		if !strings.Contains(defaultRoutingTOML, want) {
			t.Errorf("seeded routing missing %q", want)
		}
	}
	if r.Conductor.Host != "claude" || !strings.Contains(defaultRoutingTOML, `host = "claude"`) {
		t.Errorf("seeded conductor host = %q, want claude", r.Conductor.Host)
	}
}

func TestDefaultChannelsMemoryGuard(t *testing.T) {
	for _, guard := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[guard], func(t *testing.T) {
			channels := defaultChannels(nil, config.Routing{
				Memory: config.MemoryConfig{Guard: guard},
			})
			for name, ch := range channels {
				progressWrapper, ok := ch.(*channel.WithProgress)
				if !ok {
					t.Fatalf("%s outer decorator = %T, want *channel.WithProgress", name, ch)
				}
				_, guarded := progressWrapper.Inner.(*channel.WithMemoryGuard)
				if guarded != guard {
					t.Errorf("%s memory guarded = %v, want %v", name, guarded, guard)
				}
			}
		})
	}
}

func TestDefaultRouting_DebugRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(path, []byte(defaultRoutingTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	routing, err := config.LoadRoutingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := router.FromConfig(routing, nil)
	tests := []struct {
		verb, channel, model, effort string
	}{
		{"debug.sweep", "agy", "Gemini 3.1 Pro (High)", ""},
		{"debug.review.codex", "codex", "", "high"},
		{"debug.review.claude", "claude", "sonnet", ""},
	}
	for _, tt := range tests {
		t.Run(tt.verb, func(t *testing.T) {
			got, err := r.Route(context.Background(), router.Request{Verb: tt.verb, Signals: []string{signals.SigDebug}})
			if err != nil {
				t.Fatal(err)
			}
			if got.Channel != tt.channel || got.Model != tt.model || got.Effort != tt.effort {
				t.Errorf("decision = %+v", got)
			}
			if got.BlockedByBudget {
				t.Error("seeded debug route unexpectedly blocked")
			}
		})
	}
}

func TestDefaultRoutingReadSweepRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(path, []byte(defaultRoutingTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	routing, err := config.LoadRoutingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := router.FromConfig(routing, nil)
	for _, verb := range []string{"dead-code", "map-impact", "cross-repo"} {
		t.Run(verb, func(t *testing.T) {
			got, err := r.Route(context.Background(), router.Request{Verb: verb})
			if err != nil {
				t.Fatal(err)
			}
			if got.Channel != "agy" || got.Model != "Gemini 3.1 Pro (High)" {
				t.Errorf("%s decision = %+v", verb, got)
			}
			if len(got.Fallback) != 2 || got.Fallback[0].Channel != "claude" || got.Fallback[0].Model != "sonnet" || got.Fallback[1].Channel != "codex" {
				t.Errorf("%s fallback = %+v", verb, got.Fallback)
			}
		})
	}
}

func TestDefaultRoutingPRDraftRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(path, []byte(defaultRoutingTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	routing, err := config.LoadRoutingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	r := router.FromConfig(routing, nil)
	for _, verb := range []string{"pr.title", "pr.body"} {
		t.Run(verb, func(t *testing.T) {
			got, err := r.Route(context.Background(), router.Request{Verb: verb})
			if err != nil {
				t.Fatal(err)
			}
			if got.Channel != "ollama" || got.Model != "qwen2.5-coder:7b" {
				t.Errorf("decision = %+v", got)
			}
			if len(got.Fallback) != 1 || got.Fallback[0].Channel != "claude" || got.Fallback[0].Model != "haiku" {
				t.Errorf("fallback = %+v", got.Fallback)
			}
		})
		t.Run(verb+" complex", func(t *testing.T) {
			got, err := r.Route(context.Background(), router.Request{Verb: verb, Signals: []string{"complex"}})
			if err != nil {
				t.Fatal(err)
			}
			if got.Channel != "claude" || got.Model != "sonnet" || len(got.Fallback) != 1 || got.Fallback[0].Channel != "codex" {
				t.Errorf("complex decision = %+v", got)
			}
		})
	}
}
