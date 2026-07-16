package prdraft

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// TitleDraft is the complete model-owned schema for pr.title.
type TitleDraft struct {
	Title string `json:"title"`
}

// BodyDraft is the complete model-owned schema for pr.body. Test and review
// verdicts are intentionally absent.
type BodyDraft struct {
	SummaryBullets    []string `json:"summary_bullets"`
	TestPlanBullets   []string `json:"test_plan_bullets"`
	ReviewerChecklist []string `json:"reviewer_checklist"`
	ReleaseNote       string   `json:"release_note"`
	LabelSuggestions  []string `json:"label_suggestions"`
}

var (
	backtickRE      = regexp.MustCompile("`([^`]+)`")
	contradictionRE = regexp.MustCompile(`(?i)\b(?:all\s+)?(?:tests?|checks?)\s+(?:(?:are|were)\s+)?(?:pass(?:ed|ing)?|succeed(?:ed|ing)?|green)\b|\b(?:test|check)\s+suite\s+(?:pass(?:ed|es|ing)?|succeed(?:ed|s|ing)?)\b|\breview\s+(?:is\s+|was\s+)?(?:clean|approved|passed|successful)\b|\breviewer(?:s)?\s+approved\b|\bno\s+(?:blocking|important|review)\s+(?:issues|findings|comments)\b`)
	secretRE        = regexp.MustCompile(`(?i)(?:api[_-]?key|access[_-]?token|password|secret)\s*[:=]\s*\S+|-----BEGIN [A-Z ]*PRIVATE KEY-----|\b(?:gh[opusr]_[A-Za-z0-9_]{20,})\b`)
)

// ParseTitle strictly decodes one JSON object with no unknown fields or tail.
func ParseTitle(raw string) (TitleDraft, error) {
	var draft TitleDraft
	if err := strictJSON(raw, &draft); err != nil {
		return TitleDraft{}, fmt.Errorf("parse PR title: %w", err)
	}
	return draft, nil
}

// ParseBody strictly decodes one JSON object with no unknown fields or tail.
func ParseBody(raw string) (BodyDraft, error) {
	var draft BodyDraft
	if err := strictJSON(raw, &draft); err != nil {
		return BodyDraft{}, fmt.Errorf("parse PR body: %w", err)
	}
	return draft, nil
}

func strictJSON(raw string, dst any) error {
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

// ValidateTitle checks bounded, evidence-grounded title prose.
func ValidateTitle(ctx Context, draft TitleDraft) error {
	title := strings.TrimSpace(draft.Title)
	if title == "" || utf8.RuneCountInString(title) > 72 {
		return fmt.Errorf("title must be 1-72 characters")
	}
	if strings.HasPrefix(title, "-") || strings.HasSuffix(title, ".") {
		return fmt.Errorf("title must not start with a dash or end with a period")
	}
	return validateProse(ctx, title)
}

// ValidateBody validates every model-owned field against the context packet.
func ValidateBody(ctx Context, draft BodyDraft) error {
	for name, items := range map[string][]string{
		"summary_bullets": draft.SummaryBullets, "test_plan_bullets": draft.TestPlanBullets,
		"reviewer_checklist": draft.ReviewerChecklist,
	} {
		if len(items) == 0 || len(items) > 6 {
			return fmt.Errorf("%s must contain 1-6 items", name)
		}
		for _, item := range items {
			if strings.TrimSpace(item) == "" || utf8.RuneCountInString(item) > 180 {
				return fmt.Errorf("%s items must be 1-180 characters", name)
			}
			if strings.HasPrefix(strings.TrimSpace(item), "-") {
				return fmt.Errorf("%s items must not include a leading dash", name)
			}
			if err := validateProse(ctx, item); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	if utf8.RuneCountInString(draft.ReleaseNote) > 240 {
		return fmt.Errorf("release_note exceeds 240 characters")
	}
	if err := validateProse(ctx, draft.ReleaseNote); err != nil {
		return fmt.Errorf("release_note: %w", err)
	}
	allowed := stringSet(ctx.AllowedLabels)
	if len(draft.LabelSuggestions) > 4 {
		return fmt.Errorf("label_suggestions must contain at most 4 items")
	}
	seen := map[string]bool{}
	for _, label := range draft.LabelSuggestions {
		if !allowed[label] {
			return fmt.Errorf("label %q is not allowlisted", label)
		}
		if seen[label] {
			return fmt.Errorf("label %q is duplicated", label)
		}
		seen[label] = true
	}
	return nil
}

func validateProse(ctx Context, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("invalid UTF-8")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("control characters are not allowed")
		}
	}
	if contradictionRE.MatchString(value) {
		return fmt.Errorf("prose contradicts deterministic test/review ownership")
	}
	if secretRE.MatchString(value) {
		return fmt.Errorf("secret-like content is not allowed")
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "generated with styx") || strings.Contains(lower, "co-authored-by:") {
		return fmt.Errorf("attribution is renderer-owned")
	}
	issues := stringSet(ctx.IssueRefs)
	for _, match := range issueRE.FindAllString(value, -1) {
		if !issues[match] {
			return fmt.Errorf("issue reference %s is not in the context packet", match)
		}
	}
	paths := stringSet(ctx.TouchedPaths)
	for _, match := range backtickRE.FindAllStringSubmatch(value, -1) {
		ref := strings.TrimSpace(match[1])
		if looksLikePath(ref) && !paths[ref] {
			return fmt.Errorf("file reference %q is not a touched path", ref)
		}
	}
	tests := stringSet(ctx.TestRefs)
	for _, ref := range testRefRE.FindAllString(value, -1) {
		if !tests[ref] {
			return fmt.Errorf("test reference %q is not in the context packet", ref)
		}
	}
	return nil
}

func looksLikePath(value string) bool {
	return strings.Contains(value, "/") || strings.HasSuffix(value, ".go") ||
		strings.HasSuffix(value, ".md") || strings.HasSuffix(value, ".json") ||
		strings.HasSuffix(value, ".toml") || strings.HasSuffix(value, ".yaml") ||
		strings.HasSuffix(value, ".yml")
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// StaticTitle is the deterministic final fallback for pr.title.
func StaticTitle(ctx Context) TitleDraft {
	title := strings.TrimSpace(strings.Join(strings.Fields(ctx.Goal), " "))
	if title == "" {
		title = "Update project"
	}
	title = strings.TrimSuffix(title, ".")
	for utf8.RuneCountInString(title) > 72 {
		_, size := utf8.DecodeLastRuneInString(title)
		title = title[:len(title)-size]
	}
	draft := TitleDraft{Title: strings.TrimSpace(title)}
	if ValidateTitle(ctx, draft) != nil {
		return TitleDraft{Title: "Update project"}
	}
	return draft
}

// StaticBody is the deterministic final fallback for pr.body.
func StaticBody(ctx Context) BodyDraft {
	summary := "Implement the requested change"
	if len(ctx.TouchedPaths) > 0 {
		summary = fmt.Sprintf("Update %d file(s) for the requested change", len(ctx.TouchedPaths))
	}
	return BodyDraft{
		SummaryBullets:    []string{summary},
		TestPlanBullets:   []string{"Run the repository's standard verification commands"},
		ReviewerChecklist: []string{"Confirm the implementation matches the requested goal"},
		ReleaseNote:       "No release note drafted.",
		LabelSuggestions:  nil,
	}
}

// Labels returns the deterministic core labels plus valid model suggestions.
func Labels(ctx Context, draft BodyDraft) []string {
	allowed := stringSet(ctx.AllowedLabels)
	labels := map[string]bool{}
	for _, label := range append(append([]string(nil), ctx.CoreLabels...), draft.LabelSuggestions...) {
		if allowed[label] {
			labels[label] = true
		}
	}
	out := make([]string, 0, len(labels))
	for label := range labels {
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}

// RenderBody combines validated prose with pipeline-owned facts.
func RenderBody(ctx Context, draft BodyDraft) string {
	var b bytes.Buffer
	renderBullets(&b, "Summary", draft.SummaryBullets, false)
	b.WriteString("## Verification\n\n")
	fmt.Fprintf(&b, "- Test stage: %s\n", checkDescription(ctx.Tests))
	fmt.Fprintf(&b, "- Review stage: %s\n\n", checkDescription(ctx.Review))
	renderBullets(&b, "Test plan", draft.TestPlanBullets, false)
	renderBullets(&b, "Reviewer checklist", draft.ReviewerChecklist, true)
	b.WriteString("## Release note\n\n")
	if strings.TrimSpace(draft.ReleaseNote) == "" {
		b.WriteString("No release note drafted.\n\n")
	} else {
		b.WriteString(strings.TrimSpace(draft.ReleaseNote) + "\n\n")
	}
	if len(ctx.RiskFlags) > 0 {
		renderBullets(&b, "Deterministic risk flags", ctx.RiskFlags, false)
	}
	if len(ctx.IssueRefs) > 0 {
		b.WriteString("## Issues\n\n")
		for _, ref := range ctx.IssueRefs {
			fmt.Fprintf(&b, "Related: %s\n", ref)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderBullets(b *bytes.Buffer, heading string, items []string, checklist bool) {
	fmt.Fprintf(b, "## %s\n\n", heading)
	for _, item := range items {
		prefix := "- "
		if checklist {
			prefix = "- [ ] "
		}
		fmt.Fprintf(b, "%s%s\n", prefix, strings.TrimSpace(item))
	}
	b.WriteString("\n")
}

func checkDescription(check CheckState) string {
	if check.Skipped {
		reason := strings.TrimSpace(check.SkippedReason)
		if reason == "" {
			return "skipped"
		}
		return "skipped: " + reason
	}
	status := "not completed successfully"
	if check.Successful {
		status = "completed successfully"
	}
	return fmt.Sprintf("%s after %d attempt(s)", status, max(1, check.Attempts))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
