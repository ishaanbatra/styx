package mapimpact

import (
	"encoding/json"
	"fmt"
	"strings"
)

func sweepPrompt(in Input) string {
	target, _ := json.Marshal(in)
	return fmt.Sprintf(`You are performing a repository-wide dependency and change-impact trace.
Read the whole repository and start from the exact input below. Identify direct
dependents first, then evidence-supported transitive impact. For a diff input,
inspect the named git diff/ref and trace the changed symbols and files outward.
DO NOT edit files or run destructive commands. This is a read-only evidence pass.

Return ONLY one JSON object with this exact shape (no markdown fence or prose):
{"findings":[{"source":{"path":"repo/relative/path","line":1,"symbol":"changed or depended-on symbol"},"dependent":{"path":"repo/relative/path","line":1,"symbol":"referencing dependent symbol"},"relationship":"calls|imports|implements|configures|tests|other","impact":"direct|transitive","reason":"short explanation of the concrete dependency evidence"}]}

Rules:
- Every finding is one directed edge: dependent references or relies on source.
- source and dependent paths must be repository-relative and lines are 1-based.
- Cite the line where the dependency is visible, not merely a nearby declaration.
- impact is direct only for the first hop from the input; later hops are transitive.
- Do not claim an edge from naming similarity alone. Follow imports, calls, types,
  interfaces, registrations, configuration, generated wiring, tests, and build rules.
- Deduplicate identical edges. An empty findings array is valid.
- Do not include comments, trailing commas, markdown, or additional top-level keys.

--- INPUT (JSON) ---
%s`, target)
}

func reviewPrompt(sample []Finding) string {
	b, _ := json.MarshalIndent(sample, "", "  ")
	var prompt strings.Builder
	prompt.WriteString(`You are doing ONE bounded Codex spot-check of dependency edges claimed by an
agy repository impact sweep. Read the repository at the cited paths. For every
sampled edge, answer the concrete question: does the dependent site actually
reference or rely on the source site in the claimed way? Check aliases,
interfaces, generated wiring, registrations, configuration, tests, and build
rules; do not accept naming similarity as evidence.

Do not edit files. Return concise markdown with a verdict for every sampled
edge: VERIFIED, REFUTED, or UNCERTAIN, plus file:line evidence.

--- CLAIMED DEPENDENCY EDGES (JSON) ---
`)
	prompt.Write(b)
	return prompt.String()
}
