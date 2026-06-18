package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

// cmdOneShot handles every verb whose pipeline is:
//  1. Read args + maybe file contents
//  2. Resolve current project (best-effort; ok if not in repo)
//  3. Route -> channel -> print to stdout
//  4. Record usage
func cmdOneShot(a *app, verb string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx %s <prompt|file...>", verb)
	}
	prompt, attachments, err := loadPromptAndAttachments(verb, args)
	if err != nil {
		return err
	}

	proj, _ := resolveGlobalTarget("") // ok if not in a repo
	sigs := signals.Extract(verb, args, proj)

	// Large-context signal for explain/summarize: rough estimate by combined attachment size.
	if verb == "explain" || verb == "summarize" {
		if totalAttachmentBytes(attachments) > 200_000 {
			sigs = appendUnique(sigs, "large_context")
		}
	}

	req := router.Request{Verb: verb, Args: args, Signals: sigs}
	resp, picked, err := sendWithFallback(a, context.Background(), req, channel.Request{
		Prompt:      prompt,
		Attachments: attachments,
	}, false)
	if err != nil {
		return err
	}
	fmt.Print(resp.Text)
	if !strings.HasSuffix(resp.Text, "\n") {
		fmt.Println()
	}
	logStatus("channel=%s:%s", picked.Channel, picked.Model)
	return nil
}

// sendWithFallback walks the router's primary + fallback chain, recording
// usage at each attempt. Returns the successful response + the channel that produced it.
// raw, when true, unwraps each channel's progress decorator before sending —
// used by orchestration callers (e.g. the auto pipeline) that narrate at their
// own level and must not open a competing progress stage.
func sendWithFallback(a *app, ctx context.Context, req router.Request, cr channel.Request, raw bool) (channel.Response, router.ChannelModel, error) {
	dec, err := a.router.Route(ctx, req)
	if err != nil {
		return channel.Response{}, router.ChannelModel{}, err
	}
	attempts := []router.ChannelModel{{Channel: dec.Channel, Model: dec.Model}}
	attempts = append(attempts, dec.Fallback...)
	var lastErr error
	for _, t := range attempts {
		ch, ok := a.channels[t.Channel]
		if !ok {
			lastErr = fmt.Errorf("unknown channel %q in routing", t.Channel)
			continue
		}
		if raw {
			ch = rawChannel(ch)
		}
		cr.Model = t.Model
		resp, err := ch.Send(ctx, cr)
		_ = a.tracker.Record(ctx, budget.Event{
			Channel:   t.Channel,
			Verb:      req.Verb,
			TokensIn:  resp.EstTokensIn,
			TokensOut: resp.EstTokensOut,
			Success:   err == nil,
			ErrorKind: errorKindOf(err),
		})
		if err == nil {
			return resp, t, nil
		}
		logStatus("%s failed (%v); falling back", t.Channel, err)
		lastErr = err
	}
	return channel.Response{}, router.ChannelModel{}, fmt.Errorf("all channels failed; last err: %w", lastErr)
}

func errorKindOf(err error) string {
	if err == nil {
		return ""
	}
	if ce, ok := err.(*channel.ClassifiedError); ok {
		return string(ce.Kind)
	}
	return "other"
}

func loadPromptAndAttachments(verb string, args []string) (string, []channel.Attachment, error) {
	// For "explain" and "summarize", treat args as file paths that exist.
	// Otherwise treat the joined args as a prompt.
	if verb == "explain" || verb == "summarize" {
		var atts []channel.Attachment
		for _, p := range args {
			if _, err := os.Stat(p); err != nil {
				return "", nil, fmt.Errorf("file not found: %s", p)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", nil, err
			}
			atts = append(atts, channel.Attachment{Path: p})
			_ = b // attachments carry path; channels inline content
		}
		prompt := promptForVerb(verb, args)
		return prompt, atts, nil
	}
	return strings.Join(args, " "), nil, nil
}

func promptForVerb(verb string, args []string) string {
	switch verb {
	case "explain":
		return "Explain the following code clearly. Cover: what it does, why it exists, key control flow, and any subtle behaviors. Files: " + strings.Join(args, ", ")
	case "summarize":
		return "Summarize the following files. Identify their purpose, key responsibilities, and how they relate. Files: " + strings.Join(args, ", ")
	case "critique":
		return "Act as a skeptical senior engineer. Argue against the following. Find holes, untested assumptions, missing context, weak evidence, edge cases that aren't addressed. Be specific.\n\n" + strings.Join(args, " ")
	case "think":
		return "Think step by step through the problem below. Do not write code unless explicitly asked. Focus on tradeoffs, hidden assumptions, edge cases, and design implications.\n\nProblem: " + strings.Join(args, " ")
	}
	return strings.Join(args, " ")
}

func totalAttachmentBytes(atts []channel.Attachment) int {
	total := 0
	for _, a := range atts {
		if fi, err := os.Stat(a.Path); err == nil {
			total += int(fi.Size())
		}
	}
	return total
}

func appendUnique(ss []string, s string) []string {
	for _, x := range ss {
		if x == s {
			return ss
		}
	}
	return append(ss, s)
}
