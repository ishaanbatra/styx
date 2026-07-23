package channel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/memguard"
	"github.com/ishaanbatra/styx/internal/progress"
)

// fakeInner is a minimal Channel double used only in decorator tests.
type fakeInner struct {
	name      string
	sendErr   error
	respText  string
	lastReq   Request
	callCount int
}

func (f *fakeInner) Name() string { return f.name }

func (f *fakeInner) Send(_ context.Context, req Request) (Response, error) {
	f.callCount++
	f.lastReq = req
	if f.sendErr != nil {
		return Response{}, f.sendErr
	}
	return Response{Text: f.respText, EstTokensIn: 5, EstTokensOut: 10}, nil
}

func (f *fakeInner) BudgetState(_ context.Context) (Budget, error) {
	return Budget{UsedPct: 42.0}, nil
}

// TestWithProgress_NarratesSendAndForwards verifies that a non-interactive Send:
//
//	(a) forwards the exact request to the inner channel,
//	(b) returns the inner channel's Response unchanged,
//	(c) the buffer contains a start line and a done line mentioning the label.
func TestWithProgress_NarratesSendAndForwards(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, true) // verbose so Info shows too

	inner := &fakeInner{name: "mock", respText: "hello world"}
	deco := &WithProgress{Inner: inner, Tracker: tr, Label: "testlabel"}

	req := Request{Prompt: "say hello", Model: "test-model"}
	resp, err := deco.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// (a) inner received the exact request
	if inner.callCount != 1 {
		t.Errorf("inner.callCount = %d, want 1", inner.callCount)
	}
	if inner.lastReq.Prompt != req.Prompt {
		t.Errorf("inner.lastReq.Prompt = %q, want %q", inner.lastReq.Prompt, req.Prompt)
	}
	if inner.lastReq.Model != req.Model {
		t.Errorf("inner.lastReq.Model = %q, want %q", inner.lastReq.Model, req.Model)
	}

	// (b) returned Response is the inner's response
	if resp.Text != "hello world" {
		t.Errorf("resp.Text = %q, want %q", resp.Text, "hello world")
	}
	if resp.EstTokensIn != 5 || resp.EstTokensOut != 10 {
		t.Errorf("resp tokens = (%d, %d), want (5, 10)", resp.EstTokensIn, resp.EstTokensOut)
	}

	// (c) buffer contains a start line and a done line mentioning the label
	out := buf.String()
	if !strings.Contains(out, "testlabel") {
		t.Errorf("output does not contain label %q:\n%s", "testlabel", out)
	}
	if !strings.Contains(out, "started") {
		t.Errorf("output does not contain 'started':\n%s", out)
	}
	// Done line mentions returned tokens
	if !strings.Contains(out, "returned") {
		t.Errorf("output does not contain 'returned':\n%s", out)
	}
}

// TestWithProgress_Interactive_SkipsProgress verifies that for an interactive
// request the buffer stays empty but the inner channel is still called.
func TestWithProgress_Interactive_SkipsProgress(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, true)

	inner := &fakeInner{name: "mock", respText: ""}
	deco := &WithProgress{Inner: inner, Tracker: tr, Label: "testlabel"}

	req := Request{Prompt: "build something", Interactive: true}
	_, err := deco.Send(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// inner was still called
	if inner.callCount != 1 {
		t.Errorf("inner.callCount = %d, want 1", inner.callCount)
	}

	// buffer is empty — no progress output for interactive requests
	if buf.Len() != 0 {
		t.Errorf("expected empty buffer for interactive request, got:\n%s", buf.String())
	}
}

// TestWithProgress_ForwardsFailure verifies that when the inner channel returns
// an error, Send returns that error and the buffer contains "failed".
func TestWithProgress_ForwardsFailure(t *testing.T) {
	var buf bytes.Buffer
	tr := progress.New(&buf, false, true)

	sendErr := errors.New("model unavailable")
	inner := &fakeInner{name: "mock", sendErr: sendErr}
	deco := &WithProgress{Inner: inner, Tracker: tr, Label: "testlabel"}

	_, err := deco.Send(context.Background(), Request{Prompt: "do something"})
	if !errors.Is(err, sendErr) {
		t.Errorf("expected sendErr, got %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "failed") {
		t.Errorf("output does not contain 'failed':\n%s", out)
	}
}

// TestWithProgress_NilTracker_Safe verifies that a nil Tracker does not panic;
// the inner channel is still called and the response is returned.
func TestWithProgress_NilTracker_Safe(t *testing.T) {
	inner := &fakeInner{name: "mock", respText: "ok"}
	deco := &WithProgress{Inner: inner, Tracker: nil, Label: "testlabel"}

	resp, err := deco.Send(context.Background(), Request{Prompt: "ping"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Text != "ok" {
		t.Errorf("resp.Text = %q, want %q", resp.Text, "ok")
	}
	if inner.callCount != 1 {
		t.Errorf("inner.callCount = %d, want 1", inner.callCount)
	}
}

// TestWithProgress_Name_ForwardsToInner verifies Name() delegates to Inner.
func TestWithProgress_Name_ForwardsToInner(t *testing.T) {
	inner := &fakeInner{name: "my-channel"}
	deco := &WithProgress{Inner: inner, Tracker: nil, Label: "lbl"}
	if got := deco.Name(); got != "my-channel" {
		t.Errorf("Name() = %q, want %q", got, "my-channel")
	}
}

// TestWithProgress_BudgetState_ForwardsToInner verifies BudgetState delegates to Inner.
func TestWithProgress_BudgetState_ForwardsToInner(t *testing.T) {
	inner := &fakeInner{name: "mock"}
	deco := &WithProgress{Inner: inner, Tracker: nil, Label: "lbl"}
	b, err := deco.BudgetState(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.UsedPct != 42.0 {
		t.Errorf("UsedPct = %.1f, want 42.0", b.UsedPct)
	}
}

// slowChannel blocks until its context is cancelled.
type slowChannel struct{}

func (slowChannel) Name() string { return "slow" }
func (slowChannel) BudgetState(context.Context) (Budget, error) {
	return Budget{}, nil
}
func (slowChannel) Send(ctx context.Context, _ Request) (Response, error) {
	<-ctx.Done()
	return Response{}, ctx.Err()
}

func TestWithTimeoutCancelsSlowSend(t *testing.T) {
	w := &WithTimeout{Inner: slowChannel{}, D: 50 * time.Millisecond}
	start := time.Now()
	_, err := w.Send(context.Background(), Request{Prompt: "x"})
	if err == nil {
		t.Fatal("want timeout error, got nil")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout did not fire promptly")
	}
}

func TestWithTimeoutSkipsInteractive(t *testing.T) {
	// Interactive sends hand over the terminal; they must never be timed out.
	w := &WithTimeout{Inner: &fakeOK{}, D: time.Nanosecond}
	if _, err := w.Send(context.Background(), Request{Interactive: true}); err != nil {
		t.Fatalf("interactive send timed out: %v", err)
	}
}

func TestWithMemoryGuard(t *testing.T) {
	tests := []struct {
		name        string
		level       memguard.Level
		interactive bool
		wantCalls   int
		wantKind    ErrorKindLabel
	}{
		{name: "critical refuses", level: memguard.Critical, wantCalls: 0, wantKind: ErrKindMemPressure},
		{name: "warn passes through", level: memguard.Warn, wantCalls: 1},
		{name: "normal passes through", level: memguard.Normal, wantCalls: 1},
		{name: "interactive critical passes through", level: memguard.Critical, interactive: true, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeInner{name: "mock", respText: "ok"}
			w := &WithMemoryGuard{
				Inner: inner,
				Level: func() memguard.Level { return tt.level },
			}
			resp, err := w.Send(context.Background(), Request{Interactive: tt.interactive})
			if inner.callCount != tt.wantCalls {
				t.Fatalf("inner call count = %d, want %d", inner.callCount, tt.wantCalls)
			}
			if tt.wantKind == "" {
				if err != nil {
					t.Fatalf("Send() error = %v, want nil", err)
				}
				if resp.Text != "ok" {
					t.Errorf("response text = %q, want ok", resp.Text)
				}
				return
			}
			var classified *ClassifiedError
			if !errors.As(err, &classified) {
				t.Fatalf("Send() error = %T %v, want *ClassifiedError", err, err)
			}
			if classified.Kind != tt.wantKind {
				t.Errorf("error kind = %q, want %q", classified.Kind, tt.wantKind)
			}
			for _, want := range []string{"host under memory pressure", "close apps or retry", "refusing to launch mock", "jetsam kill"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

// fakeOK returns immediately.
type fakeOK struct{}

func (*fakeOK) Name() string { return "ok" }
func (*fakeOK) BudgetState(context.Context) (Budget, error) {
	return Budget{}, nil
}
func (*fakeOK) Send(context.Context, Request) (Response, error) {
	return Response{Text: "ok"}, nil
}
