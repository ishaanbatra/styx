package main

import (
	"context"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

type recordingChannel struct{ last channel.Request }

func (r *recordingChannel) Name() string { return "rec" }

func (r *recordingChannel) BudgetState(context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

func (r *recordingChannel) Send(_ context.Context, req channel.Request) (channel.Response, error) {
	r.last = req
	return channel.Response{Text: "ok"}, nil
}

func TestChannelAdapter_PassesEffort(t *testing.T) {
	rec := &recordingChannel{}
	a := &channelAdapter{ch: rec, model: "", effort: "high"}
	if _, err := a.Send(context.Background(), "draft this"); err != nil {
		t.Fatal(err)
	}
	if rec.last.Effort != "high" {
		t.Errorf("Effort = %q, want high", rec.last.Effort)
	}
}
