package debug

import (
	"strings"
	"testing"
)

func TestSweepPromptOptionalSections(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want []string
		omit []string
	}{
		{
			name: "bug only", in: Input{Bug: "panic on empty input"},
			want: []string{"panic on empty input", "## Evidence", "## Hypotheses"},
			omit: []string{"--- FAILING TEST ---", "--- LOG / STACK", "--- START HERE"},
		},
		{
			name: "all evidence", in: Input{Bug: "boom", TestName: "TestFoo", LogBody: "stack line", FileHints: []string{"a.go", "b.go:20"}},
			want: []string{"--- FAILING TEST ---", "TestFoo", "--- LOG / STACK (truncated) ---", "stack line", "--- START HERE", "a.go\nb.go:20"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sweepPrompt(tt.in)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("prompt missing %q:\n%s", want, got)
				}
			}
			for _, omit := range tt.omit {
				if strings.Contains(got, omit) {
					t.Errorf("prompt unexpectedly contains %q:\n%s", omit, got)
				}
			}
		})
	}
}

func TestReviewerPromptsEmbedBriefAndDemandJSON(t *testing.T) {
	for name, got := range map[string]string{
		"misread":    reviewPromptMisread("CITED BRIEF"),
		"root cause": reviewPromptRootCause("CITED BRIEF"),
	} {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{"CITED BRIEF", "Return ONLY this JSON", `{"blocking":["..."]`} {
				if !strings.Contains(got, want) {
					t.Errorf("prompt missing %q:\n%s", want, got)
				}
			}
		})
	}
}
