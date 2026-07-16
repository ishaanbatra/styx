// Package deadcode implements the read-only dead-code sweep pathway.
package deadcode

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
	PipelineName    = "dead-code"
	maxReviewSample = 5
)

// Channel is the narrow provider interface needed by the pathway.
type Channel interface {
	Send(context.Context, string) (string, error)
}

// Input identifies the repository-relative scope requested by the user.
type Input struct {
	Target      string
	ProjectPath string
}

// Definition identifies the site excluded by deterministic verification.
type Definition struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// Finding is one structurally valid agy claim.
type Finding struct {
	Kind       string     `json:"kind"`
	Symbol     string     `json:"symbol"`
	Definition Definition `json:"definition"`
	Reason     string     `json:"reason"`
}

// Reference is one whole-word match outside the reported definition site.
type Reference struct {
	Path string `json:"path"`
	Line int    `json:"line"`
}

// VerifiedFinding records the deterministic result for one accepted finding.
type VerifiedFinding struct {
	Finding
	Status     string      `json:"status"`
	References []Reference `json:"references,omitempty"`
}

// Report is the complete pathway result rendered to disk by the command.
type Report struct {
	Target        string
	StartedAt     time.Time
	EndedAt       time.Time
	SweepChannel  string
	RawSweep      string
	Findings      []VerifiedFinding
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

// Run executes one sweep, deterministic verification, and at most one Codex
// review turn. Malformed findings become warnings and never abort the run.
func Run(ctx context.Context, o Options) (*Report, error) {
	prog := o.Prog
	if prog == nil {
		prog = progress.Quiet()
	}
	rep := &Report{
		Target:       o.Input.Target,
		StartedAt:    time.Now().UTC(),
		SweepChannel: defaultString(o.SweepChannel, "agy:default"),
	}
	if o.Sweeper == nil {
		return nil, fmt.Errorf("dead-code sweep: no sweeper configured")
	}

	st := prog.Stage("Dead-code sweep (agy reads the repo)")
	raw, err := o.Sweeper.Send(ctx, sweepPrompt(o.Input))
	if o.AfterSweep != nil {
		rep.SweepDirtied = o.AfterSweep()
	}
	if err != nil {
		st.Fail(err)
		return nil, fmt.Errorf("dead-code sweep: %w", err)
	}
	rep.RawSweep = raw
	st.Done("findings collected")

	findings, parseWarnings := ParseFindings(raw)
	rep.Warnings = append(rep.Warnings, parseWarnings...)
	verifyStage := prog.Stage("Verifying reported symbols")
	rep.Findings, parseWarnings, err = Verify(ctx, o.Input.ProjectPath, findings)
	rep.Warnings = append(rep.Warnings, parseWarnings...)
	if err != nil {
		verifyStage.Fail(err)
		return nil, fmt.Errorf("verify dead-code findings: %w", err)
	}
	verifyStage.Done("%d finding(s) checked", len(rep.Findings))

	sample := confirmedSample(rep.Findings, maxReviewSample)
	if len(sample) > 0 {
		rep.ReviewedCount = len(sample)
		reviewStage := prog.Stage("Spot-checking confirmed findings (codex)")
		if o.Codex == nil {
			rep.CodexError = "codex review channel is unavailable"
			reviewStage.Fail(fmt.Errorf("%s", rep.CodexError))
		} else {
			rep.CodexReview, err = o.Codex.Send(ctx, reviewPrompt(sample))
			if err != nil {
				rep.CodexError = err.Error()
				reviewStage.Fail(err)
			} else {
				reviewStage.Done("%d finding(s) sampled", len(sample))
			}
		}
	}
	rep.EndedAt = time.Now().UTC()
	return rep, nil
}

func confirmedSample(findings []VerifiedFinding, limit int) []VerifiedFinding {
	if limit <= 0 {
		return nil
	}
	out := make([]VerifiedFinding, 0, limit)
	for _, finding := range findings {
		if finding.Status != "CONFIRMED" {
			continue
		}
		out = append(out, finding)
		if len(out) == limit {
			break
		}
	}
	return out
}

// RenderReport returns the complete markdown artifact.
func RenderReport(r *Report) string {
	if r == nil {
		return ""
	}
	confirmed, refuted := 0, 0
	for _, finding := range r.Findings {
		if finding.Status == "CONFIRMED" {
			confirmed++
		} else if finding.Status == "REFUTED" {
			refuted++
		}
	}
	var b strings.Builder
	b.WriteString("# dead-code report\n\n")
	fmt.Fprintf(&b, "- Target: %s\n", r.Target)
	fmt.Fprintf(&b, "- Date: %s\n", r.EndedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Sweep channel: %s\n", r.SweepChannel)
	fmt.Fprintf(&b, "- Deterministic results: confirmed=%d, refuted=%d\n", confirmed, refuted)
	if len(r.SweepDirtied) > 0 {
		b.WriteString("\n## ⚠ Sweep modified the working tree\n\n")
		b.WriteString("The sweep was instructed to be read-only, but these paths changed during it. Review and revert them; treat every result with suspicion.\n\n")
		for _, path := range r.SweepDirtied {
			fmt.Fprintf(&b, "- %s\n", path)
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\n## Parser and verifier warnings\n\n")
		for _, warning := range r.Warnings {
			fmt.Fprintf(&b, "- %s\n", warning)
		}
	}
	b.WriteString("\n## Deterministic verification\n\n")
	if len(r.Findings) == 0 {
		b.WriteString("No structurally valid findings were available to verify.\n")
	}
	for i, finding := range r.Findings {
		fmt.Fprintf(&b, "### %d. %s %s — %s\n\n", i+1, finding.Kind, finding.Symbol, finding.Status)
		fmt.Fprintf(&b, "- Definition: %s:%d\n", finding.Definition.Path, finding.Definition.Line)
		fmt.Fprintf(&b, "- Sweep rationale: %s\n", finding.Reason)
		if len(finding.References) == 0 {
			b.WriteString("- Whole-word references outside definition: none\n")
		} else {
			b.WriteString("- Whole-word references outside definition:\n")
			for _, ref := range finding.References {
				fmt.Fprintf(&b, "  - %s:%d\n", ref.Path, ref.Line)
			}
		}
	}
	b.WriteString("\n## Codex spot-check\n\n")
	switch {
	case r.CodexReview != "":
		fmt.Fprintf(&b, "Sampled %d confirmed finding(s).\n\n%s\n", r.ReviewedCount, r.CodexReview)
	case r.CodexError != "":
		fmt.Fprintf(&b, "Review failed after selecting %d confirmed finding(s): %s\n", r.ReviewedCount, r.CodexError)
	default:
		b.WriteString("Skipped because the deterministic pass produced no confirmed findings.\n")
	}
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

// MarshalFinding is kept small and deterministic for tests and prompt samples.
func MarshalFinding(f Finding) string {
	b, _ := json.Marshal(f)
	return string(b)
}
