package execute

import "context"

// FixLoopOptions configures a fix-loop.
type FixLoopOptions struct {
	MaxAttempts int

	// Verify returns (ok=true) when the situation is resolved.
	// The second return is the diagnostic output to feed to the fixer.
	Verify func(ctx context.Context) (ok bool, output string)

	// Fix is asked to repair the problem. attempt is 1-indexed.
	Fix func(ctx context.Context, problem string, attempt int) error
}

// FixLoopResult reports loop outcome.
type FixLoopResult struct {
	Fixed      bool
	Attempts   int
	LastOutput string
	LastFixErr error
}

// FixLoop repeatedly: verify, and if not ok, fix. Returns when verify succeeds
// or MaxAttempts is reached. Calls Verify before Fix on each iteration.
func FixLoop(ctx context.Context, o FixLoopOptions) (FixLoopResult, error) {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 5
	}
	res := FixLoopResult{}
	for attempt := 1; attempt <= o.MaxAttempts; attempt++ {
		res.Attempts = attempt
		ok, out := o.Verify(ctx)
		res.LastOutput = out
		if ok {
			res.Fixed = true
			return res, nil
		}
		if err := o.Fix(ctx, out, attempt); err != nil {
			res.LastFixErr = err
			// Continue trying; a single bad fix shouldn't abort.
		}
	}
	// One final verify after the last fix attempt.
	ok, out := o.Verify(ctx)
	res.LastOutput = out
	if ok {
		res.Fixed = true
	}
	return res, nil
}
