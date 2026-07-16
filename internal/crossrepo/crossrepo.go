// Package crossrepo implements the guarded, read-only cross-repository
// relationship analysis pathway.
package crossrepo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/progress"
)

const (
	// PipelineName is the user-visible pathway name.
	PipelineName    = "cross-repo"
	maxReviewSample = 5
)

// ErrTreeChanged means the agy sweep changed at least one mounted repository.
// Callers must treat this as a hard safety failure, even if the sweep returned.
var ErrTreeChanged = errors.New("agy sweep changed a mounted repository")

// Channel is the narrow provider interface needed by the pathway.
type Channel interface {
	Send(context.Context, string) (string, error)
}

// Site identifies one endpoint of a claimed cross-repository relationship.
type Site struct {
	Root   string `json:"root"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Symbol string `json:"symbol"`
}

// Finding is one structurally valid producer-to-consumer link claimed by agy.
type Finding struct {
	Producer     Site   `json:"producer"`
	Consumer     Site   `json:"consumer"`
	Relationship string `json:"relationship"`
	Contract     string `json:"contract"`
	Reason       string `json:"reason"`
}

// TreeChange records paths changed by the sweep in one mounted root.
type TreeChange struct {
	Root  string
	Paths []string
}

// Report is the complete pathway result rendered atomically by the command.
type Report struct {
	Roots          []string
	Question       string
	StartedAt      time.Time
	EndedAt        time.Time
	SweepChannel   string
	RawSweep       string
	Findings       []Finding
	Warnings       []string
	CodexReview    string
	CodexError     string
	TreeChanges    []TreeChange
	TreeGuardError string
	ReviewedCount  int
}

// Options bundles channels, mounted roots, progress, and the mandatory
// all-roots post-sweep tree guard.
type Options struct {
	Roots        []string
	Question     string
	Sweeper      Channel
	Codex        Channel
	Prog         *progress.Tracker
	SweepChannel string
	AfterSweep   func() ([]TreeChange, error)
}

// Run executes one agy sweep and at most one bounded Codex spot-check. The
// tree guard runs immediately after agy returns. Any mutation or guard error
// aborts before parsing or review and is returned as a hard failure with a
// report suitable for a forensic artifact.
func Run(ctx context.Context, o Options) (*Report, error) {
	prog := o.Prog
	if prog == nil {
		prog = progress.Quiet()
	}
	rep := &Report{
		Roots:        append([]string(nil), o.Roots...),
		Question:     o.Question,
		StartedAt:    time.Now().UTC(),
		SweepChannel: defaultString(o.SweepChannel, "agy:default"),
	}
	if o.Sweeper == nil {
		return nil, errors.New("cross-repo sweep: no sweeper configured")
	}
	if len(o.Roots) < 2 {
		return nil, errors.New("cross-repo sweep: at least two repository roots are required")
	}
	if o.AfterSweep == nil {
		return nil, errors.New("cross-repo sweep: all-roots tree guard is required")
	}

	stage := prog.Stage("Tracing cross-repository links (agy reads every mounted repo)")
	raw, sweepErr := o.Sweeper.Send(ctx, sweepPrompt(o.Roots, o.Question))
	rep.RawSweep = raw
	treeChanges, err := o.AfterSweep()
	rep.TreeChanges = treeChanges
	if err != nil {
		rep.TreeGuardError = err.Error()
		rep.EndedAt = time.Now().UTC()
		stage.Fail(err)
		return rep, fmt.Errorf("cross-repo tree guard: %w", err)
	}
	if len(rep.TreeChanges) > 0 {
		rep.EndedAt = time.Now().UTC()
		stage.Fail(ErrTreeChanged)
		return rep, ErrTreeChanged
	}
	if sweepErr != nil {
		stage.Fail(sweepErr)
		return nil, fmt.Errorf("cross-repo sweep: %w", sweepErr)
	}
	stage.Done("cross-repository links collected")

	rep.Findings, rep.Warnings = ParseFindings(raw, o.Roots)
	sample := findingSample(rep.Findings, maxReviewSample)
	if len(sample) > 0 {
		rep.ReviewedCount = len(sample)
		reviewStage := prog.Stage("Spot-checking cross-repository links (codex)")
		if o.Codex == nil {
			rep.CodexError = "codex review channel is unavailable"
			reviewStage.Fail(errors.New(rep.CodexError))
		} else {
			rep.CodexReview, err = o.Codex.Send(ctx, reviewPrompt(sample))
			if err != nil {
				rep.CodexError = err.Error()
				reviewStage.Fail(err)
			} else {
				reviewStage.Done("%d link(s) sampled", len(sample))
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
	b.WriteString("# cross-repo report\n\n")
	fmt.Fprintf(&b, "- Date: %s\n", r.EndedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Sweep channel: %s\n", r.SweepChannel)
	fmt.Fprintf(&b, "- Mounted repository roots: %d\n", len(r.Roots))
	fmt.Fprintf(&b, "- Structurally valid cross-repo links: %d\n", len(r.Findings))
	if r.Question != "" {
		fmt.Fprintf(&b, "- Question: %s\n", r.Question)
	}
	b.WriteString("\n## Repository roots\n\n")
	for _, root := range r.Roots {
		fmt.Fprintf(&b, "- %s\n", root)
	}
	if len(r.TreeChanges) > 0 || r.TreeGuardError != "" {
		b.WriteString("\n## ⚠ SAFETY ABORT: mounted tree guard failed\n\n")
		b.WriteString("Styx refuses to report this sweep as successful. Codex review was not run. Inspect and revert unexpected changes before trusting any raw output.\n")
		if r.TreeGuardError != "" {
			fmt.Fprintf(&b, "\n- Guard error: %s\n", r.TreeGuardError)
		}
		for _, change := range r.TreeChanges {
			fmt.Fprintf(&b, "\n### %s\n\n", change.Root)
			for _, path := range change.Paths {
				fmt.Fprintf(&b, "- %s\n", path)
			}
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n## Parser warnings\n\n")
		for _, warning := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}
	b.WriteString("\n## Cross-repository links\n\n")
	if len(r.Findings) == 0 {
		b.WriteString("No structurally valid findings were available to review.\n")
	}
	for i, finding := range r.Findings {
		fmt.Fprintf(&b, "### %d. %s\n\n", i+1, finding.Relationship)
		fmt.Fprintf(&b, "- Producer: %s (%s :: %s:%d)\n", finding.Producer.Symbol, finding.Producer.Root, finding.Producer.Path, finding.Producer.Line)
		fmt.Fprintf(&b, "- Consumer: %s (%s :: %s:%d)\n", finding.Consumer.Symbol, finding.Consumer.Root, finding.Consumer.Path, finding.Consumer.Line)
		fmt.Fprintf(&b, "- Contract/API: %s\n", finding.Contract)
		fmt.Fprintf(&b, "- Sweep rationale: %s\n", finding.Reason)
	}
	b.WriteString("\n## Codex link spot-check\n\n")
	switch {
	case len(r.TreeChanges) > 0 || r.TreeGuardError != "":
		b.WriteString("Skipped because the mandatory all-roots tree guard failed.\n")
	case r.CodexReview != "":
		fmt.Fprintf(&b, "Sampled %d cross-repository link(s).\n\n%s\n", r.ReviewedCount, r.CodexReview)
	case r.CodexError != "":
		fmt.Fprintf(&b, "Review failed after selecting %d link(s): %s\n", r.ReviewedCount, r.CodexError)
	default:
		b.WriteString("Skipped because the sweep produced no structurally valid cross-repository links.\n")
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
