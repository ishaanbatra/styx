package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/router"
)

func cmdReview(a *app, args []string) error {
	proj, err := resolveGlobalTarget("")
	if err != nil {
		return err
	}
	diff, err := gitDiffBase(proj.Path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		return fmt.Errorf("no diff between current branch and default branch; nothing to review")
	}
	runID := pipeline.NewRunID("review")
	text, err := runReviewSynthesized(a, context.Background(), a.progress, runID, proj.Path, diff)
	if err != nil {
		return err
	}
	fmt.Println(text)
	return nil
}

// runReviewSynthesized runs the routed review (parallel + synthesize, or single-channel)
// and returns the synthesized text. Shared by cmdReview and auto's review stage.
// prog is used for serial narration; pass progress.Quiet() to suppress output.
func runReviewSynthesized(a *app, ctx context.Context, prog *progress.Tracker, runID, projectPath, diff string) (string, error) {
	projectID := config.ProjectID(projectPath)
	if prog == nil {
		prog = progress.Quiet()
	}
	dec, err := a.router.Route(ctx, router.Request{Verb: "review"})
	if err != nil {
		return "", err
	}
	if !dec.Parallel {
		// Single-channel fallback: use raw (undecorated) channel, narrate ourselves.
		ch, ok := a.channels[dec.Channel]
		if !ok {
			return "", fmt.Errorf("unknown review channel %q", dec.Channel)
		}
		raw := rawChannel(ch)
		st := prog.Stage("Reviewing diff (" + dec.Channel + ")")
		prompt := "Review this diff. Identify BLOCKING/IMPORTANT findings only. Be specific (file:line).\n\n--- DIFF ---\n" + diff
		resp, err := raw.Send(ctx, channel.Request{Model: dec.Model, Prompt: prompt, WorkingDir: projectPath})
		_ = a.tracker.Record(ctx, budget.Event{
			Channel:   dec.Channel,
			Verb:      "review",
			TokensIn:  resp.EstTokensIn,
			TokensOut: resp.EstTokensOut,
			Success:   err == nil,
			ErrorKind: errorKindOf(err),
			Project:   projectID,
			RunID:     runID,
		})
		if err != nil {
			st.Fail(err)
			return resp.Text, err
		}
		st.Done("done")
		return resp.Text, nil
	}

	type result struct {
		Target router.ChannelModel
		Text   string
		Err    error
	}
	results := make([]result, len(dec.ParallelTargets))

	// One stage for the entire parallel fan-out — opened BEFORE the goroutines so
	// it is never nested/concurrent with another stage on this tracker.
	st := prog.Stage(fmt.Sprintf("Reviewing diff (%d channels in parallel)", len(dec.ParallelTargets)))

	var wg sync.WaitGroup
	for i, t := range dec.ParallelTargets {
		i, t := i, t
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, ok := a.channels[t.Channel]
			if !ok {
				results[i] = result{Target: t, Err: fmt.Errorf("unknown channel %s", t.Channel)}
				return
			}
			raw := rawChannel(ch)
			prompt := fmt.Sprintf("You are reviewing a git diff. Identify bugs, security issues, regressions, missing tests, and architectural concerns. Be specific (file:line). Group findings as BLOCKING / IMPORTANT / NIT.\n\n--- DIFF ---\n%s\n", diff)
			resp, err := raw.Send(ctx, channel.Request{Model: t.Model, Prompt: prompt, WorkingDir: projectPath})
			_ = a.tracker.Record(ctx, budget.Event{
				Channel:   t.Channel,
				Verb:      "review",
				TokensIn:  resp.EstTokensIn,
				TokensOut: resp.EstTokensOut,
				Success:   err == nil,
				ErrorKind: errorKindOf(err),
				Project:   projectID,
				RunID:     runID,
			})
			results[i] = result{Target: t, Text: resp.Text, Err: err}
		}()
	}
	wg.Wait()
	st.Done("%d reviews collected", len(results))

	var b strings.Builder
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(&b, "## %s:%s (FAILED)\n%v\n\n", r.Target.Channel, r.Target.Model, r.Err)
			continue
		}
		fmt.Fprintf(&b, "## %s:%s\n\n%s\n\n", r.Target.Channel, r.Target.Model, r.Text)
	}

	synth, ok := a.channels[dec.SynthesizeWith.Channel]
	if !ok {
		return "", fmt.Errorf("synthesize channel %q not registered", dec.SynthesizeWith.Channel)
	}
	rawSynth := rawChannel(synth)
	st2 := prog.Stage("Synthesizing reviews")
	synthResp, err := rawSynth.Send(ctx, channel.Request{
		Model:      dec.SynthesizeWith.Model,
		Prompt:     "Merge the following independent reviews into a single deduplicated report grouped by severity (BLOCKING / IMPORTANT / NIT). Keep specific file:line citations.\n\n" + b.String(),
		WorkingDir: projectPath,
	})
	if err != nil {
		st2.Fail(err)
		return "", err
	}
	st2.Done("synthesized by %s:%s", dec.SynthesizeWith.Channel, dec.SynthesizeWith.Model)
	return synthResp.Text, nil
}
