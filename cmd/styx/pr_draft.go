package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/microtask"
	"github.com/ishaanbatra/styx/internal/pipeline"
	"github.com/ishaanbatra/styx/internal/prdraft"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

// pullRequestDraft is the ship-ready result of the two independent bounded
// prose tasks. Publication remains execute.Ship's responsibility.
type pullRequestDraft struct {
	Title       string
	Body        string
	Draft       bool
	Labels      []string
	TitleStatic bool
	BodyStatic  bool
}

func draftPullRequest(ctx context.Context, a *app, proj project.Project, state *pipeline.State) pullRequestDraft {
	packet, err := prdraft.BuildContext(ctx, proj.Path, state)
	if err != nil {
		logStatus("PR context unavailable; using deterministic draft: %v", err)
		packet, err = prdraft.ContextFromState(state)
		if err != nil {
			packet = prdraft.Context{Goal: stateGoal(state), Branch: stateBranch(state)}
		}
		// Missing git evidence is uncertainty, so fail safe to a draft PR.
		packet.DraftRequired = true
		draft := staticPullRequestDraft(packet)
		titleSignals := prDraftSignals("pr.title", packet, proj)
		recordPRMicrotask(ctx, a, proj.ID, stateRunID(state), "pr.title", titleSignals,
			microtask.Result[prdraft.TitleDraft]{Value: prdraft.StaticTitle(packet), StaticFallback: true})
		bodySignals := prDraftSignals("pr.body", packet, proj)
		recordPRMicrotask(ctx, a, proj.ID, stateRunID(state), "pr.body", bodySignals,
			microtask.Result[prdraft.BodyDraft]{Value: prdraft.StaticBody(packet), StaticFallback: true})
		return draft
	}

	titleSignals := prDraftSignals("pr.title", packet, proj)
	titleResult := runPRMicrotask(ctx, a, proj.Path, "pr.title", titleSignals,
		prdraft.TitlePrompt(packet), prdraft.ParseTitle,
		func(d prdraft.TitleDraft) error { return prdraft.ValidateTitle(packet, d) },
		prdraft.StaticTitle(packet))
	recordPRMicrotask(ctx, a, proj.ID, stateRunID(state), "pr.title", titleSignals, titleResult)

	bodySignals := prDraftSignals("pr.body", packet, proj)
	bodyResult := runPRMicrotask(ctx, a, proj.Path, "pr.body", bodySignals,
		prdraft.BodyPrompt(packet), prdraft.ParseBody,
		func(d prdraft.BodyDraft) error { return prdraft.ValidateBody(packet, d) },
		prdraft.StaticBody(packet))
	recordPRMicrotask(ctx, a, proj.ID, stateRunID(state), "pr.body", bodySignals, bodyResult)

	return pullRequestDraft{
		Title: titleResult.Value.Title, Body: prdraft.RenderBody(packet, bodyResult.Value),
		Draft: packet.DraftRequired, Labels: prdraft.Labels(packet, bodyResult.Value),
		TitleStatic: titleResult.StaticFallback, BodyStatic: bodyResult.StaticFallback,
	}
}

func prDraftSignals(verb string, packet prdraft.Context, proj project.Project) []string {
	result := signals.Extract(verb, []string{packet.Goal}, proj)
	if !prdraft.RequiresCapableModel(packet) {
		return result
	}
	for _, signal := range result {
		if signal == signals.SigComplex {
			return result
		}
	}
	return append(result, signals.SigComplex)
}

func staticPullRequestDraft(packet prdraft.Context) pullRequestDraft {
	title := prdraft.StaticTitle(packet)
	body := prdraft.StaticBody(packet)
	return pullRequestDraft{
		Title: title.Title, Body: prdraft.RenderBody(packet, body),
		Draft: packet.DraftRequired, Labels: prdraft.Labels(packet, body),
		TitleStatic: true, BodyStatic: true,
	}
}

func runPRMicrotask[T any](ctx context.Context, a *app, projectPath, verb string, sigs []string, prompt string,
	parse func(string) (T, error), validate func(T) error, static T,
) microtask.Result[T] {
	primary, fallback, err := prMicrotaskTargets(ctx, a, verb, sigs)
	if err != nil {
		logStatus("%s routing unavailable; using deterministic draft: %v", verb, err)
		return microtask.Result[T]{Value: static, StaticFallback: true}
	}
	result := microtask.Run(ctx, microtask.Options[T]{
		Prompt: prompt, WorkingDir: projectPath, Primary: primary, Fallback: fallback,
		Parse: parse, Validate: validate, Static: static,
	})
	for _, attempt := range result.Attempts {
		if attempt.Validated {
			continue
		}
		detail := attempt.Error
		if attempt.ValidationError != "" {
			detail = attempt.ValidationError
		}
		logStatus("%s attempt %s rejected: %s", verb, microtask.TargetString(microtask.Target{Name: attempt.Channel, Model: attempt.Model}), detail)
	}
	if result.StaticFallback {
		logStatus("%s model attempts exhausted; using deterministic draft", verb)
	}
	return result
}

func prMicrotaskTargets(ctx context.Context, a *app, verb string, sigs []string) (microtask.Target, func(context.Context) *microtask.Target, error) {
	if a == nil || a.router == nil {
		return microtask.Target{}, nil, fmt.Errorf("router is unavailable")
	}
	decision, err := a.router.Route(ctx, router.Request{Verb: verb, Signals: sigs})
	if err != nil {
		return microtask.Target{}, nil, err
	}
	if decision.BlockedByBudget {
		return microtask.Target{}, nil, fmt.Errorf("all routed targets are unavailable")
	}
	primary := microtask.Target{
		Channel: a.channels[decision.Channel], Name: decision.Channel,
		Model: decision.Model, Effort: decision.Effort, Escalated: decision.Degraded,
	}

	fallback := func(ctx context.Context) *microtask.Target {
		target, ok := a.router.NextAvailableFallback(ctx, decision)
		if !ok {
			return nil
		}
		return &microtask.Target{
			Channel: a.channels[target.Channel], Name: target.Channel,
			Model: target.Model, Effort: decision.Effort, Escalated: true,
		}
	}
	return primary, fallback, nil
}

func recordPRMicrotask[T any](ctx context.Context, a *app, projectID, runID, verb string, sigs []string, result microtask.Result[T]) {
	if a == nil || a.tracker == nil {
		return
	}
	sortedSignals := append([]string(nil), sigs...)
	sort.Strings(sortedSignals)
	ref := prMicrotaskRef(runID, verb)
	for _, attempt := range result.Attempts {
		validation := "valid"
		if attempt.ValidationError != "" {
			validation = "invalid"
		} else if attempt.Error != "" {
			validation = "not_run"
		}
		usageErrorKind := ""
		if !attempt.SendSucceeded {
			usageErrorKind = attempt.ErrorKind
		}
		event := budget.Event{
			Channel: attempt.Channel, Verb: verb, Model: attempt.Model,
			TokensIn: attempt.TokensIn, TokensOut: attempt.TokensOut,
			Success: attempt.SendSucceeded, ErrorKind: usageErrorKind,
			Project: projectID, RunID: runID,
		}
		if err := a.tracker.Record(ctx, event); err != nil {
			logStatus("record %s usage failed: %v", verb, err)
		}
		note := fmt.Sprintf("verb=%s;validation=%s;escalated=%t;static_fallback=false", verb, validation, attempt.Escalated)
		signalText := prMicrotaskSignals(sortedSignals, verb, attempt.Escalated, false)
		if err := a.tracker.RecordOutcome(ctx, budget.Outcome{
			Project: projectID, TaskID: ref, CLI: attempt.Channel, Model: attempt.Model,
			Signals: signalText, Risk: "read", DurationS: attempt.Duration.Seconds(),
			TokensIn: attempt.TokensIn, TokensOut: attempt.TokensOut,
			ErrorKind: attempt.ErrorKind, Note: note,
		}); err != nil {
			logStatus("record %s outcome failed: %v", verb, err)
		}
	}
	if result.StaticFallback {
		escalated := false
		for _, attempt := range result.Attempts {
			escalated = escalated || attempt.Escalated
		}
		if err := a.tracker.RecordOutcome(ctx, budget.Outcome{
			Project: projectID, TaskID: ref, CLI: "static", Model: "deterministic",
			Signals: prMicrotaskSignals(sortedSignals, verb, escalated, true), Risk: "read",
			Note: fmt.Sprintf("verb=%s;validation=static;escalated=%t;static_fallback=true", verb, escalated),
		}); err != nil {
			logStatus("record %s static outcome failed: %v", verb, err)
		}
	}
}

func prMicrotaskSignals(base []string, verb string, escalated, staticFallback bool) string {
	set := make(map[string]bool, len(base)+3)
	for _, signal := range base {
		set[signal] = true
	}
	set["verb:"+verb] = true
	if escalated {
		set["escalated"] = true
	}
	if staticFallback {
		set["static-fallback"] = true
	}
	all := make([]string, 0, len(set))
	for signal := range set {
		all = append(all, signal)
	}
	sort.Strings(all)
	return strings.Join(all, ",")
}

func prMicrotaskRef(runID, verb string) string {
	if runID == "" {
		return ""
	}
	return runID + ":" + verb
}

func stateGoal(state *pipeline.State) string {
	if state == nil {
		return ""
	}
	return state.Goal
}

func stateBranch(state *pipeline.State) string {
	if state == nil {
		return ""
	}
	return state.Branch
}

func stateRunID(state *pipeline.State) string {
	if state == nil {
		return ""
	}
	return state.RunID
}
