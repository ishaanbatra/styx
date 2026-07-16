package microtask

import (
	"context"
	"errors"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

type fakeChannel struct {
	responses []channel.Response
	errors    []error
	calls     int
}

func (f *fakeChannel) Name() string { return "fake" }
func (f *fakeChannel) BudgetState(context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}
func (f *fakeChannel) Send(context.Context, channel.Request) (channel.Response, error) {
	i := f.calls
	f.calls++
	var resp channel.Response
	if i < len(f.responses) {
		resp = f.responses[i]
	}
	if i < len(f.errors) {
		return resp, f.errors[i]
	}
	return resp, nil
}

func TestRunBoundedFallback(t *testing.T) {
	parse := func(s string) (string, error) {
		if s == "valid" {
			return s, nil
		}
		return "", errors.New("invalid json")
	}
	tests := []struct {
		name         string
		primary      *fakeChannel
		fallback     *fakeChannel
		want         string
		wantAttempts int
		wantFallback bool
		wantStatic   bool
	}{
		{name: "primary succeeds", primary: &fakeChannel{responses: []channel.Response{{Text: "valid"}}}, want: "valid", wantAttempts: 1},
		{name: "invalid primary escalates once", primary: &fakeChannel{responses: []channel.Response{{Text: "bad"}}}, fallback: &fakeChannel{responses: []channel.Response{{Text: "valid"}}}, want: "valid", wantAttempts: 2, wantFallback: true},
		{name: "send and validation failure use static", primary: &fakeChannel{errors: []error{errors.New("offline")}}, fallback: &fakeChannel{responses: []channel.Response{{Text: "bad"}}}, want: "static", wantAttempts: 2, wantStatic: true},
		{name: "unavailable primary reaches fallback", fallback: &fakeChannel{responses: []channel.Response{{Text: "valid"}}}, want: "valid", wantAttempts: 2, wantFallback: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var primaryChannel channel.Channel
			if tt.primary != nil {
				primaryChannel = tt.primary
			}
			var fallback func(context.Context) *Target
			if tt.fallback != nil {
				fallback = func(context.Context) *Target {
					return &Target{Channel: tt.fallback, Name: "cloud", Model: "cheap"}
				}
			}
			got := Run(context.Background(), Options[string]{
				Primary:  Target{Channel: primaryChannel, Name: "local", Model: "small"},
				Fallback: fallback, Parse: parse, Static: "static",
			})
			if got.Value != tt.want || len(got.Attempts) != tt.wantAttempts || got.UsedFallback != tt.wantFallback || got.StaticFallback != tt.wantStatic {
				t.Fatalf("result = %+v", got)
			}
			if tt.wantFallback && !got.Attempts[1].Escalated {
				t.Error("fallback attempt must be marked escalated")
			}
			if !tt.wantStatic && !got.Attempts[len(got.Attempts)-1].Validated {
				t.Error("successful attempt must be marked validated")
			}
			if tt.wantStatic && got.Attempts[len(got.Attempts)-1].ValidationError == "" {
				t.Error("validation failure metadata missing")
			}
		})
	}
}

func TestRunUsesAtMostOneFallback(t *testing.T) {
	primary := &fakeChannel{responses: []channel.Response{{Text: "bad"}}}
	fallback := &fakeChannel{responses: []channel.Response{{Text: "bad"}, {Text: "valid"}}}
	got := Run(context.Background(), Options[string]{
		Primary: Target{Channel: primary}, Fallback: func(context.Context) *Target { return &Target{Channel: fallback} },
		Parse: func(string) (string, error) { return "", errors.New("invalid") }, Static: "static",
	})
	if !got.StaticFallback || fallback.calls != 1 || len(got.Attempts) != 2 {
		t.Fatalf("runner exceeded bounded fallback: result=%+v calls=%d", got, fallback.calls)
	}
}

func TestRunResolvesFallbackOnlyAfterPrimaryFailure(t *testing.T) {
	primary := &fakeChannel{responses: []channel.Response{{Text: "bad"}}}
	fallback := &fakeChannel{responses: []channel.Response{{Text: "valid"}}}
	resolved := 0
	got := Run(context.Background(), Options[string]{
		Primary: Target{Channel: primary},
		Fallback: func(context.Context) *Target {
			resolved++
			return &Target{Channel: fallback}
		},
		Parse: func(s string) (string, error) {
			if s == "valid" {
				return s, nil
			}
			return "", errors.New("invalid")
		},
		Static: "static",
	})
	if !got.UsedFallback || resolved != 1 || fallback.calls != 1 {
		t.Fatalf("fallback resolution/result = %d calls, %d sends, %+v", resolved, fallback.calls, got)
	}
	if !got.Attempts[0].SendSucceeded || got.Attempts[0].Validated {
		t.Fatalf("validation failure must preserve send success: %+v", got.Attempts[0])
	}
}

func TestRunPreservesRouterEscalationMarker(t *testing.T) {
	primary := &fakeChannel{responses: []channel.Response{{Text: "valid"}}}
	got := Run(context.Background(), Options[string]{
		Primary: Target{Channel: primary, Escalated: true},
		Parse:   func(s string) (string, error) { return s, nil },
	})
	if len(got.Attempts) != 1 || !got.Attempts[0].Escalated {
		t.Fatalf("router escalation marker lost: %+v", got)
	}
}
