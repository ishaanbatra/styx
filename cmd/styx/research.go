package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/research"
	"github.com/ishaanbatra/styx/internal/router"
)

func cmdResearch(a *app, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx research [--deep] <query>")
	}
	deep := false
	queryParts := []string{}
	for _, arg := range args {
		if arg == "--deep" {
			deep = true
			continue
		}
		queryParts = append(queryParts, arg)
	}
	if len(queryParts) == 0 {
		return fmt.Errorf("usage: styx research [--deep] <query>")
	}
	query := strings.Join(queryParts, " ")

	proj, err := project.Current()
	if err != nil {
		return err
	}

	// Resolve drafter (verb=research) and critic (verb=research.critic) channels via router.
	drafterDec, err := a.router.Route(context.Background(), router.Request{Verb: "research", Args: queryParts})
	if err != nil {
		return fmt.Errorf("route research: %w", err)
	}
	criticDec, err := a.router.Route(context.Background(), router.Request{Verb: "research.critic", Args: queryParts})
	if err != nil {
		return fmt.Errorf("route research.critic: %w", err)
	}
	drafterCh, ok := a.channels[drafterDec.Channel]
	if !ok {
		return fmt.Errorf("unknown drafter channel %q", drafterDec.Channel)
	}
	criticCh, ok := a.channels[criticDec.Channel]
	if !ok {
		return fmt.Errorf("unknown critic channel %q", criticDec.Channel)
	}

	drafter := &channelAdapter{ch: rawChannel(drafterCh), model: drafterDec.Model, projectPath: proj.Path}
	critic := &channelAdapter{ch: rawChannel(criticCh), model: criticDec.Model}

	fmt.Fprintf(os.Stderr, "[styx] research: drafter=%s:%s critic=%s:%s%s\n",
		drafterDec.Channel, drafterDec.Model, criticDec.Channel, criticDec.Model,
		mapStr(deep, " (--deep)"))

	b, err := research.Loop(context.Background(), query, drafter, critic, a.progress)
	if err != nil {
		return fmt.Errorf("convergence loop: %w", err)
	}
	b.DrafterChannel = drafterDec.Channel + ":" + drafterDec.Model
	b.CriticChannel = criticDec.Channel + ":" + criticDec.Model

	if deep {
		body := ""
		if len(b.Drafts) > 0 {
			body = b.Drafts[len(b.Drafts)-1]
		}
		urls := research.ExtractURLs(body)
		if len(urls) > 0 {
			fmt.Fprintf(os.Stderr, "[styx] chasing %d source URLs...\n", len(urls))
			summarizer := research.AgySummarizer(drafter)
			sources, _ := research.ChaseSources(context.Background(), urls, summarizer)
			b.Sources = sources
		}
	}

	subDir := proj.ResearchDir
	if subDir == "" {
		subDir = "styx/research"
	}
	out, err := brief.WriteBrief(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      subDir,
		Query:       query,
		Body:        research.RenderBrief(b),
		Now:         b.StartedAt,
	})
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(proj.Path, out)
	fmt.Printf("✓ Brief saved: %s\n", rel)
	fmt.Printf("  Status: %s (%d rounds)\n", b.Status, len(b.Critiques))
	return nil
}

// channelAdapter bridges channel.Channel into research.Channel.
type channelAdapter struct {
	ch          channel.Channel
	model       string
	projectPath string // only used for drafter to enable agy's --add-dir
}

func (a *channelAdapter) Send(ctx context.Context, prompt string) (string, error) {
	resp, err := a.ch.Send(ctx, channel.Request{
		Model:      a.model,
		Prompt:     prompt,
		WorkingDir: a.projectPath,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func mapStr(b bool, s string) string {
	if b {
		return s
	}
	return ""
}
