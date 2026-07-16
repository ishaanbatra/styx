package deadcode

import (
	"encoding/json"
	"fmt"
	"strings"
)

func sweepPrompt(in Input) string {
	return fmt.Sprintf(`You are performing a repository-wide dead-code sweep. Read the whole repository,
with special attention to the requested target below, and identify unused files,
functions, and imports. DO NOT edit files or run destructive commands. This is a
read-only evidence pass.

Return ONLY one JSON object with this exact shape (no markdown fence or prose):
{"findings":[{"kind":"file|function|import","symbol":"exact searchable token","definition":{"path":"repo/relative/path","line":1},"reason":"short evidence-based explanation"}]}

Rules:
- Every finding must have exactly one kind: file, function, or import.
- symbol must be the exact token that a whole-word repository search can verify.
- definition.path must be relative to the repository root, never absolute.
- definition.line must be the 1-based line containing the definition/import. For
  a whole-file finding, use the most representative declaration line (or line 1).
- Report only findings you investigated. An empty findings array is valid.
- Do not include comments, trailing commas, markdown, or additional top-level keys.

--- REQUESTED TARGET ---
%s`, in.Target)
}

func reviewPrompt(sample []VerifiedFinding) string {
	b, _ := json.MarshalIndent(sample, "", "  ")
	var prompt strings.Builder
	prompt.WriteString(`You are doing ONE short Codex spot-check of dead-code findings that a deterministic
whole-word repository scan marked CONFIRMED. Read the repository at the cited
paths and inspect a representative sample below. Check whether language/runtime
mechanisms, generated references, interfaces, reflection, registration, build
tags, or entry points make any finding live despite the text scan.

Do not edit files. Return concise markdown with a verdict for every sampled
finding: UPHELD, OVERTURNED, or UNCERTAIN, plus file:line evidence.

--- CONFIRMED SAMPLE (JSON) ---
`)
	prompt.Write(b)
	return prompt.String()
}
