package channel

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeChannel is a test double used to verify the interface contract.
type fakeChannel struct {
	name     string
	sendErr  error
	respText string
	budget   Budget
	budErr   error
	sleep    time.Duration
}

func (f *fakeChannel) Name() string { return f.name }
func (f *fakeChannel) Send(ctx context.Context, req Request) (Response, error) {
	if f.sleep > 0 {
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(f.sleep):
		}
	}
	if f.sendErr != nil {
		return Response{}, f.sendErr
	}
	return Response{Text: f.respText, EstTokensIn: 10, EstTokensOut: 20}, nil
}
func (f *fakeChannel) BudgetState(ctx context.Context) (Budget, error) {
	return f.budget, f.budErr
}

func TestContract_NameNonEmpty(t *testing.T) {
	c := &fakeChannel{name: "fake"}
	if c.Name() == "" {
		t.Error("Name() returned empty string")
	}
}

func TestContract_SendReturnsResponse(t *testing.T) {
	c := &fakeChannel{name: "fake", respText: "hello"}
	resp, err := c.Send(context.Background(), Request{Prompt: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" {
		t.Errorf("Text = %q, want hello", resp.Text)
	}
}

func TestContract_SendHonorsContextCancel(t *testing.T) {
	c := &fakeChannel{name: "fake", sleep: 500 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := c.Send(ctx, Request{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestContract_BudgetReachable(t *testing.T) {
	c := &fakeChannel{name: "fake", budget: Budget{UsedPct: 12.5}}
	got, err := c.BudgetState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedPct != 12.5 {
		t.Errorf("UsedPct = %.2f, want 12.5", got.UsedPct)
	}
}
