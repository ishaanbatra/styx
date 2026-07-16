package prdraft

import (
	"encoding/json"
	"fmt"
)

// TitlePrompt requests the exact TitleDraft JSON shape.
func TitlePrompt(ctx Context) string {
	return prompt("title", ctx, `{"title":"imperative PR title, at most 72 characters"}`)
}

// BodyPrompt requests the exact BodyDraft JSON shape without test/review truth.
func BodyPrompt(ctx Context) string {
	return prompt("body", ctx, `{"summary_bullets":["..."],"test_plan_bullets":["..."],"reviewer_checklist":["..."],"release_note":"...","label_suggestions":["allowlisted-label"]}`)
}

func prompt(kind string, ctx Context, schema string) string {
	packet, _ := json.MarshalIndent(ctx, "", "  ")
	return fmt.Sprintf(`Draft bounded pull-request %s prose from the evidence packet.
Return exactly one JSON object matching this schema and no markdown fences:
%s

Rules:
- Do not add JSON fields.
- Do not prefix bullet strings with dashes.
- Mention only issue references, test identifiers, and backticked file paths present in the packet.
- Do not claim tests passed, review approval, or absence of findings; Go renders those facts.
- Do not emit secrets, credentials, styx attribution, or a co-author trailer.
- Suggest labels only from allowed_labels.

BEGIN DETERMINISTIC CONTEXT
%s
END DETERMINISTIC CONTEXT
`, kind, schema, packet)
}
