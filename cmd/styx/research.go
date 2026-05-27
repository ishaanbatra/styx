package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
)

func cmdResearch(a *app, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx research <query>")
	}
	query := strings.Join(args, " ")
	proj, err := project.Current()
	if err != nil {
		return fmt.Errorf("identify current project: %w", err)
	}
	ctx := context.Background()

	// 1. Draft (Gemini by default)
	draftPrompt := "You are a senior technical researcher. Investigate the following thoroughly. Cover: current best practices, common pitfalls, recommended libraries/approaches, real-world tradeoffs, and concrete code patterns where applicable. Be specific and cite reasoning, not just assertions.\n\nQuery: " + query
	draftResp, draftPicked, draftErr := sendWithFallback(a, ctx,
		router.Request{Verb: "research", Args: args},
		channel.Request{Prompt: draftPrompt})
	draftText := ""
	if draftErr != nil {
		fmt.Fprintf(os.Stderr, "[styx] research draft failed (%v); critic will work from raw query\n", draftErr)
	} else {
		draftText = draftResp.Text
	}

	// 2. Critic
	var criticPrompt string
	if draftText != "" {
		criticPrompt = "You are a skeptical senior engineer. Critically review the research below. Argue against it: find holes, untested assumptions, missing context, weak evidence, edge cases that aren't addressed. Be specific. Do not rewrite the research — argue with it.\n\nRESEARCH TO CRITIQUE:\n" + draftText
	} else {
		criticPrompt = "You are a skeptical senior engineer. External research was unavailable. Analyze this query: surface key questions, hidden assumptions, likely failure modes, and what should be investigated before acting.\n\nQUERY:\n" + query
	}
	criticResp, criticPicked, criticErr := sendWithFallback(a, ctx,
		router.Request{Verb: "research.critic", Args: args},
		channel.Request{Prompt: criticPrompt})
	if criticErr != nil {
		return fmt.Errorf("research critic failed: %w", criticErr)
	}

	// 3. Compose brief
	subDir := proj.ResearchDir
	if subDir == "" {
		subDir = "styx/research"
	}
	body := composeBrief(query, draftText, criticResp.Text, draftPicked, criticPicked)
	out, err := brief.WriteBrief(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      subDir,
		Query:       query,
		Body:        body,
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(proj.Path, out)
	fmt.Printf("✓ Brief saved: %s\n", rel)
	fmt.Printf("  Draft channel:  %s:%s\n", draftPicked.Channel, draftPicked.Model)
	fmt.Printf("  Critic channel: %s:%s\n", criticPicked.Channel, criticPicked.Model)
	return nil
}

func composeBrief(query, draft, critique string, draftCM, critCM router.ChannelModel) string {
	var b strings.Builder
	b.WriteString("# Research Brief\n\n")
	fmt.Fprintf(&b, "**Query**: %s\n", query)
	fmt.Fprintf(&b, "**Date**: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "**Researcher**: %s:%s\n", draftCM.Channel, draftCM.Model)
	fmt.Fprintf(&b, "**Reviewer**:  %s:%s\n\n", critCM.Channel, critCM.Model)
	b.WriteString("---\n\n## Research\n\n")
	if draft != "" {
		b.WriteString(draft)
	} else {
		b.WriteString("_External research failed; critique below is based on the raw query._\n")
	}
	b.WriteString("\n\n---\n\n## Critical Review\n\n")
	b.WriteString(critique)
	b.WriteString("\n")
	return b.String()
}
