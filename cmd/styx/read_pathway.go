package main

import (
	"context"
	"errors"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
)

// readPathwayChannelAdapter is shared by one-shot, read-only agy pathways and
// their bounded Codex review. It preserves the routed model/effort and records
// every role under one correlated run id.
type readPathwayChannelAdapter struct {
	ch          channel.Channel
	tracker     *budget.Tracker
	channelName string
	model       string
	effort      string
	role        string
	projectID   string
	runID       string
	projectPath string
	extraRoots  []string
	response    channel.Response
}

// withExtraRoots attaches the exact additional repository roots required by a
// multi-root read pathway. The copy prevents caller mutation after setup.
func (a *readPathwayChannelAdapter) withExtraRoots(roots []string) *readPathwayChannelAdapter {
	if a != nil {
		a.extraRoots = append([]string(nil), roots...)
	}
	return a
}

func newReadPathwayChannelAdapter(a *app, ch channel.Channel, channelName, model, effort, role, projectID, runID, projectPath string) *readPathwayChannelAdapter {
	return &readPathwayChannelAdapter{
		ch: ch, tracker: a.tracker, channelName: channelName, model: model, effort: effort,
		role: role, projectID: projectID, runID: runID, projectPath: projectPath,
	}
}

func (a *readPathwayChannelAdapter) Send(ctx context.Context, prompt string) (string, error) {
	if a == nil || a.ch == nil {
		return "", errors.New("read pathway channel is unavailable")
	}
	resp, err := a.ch.Send(ctx, channel.Request{
		Model: a.model, Effort: a.effort, Prompt: prompt, WorkingDir: a.projectPath,
		ExtraRoots: a.extraRoots,
	})
	a.response = resp
	if a.tracker != nil {
		_ = a.tracker.Record(ctx, budget.Event{
			Channel: a.channelName, Verb: a.role, Model: a.model,
			TokensIn: resp.EstTokensIn, TokensOut: resp.EstTokensOut,
			Success: err == nil, ErrorKind: errorKindOf(err),
			Project: a.projectID, RunID: a.runID,
		})
	}
	return resp.Text, err
}
