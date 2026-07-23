package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/progress"
)

// MaxRounds caps the convergence loop. After this many critic passes, status is max_rounds_exhausted.
const MaxRounds = 6

// Channel is the narrow interface the loop needs.
// Production wires through internal/channel via thin adapters; tests use fakeChan.
type Channel interface {
	Send(ctx context.Context, prompt string) (string, error)
}

// Brief is the persistent output of a research run.
type Brief struct {
	Query          string     `json:"query"`
	Status         string     `json:"status"` // converged | oscillating | max_rounds_exhausted
	Drafts         []string   `json:"drafts"`
	Critiques      []Critique `json:"critiques"`
	DrafterChannel string     `json:"drafter_channel"`
	CriticChannel  string     `json:"critic_channel"`
	StartedAt      time.Time  `json:"started_at"`
	EndedAt        time.Time  `json:"ended_at"`
	Sources        []Source   `json:"sources,omitempty"` // populated by --deep
}

// Source is a citation-chase result attached to a brief.
type Source struct {
	URL     string `json:"url"`
	Summary string `json:"summary"`
}

// Loop runs the convergence loop. drafter produces drafts; critic produces critiques.
// prog receives narration of each round; pass nil or progress.Quiet() for silence.
//
// Stop conditions (in order):
//  1. critic.Converged() (no blocking + no important findings) -> "converged"
//  2. draft[N] hash equals draft[N-2] hash after N >= 3 -> "oscillating"
//  3. MaxRounds critic passes reached -> "max_rounds_exhausted"
func Loop(ctx context.Context, query string, drafter, critic Channel, prog *progress.Tracker) (*Brief, error) {
	if prog == nil {
		prog = progress.Quiet()
	}
	b := &Brief{
		Query:     query,
		StartedAt: time.Now().UTC(),
	}
	// Initial draft.
	stDraft := prog.Stage(fmt.Sprintf("Round 1/%d: drafting initial response", MaxRounds))
	d0, err := drafter.Send(ctx, draftPrompt(query))
	if err != nil {
		stDraft.Fail(err)
		return nil, fmt.Errorf("initial draft: %w", err)
	}
	stDraft.Done("done")
	b.Drafts = append(b.Drafts, d0)

	for round := 1; round <= MaxRounds; round++ {
		currentDraft := b.Drafts[len(b.Drafts)-1]

		stCrit := prog.Stage(fmt.Sprintf("Round %d/%d: critiquing draft", round, MaxRounds))
		critRaw, err := critic.Send(ctx, critiquePrompt(currentDraft))
		if err != nil {
			stCrit.Fail(err)
			return nil, fmt.Errorf("round %d critique: %w", round, err)
		}
		c, err := Parse(critRaw)
		if errors.Is(err, ErrDegraded) {
			stCrit.Info("critique parse degraded: %v (raw text treated as one IMPORTANT finding)", err)
		} else if err != nil {
			// Preserve the loop's existing fail-safe behavior for any future
			// parse errors: report them and continue with the returned critique.
			stCrit.Info("critique parse error: %v", err)
		}
		b.Critiques = append(b.Critiques, c)

		critSummary := fmt.Sprintf("%d BLOCKING, %d IMPORTANT, %d NITs", len(c.Blocking), len(c.Important), len(c.Nits))
		if c.Converged() {
			stCrit.Done("%s -> converged", critSummary)
			b.Status = "converged"
			b.EndedAt = time.Now().UTC()
			return b, nil
		}

		// Oscillation check: if the next revise would match draft[round-2] from
		// two rounds back, we're cycling. We check by comparing the latest draft
		// hash to the one two back BEFORE generating the next draft.
		// (Equivalent to checking after generation; this saves a model call.)
		if len(b.Drafts) >= 3 {
			if hash(b.Drafts[len(b.Drafts)-1]) == hash(b.Drafts[len(b.Drafts)-3]) {
				stCrit.Done("%s -> oscillating", critSummary)
				b.Status = "oscillating"
				b.EndedAt = time.Now().UTC()
				return b, nil
			}
		}

		stCrit.Done("%s", critSummary)

		stRevise := prog.Stage(fmt.Sprintf("Round %d/%d: revising draft", round, MaxRounds))
		newDraft, err := drafter.Send(ctx, revisePrompt(currentDraft, c))
		if err != nil {
			stRevise.Fail(err)
			return nil, fmt.Errorf("round %d revise: %w", round, err)
		}
		stRevise.Done("done")
		b.Drafts = append(b.Drafts, newDraft)
	}
	b.Status = "max_rounds_exhausted"
	b.EndedAt = time.Now().UTC()
	return b, nil
}

// RenderBrief produces the markdown that gets written under <project>/<research-dir>/.
func RenderBrief(b *Brief) string {
	var w strings.Builder
	fmt.Fprintf(&w, "# Research Brief\n\n")
	fmt.Fprintf(&w, "**Query:** %s\n", b.Query)
	fmt.Fprintf(&w, "**Date:** %s\n", b.StartedAt.Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&w, "**Status:** %s (after %d rounds)\n", b.Status, len(b.Critiques))
	fmt.Fprintf(&w, "**Channels:** drafter=%s, critic=%s\n\n", b.DrafterChannel, b.CriticChannel)
	w.WriteString("---\n\n## Final Synthesis\n\n")
	if len(b.Drafts) > 0 {
		w.WriteString(b.Drafts[len(b.Drafts)-1])
	}
	w.WriteString("\n\n---\n\n## Convergence Trace\n\n")
	w.WriteString("| Round | Blocking | Important | Nits |\n|---|---|---|---|\n")
	for i, c := range b.Critiques {
		fmt.Fprintf(&w, "| %d | %d | %d | %d |\n", i+1, len(c.Blocking), len(c.Important), len(c.Nits))
	}
	if len(b.Critiques) > 0 {
		last := b.Critiques[len(b.Critiques)-1]
		if len(last.Nits) > 0 {
			w.WriteString("\n## Remaining Nits (non-blocking)\n\n")
			for _, n := range last.Nits {
				fmt.Fprintf(&w, "- %s\n", n)
			}
		}
	}
	if len(b.Sources) > 0 {
		w.WriteString("\n## Sources\n\n")
		for _, s := range b.Sources {
			fmt.Fprintf(&w, "- **%s**\n  %s\n\n", s.URL, s.Summary)
		}
	}
	return w.String()
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func draftPrompt(query string) string {
	return "You are a senior technical researcher. Investigate the following thoroughly. " +
		"Cover: current best practices, common pitfalls, recommended libraries/approaches, " +
		"real-world tradeoffs, and concrete code patterns where applicable. Be specific and " +
		"cite reasoning, not just assertions.\n\nQuery: " + query
}

func critiquePrompt(draft string) string {
	return `You are reviewing a research draft. Return your findings as JSON:
{
  "blocking": ["finding 1", "finding 2"],
  "important": ["finding 1"],
  "nits": ["finding 1"]
}

BLOCKING:  factual errors, untested claims that change the conclusion, missing critical context.
IMPORTANT: weak evidence, alternative interpretations not addressed, edge cases that matter.
NIT:       style, wording, minor omissions.

Be specific. Return ONLY the JSON object, no surrounding text.

--- DRAFT ---
` + draft
}

func revisePrompt(draft string, c Critique) string {
	var critJSON strings.Builder
	critJSON.WriteString("BLOCKING:\n")
	for _, f := range c.Blocking {
		fmt.Fprintf(&critJSON, "- %s\n", f)
	}
	critJSON.WriteString("\nIMPORTANT:\n")
	for _, f := range c.Important {
		fmt.Fprintf(&critJSON, "- %s\n", f)
	}
	return "Revise the draft below. Address every BLOCKING and IMPORTANT finding from the critique. " +
		"Keep what works. Return the new draft only.\n\n--- CRITIQUE ---\n" + critJSON.String() +
		"\n--- DRAFT ---\n" + draft
}
