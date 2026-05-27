package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
)

func cmdReview(a *app, args []string) error {
	proj, err := project.Current()
	if err != nil {
		return err
	}
	diff, err := branchDiff(proj.Path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		return fmt.Errorf("no diff between current branch and main; nothing to review")
	}

	ctx := context.Background()
	dec, err := a.router.Route(ctx, router.Request{Verb: "review", Args: args})
	if err != nil {
		return err
	}
	if !dec.Parallel {
		return fmt.Errorf("review verb requires a parallel rule in routing.toml (got Channel=%s)", dec.Channel)
	}

	type result struct {
		Target router.ChannelModel
		Text   string
		Err    error
	}
	results := make([]result, len(dec.ParallelTargets))
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
			prompt := fmt.Sprintf("You are reviewing a git diff. Identify bugs, security issues, regressions, missing tests, and architectural concerns. Be specific (file:line). Group findings as BLOCKING / IMPORTANT / NIT.\n\n--- DIFF ---\n%s\n", diff)
			resp, err := ch.Send(ctx, channel.Request{Model: t.Model, Prompt: prompt})
			_ = a.tracker.Record(ctx, budget.Event{
				Channel:   t.Channel,
				Verb:      "review",
				TokensIn:  resp.EstTokensIn,
				TokensOut: resp.EstTokensOut,
				Success:   err == nil,
				ErrorKind: errorKindOf(err),
			})
			results[i] = result{Target: t, Text: resp.Text, Err: err}
		}()
	}
	wg.Wait()

	var b strings.Builder
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(&b, "## %s:%s (FAILED)\n%v\n\n", r.Target.Channel, r.Target.Model, r.Err)
			continue
		}
		fmt.Fprintf(&b, "## %s:%s\n\n%s\n\n", r.Target.Channel, r.Target.Model, r.Text)
	}

	// Synthesize
	synth, ok := a.channels[dec.SynthesizeWith.Channel]
	if !ok {
		return fmt.Errorf("synthesize channel %q not registered", dec.SynthesizeWith.Channel)
	}
	synthResp, err := synth.Send(ctx, channel.Request{
		Model:  dec.SynthesizeWith.Model,
		Prompt: "Merge the following independent reviews into a single deduplicated report grouped by severity (BLOCKING / IMPORTANT / NIT). Keep specific file:line citations.\n\n" + b.String(),
	})
	if err != nil {
		return err
	}
	fmt.Println(synthResp.Text)
	fmt.Fprintf(os.Stderr, "[styx] synthesized by %s:%s\n", dec.SynthesizeWith.Channel, dec.SynthesizeWith.Model)
	return nil
}

func branchDiff(projectPath string) (string, error) {
	cmd := exec.Command("git", "diff", "main...HEAD")
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff main...HEAD: %w", err)
	}
	return string(out), nil
}
