// Package mapimpact implements the read-only repository impact-mapping pathway.
package mapimpact

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/progress"
)

const (
	// PipelineName is the user-visible pathway name.
	PipelineName    = "map-impact"
	maxReviewSample = 5
)

// Channel is the narrow provider interface needed by the pathway.
type Channel interface {
	Send(context.Context, string) (string, error)
}

// Input is one symbol, repository-relative file, or git diff specification.
type Input struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// Site identifies one endpoint of a claimed dependency edge.
type Site struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol"`
}

// Finding is one structurally valid dependency edge claimed by agy.
type Finding struct {
	Source       Site   `json:"source"`
	Dependent    Site   `json:"dependent"`
	Relationship string `json:"relationship"`
	Impact       string `json:"impact"`
	Reason       string `json:"reason"`
}

// Report is the complete pathway result rendered to disk by the command.
type Report struct {
	Input         Input
	StartedAt     time.Time
	EndedAt       time.Time
	SweepChannel  string
	RawSweep      string
	Findings      []Finding
	Warnings      []string
	CodexReview   string
	CodexError    string
	SweepDirtied  []string
	ReviewedCount int
}

// Options bundles channels, progress, and the post-sweep tree-guard hook.
type Options struct {
	Input        Input
	Sweeper      Channel
	Codex        Channel
	Prog         *progress.Tracker
	SweepChannel string
	AfterSweep   func() []string
}

// Run executes one agy sweep and at most one Codex dependency-edge spot-check.
// Malformed findings become warnings and never abort a completed sweep.
func Run(ctx context.Context, o Options) (*Report, error) {
	prog := o.Prog
	if prog == nil {
		prog = progress.Quiet()
	}
	rep := &Report{
		Input:        o.Input,
		StartedAt:    time.Now().UTC(),
		SweepChannel: defaultString(o.SweepChannel, "agy:default"),
	}
	if o.Sweeper == nil {
		return nil, fmt.Errorf("map-impact sweep: no sweeper configured")
	}

	stage := prog.Stage("Mapping repository impact (agy reads the repo)")
	raw, err := o.Sweeper.Send(ctx, sweepPrompt(o.Input))
	if o.AfterSweep != nil {
		rep.SweepDirtied = o.AfterSweep()
	}
	if err != nil {
		stage.Fail(err)
		return nil, fmt.Errorf("map-impact sweep: %w", err)
	}
	rep.RawSweep = raw
	stage.Done("dependency edges collected")

	rep.Findings, rep.Warnings = ParseFindings(raw)
	sample := findingSample(rep.Findings, maxReviewSample)
	if len(sample) > 0 {
		rep.ReviewedCount = len(sample)
		reviewStage := prog.Stage("Spot-checking dependency edges (codex)")
		if o.Codex == nil {
			rep.CodexError = "codex review channel is unavailable"
			reviewStage.Fail(fmt.Errorf("%s", rep.CodexError))
		} else {
			rep.CodexReview, err = o.Codex.Send(ctx, reviewPrompt(sample))
			if err != nil {
				rep.CodexError = err.Error()
				reviewStage.Fail(err)
			} else {
				reviewStage.Done("%d edge(s) sampled", len(sample))
			}
		}
	}
	rep.EndedAt = time.Now().UTC()
	return rep, nil
}

func findingSample(findings []Finding, limit int) []Finding {
	if limit <= 0 || len(findings) == 0 {
		return nil
	}
	if len(findings) < limit {
		limit = len(findings)
	}
	return append([]Finding(nil), findings[:limit]...)
}

// RenderReport returns the complete markdown artifact.
func RenderReport(r *Report) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("# map-impact report\n\n")
	fmt.Fprintf(&b, "- Input kind: %s\n", r.Input.Kind)
	fmt.Fprintf(&b, "- Input: %s\n", r.Input.Value)
	fmt.Fprintf(&b, "- Date: %s\n", r.EndedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Sweep channel: %s\n", r.SweepChannel)
	fmt.Fprintf(&b, "- Structurally valid dependency edges: %d\n", len(r.Findings))
	if len(r.SweepDirtied) > 0 {
		b.WriteString("\n## ⚠ Sweep modified the working tree\n\n")
		b.WriteString("The sweep was instructed to be read-only, but these paths changed during it. Review and revert them; treat every result with suspicion.\n\n")
		for _, path := range r.SweepDirtied {
			fmt.Fprintf(&b, "- %s\n", path)
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n## Parser warnings\n\n")
		for _, warning := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}
	b.WriteString("\n## Dependency edges\n\n")
	if len(r.Findings) == 0 {
		b.WriteString("No structurally valid findings were available to review.\n")
	}
	for i, finding := range r.Findings {
		fmt.Fprintf(&b, "### %d. %s — %s\n\n", i+1, finding.Relationship, strings.ToUpper(finding.Impact))
		fmt.Fprintf(&b, "- Source: %s (%s:%d)\n", finding.Source.Symbol, finding.Source.Path, finding.Source.Line)
		fmt.Fprintf(&b, "- Dependent: %s (%s:%d)\n", finding.Dependent.Symbol, finding.Dependent.Path, finding.Dependent.Line)
		fmt.Fprintf(&b, "- Sweep rationale: %s\n", finding.Reason)
	}
	b.WriteString("\n## Codex edge spot-check\n\n")
	switch {
	case r.CodexReview != "":
		fmt.Fprintf(&b, "Sampled %d dependency edge(s).\n\n%s\n", r.ReviewedCount, r.CodexReview)
	case r.CodexError != "":
		fmt.Fprintf(&b, "Review failed after selecting %d edge(s): %s\n", r.ReviewedCount, r.CodexError)
	default:
		b.WriteString("Skipped because the sweep produced no structurally valid dependency edges.\n")
	}
	b.WriteString("\n## Machine-readable findings\n\n```json\n")
	structured, _ := json.MarshalIndent(struct {
		Findings []Finding `json:"findings"`
	}{Findings: r.Findings}, "", "  ")
	b.Write(structured)
	b.WriteString("\n```\n")
	b.WriteString("\n## Raw agy sweep\n\n```json\n")
	b.WriteString(r.RawSweep)
	b.WriteString("\n```\n")
	return b.String()
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
