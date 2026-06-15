package brain

// Card encodes curated expert knowledge of one CLI's surface. Condensed is
// injected into the brain's system prompt every turn; ExpectedFlags and
// ResumeProbe are used by `styx doctor` to detect knowledge drift.
type Card struct {
	CLI           string
	Bin           string   // binary to probe; "" = no binary (ollama probed via HTTP)
	Condensed     string   // what the brain sees
	ExpectedFlags []string // doctor checks --help contains each of these
	ResumeProbe   string   // substring of --help proving session-resume support
}

// Cards is the curated capability set, one per dispatchable thread kind.
var Cards = []Card{
	{
		CLI:           "claude",
		Bin:           "claude",
		Condensed:     "claude - Claude Code CLI. Models by tier: opus (deep planning, architecture, hard debugging, complex implementation - the top callable tier), sonnet (default implementation/review), haiku (cheap classify/distill). Best for: multi-file implementation, debugging with repo context, planning, code review. Supports per-thread persistent sessions and interactive handoff. Extra option --add-dir <path> for cross-repo work. (A 'fable' tier exists for the most demanding work but is currently suspended and maps to opus - prefer opus.)",
		ExpectedFlags: []string{"--resume", "--output-format", "--model", "--add-dir", "--dangerously-skip-permissions"},
		ResumeProbe:   "--resume",
	},
	{
		CLI:           "codex",
		Bin:           "codex",
		Condensed:     "codex - OpenAI Codex CLI (gpt-5 class). Best for: sandboxed script checks, quick second-opinion code reviews, algorithmic one-shots, cross-checking claude's work. Styx v1 dispatches it headlessly with `codex exec`; no interactive handoff from styx.",
		ExpectedFlags: []string{"exec", "--model", "--add-dir"},
		ResumeProbe:   "resume",
	},
	{
		CLI:           "agy",
		Bin:           "agy",
		Condensed:     "agy - Google Antigravity CLI (Gemini, 1M context). Best for: summarizing or explaining very large files/diffs, web-flavored research questions. Headless only; styx maintains conversation continuity for it.",
		ExpectedFlags: []string{"-p", "--add-dir"},
		ResumeProbe:   "", // no known resume support; doctor reports degraded mode
	},
	{
		CLI:       "ollama",
		Bin:       "",
		Condensed: "ollama - local models, free and instant. Models: qwen2.5-coder:7b (trivial grunt), qwen2.5-coder:14b (better grunt/summarize). Best for: summaries, commit messages, classification, boilerplate one-shots. NEVER for real implementation, planning, or anything needing accuracy.",
	},
}

// CondensedCards returns each card's brain-facing text.
func CondensedCards() []string {
	out := make([]string, 0, len(Cards))
	for _, c := range Cards {
		out = append(out, c.Condensed)
	}
	return out
}

// CardFor returns the card for a CLI name (nil if unknown).
func CardFor(cli string) *Card {
	for i := range Cards {
		if Cards[i].CLI == cli {
			return &Cards[i]
		}
	}
	return nil
}
