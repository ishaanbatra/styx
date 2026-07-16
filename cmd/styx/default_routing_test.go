package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if r.Models.RefreshIntervalHours == 0 {
		t.Error("seeded routing missing [models] (defaults not applied?)")
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
		{"debug.sweep", "agy", "default", ""},
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
