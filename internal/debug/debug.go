// Package debug implements the ultraFerdDebug diagnosis pathway: one
// repository-wide sweep, two independent reviews, and a deterministic verdict.
// It never edits code.
package debug

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ishaanbatra/styx/internal/progress"
	"github.com/ishaanbatra/styx/internal/research"
)

// PipelineName is the user-visible name of the debug pathway.
const PipelineName = "ultraFerdDebug"

// Channel is the narrow provider interface needed by the pathway.
type Channel interface {
	Send(ctx context.Context, prompt string) (string, error)
}

// Input is the user-supplied problem statement and optional evidence.
type Input struct {
	Bug         string
	TestName    string
	LogPath     string
	LogBody     string
	FileHints   []string
	ProjectPath string
}

// Review is one reviewer's raw and parsed response.
type Review struct {
	Channel  string            `json:"channel"`
	Lens     string            `json:"lens"`
	Raw      string            `json:"raw"`
	Critique research.Critique `json:"critique"`
	Err      string            `json:"err,omitempty"`
}

// Verdict is synthesized without another model call.
type Verdict struct {
	Confirmed  bool   `json:"confirmed"`
	Confidence string `json:"confidence"`
	Summary    string `json:"summary"`
}

// Report is the complete diagnosis produced by the pathway.
type Report struct {
	Bug          string    `json:"bug"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at"`
	SweepChannel string    `json:"sweep_channel"`
	BriefPath    string    `json:"brief_path,omitempty"`
	Brief        string    `json:"brief"`
	CodexReview  Review    `json:"codex_review"`
	ClaudeReview Review    `json:"claude_review"`
	Verdict      Verdict   `json:"verdict"`

	// SweepDirtied lists working-tree paths the sweep modified despite the
	// diagnosis-only instruction. The caller populates it (the pathway itself
	// has no repo access); non-empty means the sweep channel did not honor
	// read-only and its evidence should be re-verified.
	SweepDirtied []string `json:"sweep_dirtied,omitempty"`
}

// Options bundles the pathway input, channels, narration, and persistence.
type Options struct {
	Input   Input
	Sweeper Channel
	Codex   Channel
	Claude  Channel
	Prog    *progress.Tracker

	SweepChannel  string
	CodexChannel  string
	ClaudeChannel string

	// PersistBrief runs immediately after the sweep, before either review.
	// A persistence failure is narrated but does not discard the diagnosis.
	PersistBrief func(brief string) (string, error)

	// PresetBrief skips the expensive sweep and re-enters at the review stage.
	PresetBrief string
}

// Run executes ultraFerdDebug. Sweep failure is fatal; reviewer failures are
// retained in the report so one unavailable reviewer does not lose the brief.
func Run(ctx context.Context, o Options) (*Report, error) {
	prog := o.Prog
	if prog == nil {
		prog = progress.Quiet()
	}
	rep := &Report{
		Bug:          o.Input.Bug,
		StartedAt:    time.Now().UTC(),
		SweepChannel: defaultString(o.SweepChannel, "agy:default"),
	}

	if o.PresetBrief != "" {
		rep.Brief = o.PresetBrief
	} else {
		if o.Sweeper == nil {
			return nil, fmt.Errorf("agy sweep: no sweeper configured")
		}
		st := prog.Stage("Debug sweep (agy reads the repo)")
		brief, err := o.Sweeper.Send(ctx, sweepPrompt(o.Input))
		if err != nil {
			st.Fail(err)
			return nil, fmt.Errorf("agy sweep: %w", err)
		}
		st.Done("brief collected")
		rep.Brief = brief

		if o.PersistBrief != nil {
			persistStage := prog.Stage("Saving debug brief")
			path, err := o.PersistBrief(brief)
			if err != nil {
				persistStage.Fail(err)
			} else {
				rep.BriefPath = path
				persistStage.Done("saved")
			}
		}
	}

	st := prog.Stage("Reviewing debug brief (codex + claude in parallel)")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rep.CodexReview = collectReview(ctx, o.Codex,
			defaultString(o.CodexChannel, "codex"), "misread", reviewPromptMisread(rep.Brief))
	}()
	go func() {
		defer wg.Done()
		rep.ClaudeReview = collectReview(ctx, o.Claude,
			defaultString(o.ClaudeChannel, "claude:sonnet"), "root-cause", reviewPromptRootCause(rep.Brief))
	}()
	wg.Wait()
	st.Done("2 reviews collected")

	rep.Verdict = synthesizeVerdict(rep.CodexReview, rep.ClaudeReview)
	rep.EndedAt = time.Now().UTC()
	return rep, nil
}

func collectReview(ctx context.Context, ch Channel, channelName, lens, prompt string) Review {
	r := Review{Channel: channelName, Lens: lens}
	if ch == nil {
		r.Err = "review channel is unavailable"
		return r
	}
	raw, err := ch.Send(ctx, prompt)
	r.Raw = raw
	if err != nil {
		r.Err = err.Error()
		return r
	}
	critique, err := research.Parse(raw)
	if err != nil {
		r.Err = fmt.Sprintf("parse review: %v", err)
		return r
	}
	r.Critique = critique
	return r
}

func synthesizeVerdict(codex, claude Review) Verdict {
	blocking := prefixedFindings(codex, codex.Critique.Blocking)
	blocking = append(blocking, prefixedFindings(claude, claude.Critique.Blocking)...)
	confirmed := len(blocking) == 0

	failed := 0
	if codex.Err != "" {
		failed++
	}
	if claude.Err != "" {
		failed++
	}
	important := len(codex.Critique.Important) + len(claude.Critique.Important)
	confidence := "medium"
	switch {
	case len(blocking) > 0 || failed == 2:
		confidence = "low"
	case failed == 0 && important == 0:
		confidence = "high"
	}

	summary := "Top hypothesis survived both independent reviews."
	if !confirmed {
		summary = "Unresolved blocking findings:\n- " + strings.Join(blocking, "\n- ")
	} else if failed == 2 {
		summary = "No blocking finding was returned, but both independent reviews failed."
	} else if failed == 1 {
		summary = "Top hypothesis had no blocking finding; one independent review failed."
	}
	return Verdict{Confirmed: confirmed, Confidence: confidence, Summary: summary}
}

func prefixedFindings(r Review, findings []string) []string {
	prefix := strings.SplitN(r.Channel, ":", 2)[0]
	out := make([]string, 0, len(findings))
	for _, finding := range findings {
		out = append(out, prefix+": "+finding)
	}
	return out
}

// RenderReport returns the implementer-ready markdown artifact.
func RenderReport(r *Report) string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s report\n\n", PipelineName)
	fmt.Fprintf(&b, "- Bug: %s\n", r.Bug)
	fmt.Fprintf(&b, "- Date: %s\n", r.EndedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Sweep channel: %s\n", r.SweepChannel)
	fmt.Fprintf(&b, "- Verdict: %s confidence; confirmed=%t\n", r.Verdict.Confidence, r.Verdict.Confirmed)
	if r.BriefPath != "" {
		fmt.Fprintf(&b, "- Recoverable brief: %s\n", r.BriefPath)
	}
	if len(r.SweepDirtied) > 0 {
		b.WriteString("\n## ⚠ Sweep modified the working tree\n\n")
		b.WriteString("The sweep is instructed to be diagnosis-only, but these paths changed during it. Review and revert them; treat the brief's evidence with suspicion.\n\n")
		for _, p := range r.SweepDirtied {
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	fmt.Fprintf(&b, "\n## Debug Brief\n\n%s\n", r.Brief)
	renderReview(&b, "Codex Review", r.CodexReview)
	renderReview(&b, "Claude Review", r.ClaudeReview)
	fmt.Fprintf(&b, "\n## Verdict\n\n- Confidence: %s\n- Confirmed: %t\n\n%s\n",
		r.Verdict.Confidence, r.Verdict.Confirmed, r.Verdict.Summary)
	return b.String()
}

func renderReview(b *strings.Builder, heading string, r Review) {
	fmt.Fprintf(b, "\n## %s (lens: %s)\n\n", heading, r.Lens)
	if r.Raw != "" {
		b.WriteString(r.Raw)
		b.WriteString("\n")
	}
	if r.Err != "" {
		fmt.Fprintf(b, "Review failed: %s\n", r.Err)
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
