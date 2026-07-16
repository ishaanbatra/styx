package crossrepo

import (
	"encoding/json"
	"fmt"
	"strings"
)

func sweepPrompt(roots []string, question string) string {
	rootJSON, _ := json.Marshal(roots)
	if question == "" {
		question = "Identify concrete API, contract, build, configuration, data-format, and runtime producer/consumer links between these repositories."
	}
	return fmt.Sprintf(`You are performing a STRICTLY READ-ONLY cross-repository relationship analysis.
Do not create, edit, rename, delete, format, generate, install, or run commands that change files in any repository.

The only mounted repository roots are exactly:
%s

Question:
%s

Trace concrete relationships that cross from one mounted repository to another. Prefer API and consumer links supported by exact code sites. Do not report relationships wholly inside one repository. Do not invent roots or use paths outside the mounted roots.

Return ONLY one JSON object with this shape:
{"findings":[{"producer":{"root":"exact mounted root","path":"repo/relative/file","line":1,"symbol":"exported API or producer"},"consumer":{"root":"different exact mounted root","path":"repo/relative/file","line":1,"symbol":"consumer"},"relationship":"calls|imports|implements|depends-on|publishes|consumes|configures|generates|tests|other","contract":"specific API, protocol, schema, package, event, or configuration contract","reason":"concise evidence explaining the link"}]}

Use 1-based lines and exact root strings from the list. Use an empty findings array when there is no supported cross-repository link.`, string(rootJSON), strings.TrimSpace(question))
}

func reviewPrompt(sample []Finding) string {
	payload, _ := json.Marshal(sample)
	return fmt.Sprintf(`Read-only spot-check. Inspect the cited files in the mounted repositories and assess each claimed cross-repository producer/consumer link. Do not modify files or run commands that change them.

For each sampled link, determine whether the consumer site actually relies on the cited producer API/contract. Return a concise numbered list with VERIFIED, REFUTED, or UNCERTAIN and exact code evidence. Check directionality, indirect/generated clients, schemas, version skew, configuration, and false name matches.

Bounded sample JSON:
%s`, payload)
}
