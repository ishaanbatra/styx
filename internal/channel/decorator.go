package channel

import (
	"context"
	"time"

	"github.com/ishaanbatra/styx/internal/progress"
)

// WithProgress decorates a Channel so each Send is narrated via a progress.Stage.
// It forwards Name and BudgetState unchanged.
type WithProgress struct {
	Inner   Channel
	Tracker *progress.Tracker
	Label   string // channel label used in the stage name, e.g. "claude"
}

func (w *WithProgress) Name() string { return w.Inner.Name() }

func (w *WithProgress) BudgetState(ctx context.Context) (Budget, error) {
	return w.Inner.BudgetState(ctx)
}

func (w *WithProgress) Send(ctx context.Context, req Request) (Response, error) {
	// Interactive requests take over the terminal (build verb); a spinner would
	// fight the child process for the TTY, so skip progress entirely.
	if req.Interactive {
		return w.Inner.Send(ctx, req)
	}

	// Guard against a nil Tracker so the decorator is safe even if a caller
	// forgets to set it.
	if w.Tracker == nil {
		return w.Inner.Send(ctx, req)
	}

	s := w.Tracker.Stage(w.Label + " request")
	if req.Prompt != "" {
		s.Info("prompt: ~%d tokens", len(req.Prompt)/4)
	}
	resp, err := w.Inner.Send(ctx, req)
	if err != nil {
		s.Fail(err)
		return resp, err
	}
	s.Done("returned ~%d tokens", len(resp.Text)/4)
	return resp, nil
}

// WithTimeout decorates a Channel so every non-interactive Send gets a
// deadline. Interactive sends hand the terminal to the child process and are
// never timed out.
type WithTimeout struct {
	Inner Channel
	D     time.Duration
}

func (w *WithTimeout) Name() string { return w.Inner.Name() }

func (w *WithTimeout) BudgetState(ctx context.Context) (Budget, error) {
	return w.Inner.BudgetState(ctx)
}

func (w *WithTimeout) Send(ctx context.Context, req Request) (Response, error) {
	if w.D <= 0 || req.Interactive {
		return w.Inner.Send(ctx, req)
	}
	ctx, cancel := context.WithTimeout(ctx, w.D)
	defer cancel()
	return w.Inner.Send(ctx, req)
}
