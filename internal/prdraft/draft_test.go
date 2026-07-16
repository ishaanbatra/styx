package prdraft

import (
	"strings"
	"testing"
)

func testContext() Context {
	return Context{
		Goal: "fix parser #42", Branch: "styx/fix-parser",
		TouchedPaths: []string{"internal/parser.go", "internal/parser_test.go"},
		Tests:        CheckState{Successful: true, Attempts: 2}, Review: CheckState{Successful: true, Attempts: 1},
		IssueRefs: []string{"#42"}, AllowedLabels: append([]string(nil), LabelAllowlist...),
		TestRefs:   []string{"TestParser"},
		CoreLabels: []string{"bug", "tests"}, RiskFlags: []string{"security-sensitive changes"},
		DraftRequired: true,
	}
}

func TestStrictParsers(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		body bool
		ok   bool
	}{
		{name: "title", raw: `{"title":"Fix parser"}`, ok: true},
		{name: "title unknown field", raw: `{"title":"Fix parser","draft":false}`},
		{name: "title fenced", raw: "```json\n{\"title\":\"Fix parser\"}\n```"},
		{name: "title trailing", raw: `{"title":"Fix parser"} {}`},
		{name: "body", body: true, raw: `{"summary_bullets":["Fix parser"],"test_plan_bullets":["Exercise parser cases"],"reviewer_checklist":["Inspect parser behavior"],"release_note":"Parser fixes.","label_suggestions":["bug"]}`, ok: true},
		{name: "body owns test truth", body: true, raw: `{"summary_bullets":["Fix parser"],"test_plan_bullets":["Exercise parser"],"reviewer_checklist":["Inspect parser"],"release_note":"","label_suggestions":[],"tests_passed":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			if tt.body {
				_, err = ParseBody(tt.raw)
			} else {
				_, err = ParseTitle(tt.raw)
			}
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%t", err, tt.ok)
			}
		})
	}
}

func TestValidateTitle(t *testing.T) {
	ctx := testContext()
	tests := []struct {
		name  string
		title string
		ok    bool
	}{
		{name: "valid evidence", title: "Fix `internal/parser.go` for #42", ok: true},
		{name: "too long", title: strings.Repeat("x", 73)},
		{name: "unknown file", title: "Fix `internal/missing.go`"},
		{name: "unknown issue", title: "Fix parser for #99"},
		{name: "known test", title: "Fix TestParser behavior", ok: true},
		{name: "unknown test", title: "Fix TestMissing behavior"},
		{name: "control character", title: "Fix\nparser"},
		{name: "secret", title: "Set API_KEY=super-secret-value"},
		{name: "attribution", title: "Generated with styx"},
		{name: "contradictory truth", title: "Fix parser; all tests passed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTitle(ctx, TitleDraft{Title: tt.title})
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%t", err, tt.ok)
			}
		})
	}
}

func validBody() BodyDraft {
	return BodyDraft{
		SummaryBullets:    []string{"Update `internal/parser.go` for #42"},
		TestPlanBullets:   []string{"Exercise parser edge cases"},
		ReviewerChecklist: []string{"Inspect parser error handling"},
		ReleaseNote:       "Parser behavior is more robust.", LabelSuggestions: []string{"bug"},
	}
}

func TestValidateBody(t *testing.T) {
	ctx := testContext()
	tests := []struct {
		name   string
		mutate func(*BodyDraft)
		ok     bool
	}{
		{name: "valid", ok: true},
		{name: "leading dash", mutate: func(d *BodyDraft) { d.SummaryBullets[0] = "- already rendered" }},
		{name: "contradictory test prose", mutate: func(d *BodyDraft) { d.SummaryBullets[0] = "All tests passed" }},
		{name: "contradictory green prose", mutate: func(d *BodyDraft) { d.SummaryBullets[0] = "Checks are green" }},
		{name: "contradictory review prose", mutate: func(d *BodyDraft) { d.ReviewerChecklist[0] = "Review is clean" }},
		{name: "unknown label", mutate: func(d *BodyDraft) { d.LabelSuggestions = []string{"needs-triage"} }},
		{name: "duplicate label", mutate: func(d *BodyDraft) { d.LabelSuggestions = []string{"bug", "bug"} }},
		{name: "too many labels", mutate: func(d *BodyDraft) { d.LabelSuggestions = []string{"bug", "ci", "database", "documentation", "tests"} }},
		{name: "unknown issue", mutate: func(d *BodyDraft) { d.ReleaseNote = "Fixes #100" }},
		{name: "attribution", mutate: func(d *BodyDraft) { d.ReleaseNote = "Generated with styx" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			draft := validBody()
			if tt.mutate != nil {
				tt.mutate(&draft)
			}
			err := ValidateBody(ctx, draft)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%t", err, tt.ok)
			}
		})
	}
}

func TestRenderBodyOwnsFactsAndMetadata(t *testing.T) {
	ctx := testContext()
	body := RenderBody(ctx, validBody())
	for _, want := range []string{
		"## Summary", "- Update `internal/parser.go` for #42",
		"Test stage: completed successfully after 2 attempt(s)",
		"Review stage: completed successfully after 1 attempt(s)",
		"- [ ] Inspect parser error handling", "Related: #42",
		"security-sensitive changes",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered body missing %q:\n%s", want, body)
		}
	}
	if got := strings.Join(Labels(ctx, validBody()), ","); got != "bug,tests" {
		t.Errorf("labels = %q, want bug,tests", got)
	}
}

func TestStaticTitleRejectsUnsafeGoalText(t *testing.T) {
	tests := []struct {
		name string
		goal string
		want string
	}{
		{name: "ordinary", goal: "Improve parser.", want: "Improve parser"},
		{name: "secret-like", goal: "Set password=hunter2", want: "Update project"},
		{name: "contradictory truth", goal: "All tests passed", want: "Update project"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testContext()
			ctx.Goal = tt.goal
			if got := StaticTitle(ctx).Title; got != tt.want {
				t.Errorf("StaticTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPromptsExcludeVerdictSchemaFields(t *testing.T) {
	ctx := testContext()
	body := BodyPrompt(ctx)
	if strings.Contains(body, `"tests_passed"`) || strings.Contains(body, `"review_approved"`) {
		t.Fatalf("model schema owns truth:\n%s", body)
	}
	for _, want := range []string{"BEGIN DETERMINISTIC CONTEXT", `"allowed_labels"`, `"summary_bullets"`} {
		if !strings.Contains(body, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
