package router

import (
	"context"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
)

type stubBreaker struct{ broken map[string]bool }

func (s stubBreaker) Broken(_ context.Context, ch string) bool { return s.broken[ch] }

func TestRouteSkipsBrokenChannel(t *testing.T) {
	r := &Router{
		Rules: []config.Rule{{
			Verb:     "plan",
			Use:      "claude:sonnet-4-6",
			Fallback: []string{"codex:gpt-5"},
		}},
		Breaker: stubBreaker{broken: map[string]bool{"claude": true}},
	}
	d, err := r.Route(context.Background(), Request{Verb: "plan"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Channel != "codex" {
		t.Errorf("channel = %s, want codex (claude circuit open)", d.Channel)
	}
	if !d.Degraded {
		t.Error("Degraded should be true when breaker forces fallback")
	}
}

func TestRouteNilBreakerUnchanged(t *testing.T) {
	r := &Router{
		Rules: []config.Rule{{Verb: "plan", Use: "claude:sonnet-4-6"}},
	}
	d, err := r.Route(context.Background(), Request{Verb: "plan"})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if d.Channel != "claude" || d.Degraded {
		t.Errorf("decision = %+v", d)
	}
}
