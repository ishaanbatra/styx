package main

import (
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/router"
)

func TestRouteChannelFallback(t *testing.T) {
	tests := []struct {
		name    string
		routing config.Routing
	}{
		{
			name: "routing error",
			routing: config.Routing{Rules: []config.Rule{{
				Verb: "test",
			}}},
		},
		{
			name: "unregistered routed channel",
			routing: config.Routing{Rules: []config.Rule{{
				Verb: "test",
				Use:  "missing:model",
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ollama := &recordingChannel{}
			a := &app{
				router: router.FromConfig(tt.routing, nil),
				channels: map[string]channel.Channel{
					"ollama": ollama,
				},
			}

			got := routeChannel(a, "test", nil)
			if got.ch != ollama {
				t.Errorf("channel = %T, want fallback Ollama channel", got.ch)
			}
			if got.model != "qwen2.5-coder:7b" {
				t.Errorf("model = %q, want qwen2.5-coder:7b", got.model)
			}
			if got.id != "ollama:qwen2.5-coder:7b" {
				t.Errorf("id = %q, want ollama:qwen2.5-coder:7b", got.id)
			}
		})
	}
}
