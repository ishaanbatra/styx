// Command braindump emits the exact prompt components the styx routing brain
// sends to ollama, so the promptfoo eval harness can byte-match real behaviour
// (see eval/promptfoo/README.md). It is a dev tool: not built into ./bin/styx.
//
// Usage (run from repo root):
//
//	go run ./eval/promptfoo/braindump -outdir eval/promptfoo/generated
//
// It writes (all CODE-MIRRORED, safe to regenerate any time):
//   - cards_block.txt        the "- <Condensed>\n" block BuildPrompt appends
//   - schema.json            ActionSchema verbatim (ollama `format`)
//   - preamble_shipped.txt   the CURRENT systemPreamble (preamble == sys minus cards)
//
// preamble_shipped.txt lets the harness verify that the shipped prompt still
// matches a variant (variants/v5.txt == preamble_shipped.txt today). The frozen
// pre-iteration baseline lives in variants/baseline.txt and is NOT regenerated.
// Regenerate after any edit to cards.go or prompt.go so the eval never drifts
// from internal/brain.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/brain"
)

func main() {
	outdir := flag.String("outdir", "eval/promptfoo/generated", "directory for generated artifacts")
	flag.Parse()

	if err := os.MkdirAll(*outdir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	// Reconstruct the cards block exactly as BuildPrompt does.
	var cards strings.Builder
	for _, c := range brain.CondensedCards() {
		cards.WriteString("- ")
		cards.WriteString(c)
		cards.WriteString("\n")
	}
	cardsBlock := cards.String()

	// The baseline system prompt is preamble + cardsBlock; recover the preamble
	// by trimming the cards block off the full system prompt.
	sys, _ := brain.BuildPrompt(brain.Turn{Utterance: "PLACEHOLDER"})
	preamble := strings.TrimSuffix(sys, cardsBlock)

	write := func(name, content string) {
		p := filepath.Join(*outdir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", p, len(content), "bytes")
	}

	write("cards_block.txt", cardsBlock)
	write("preamble_shipped.txt", preamble)
	write("schema.json", string(brain.ActionSchema))
}
