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
		Condensed:     "claude - Claude Code CLI. Models by tier: fable (the most demanding judgment work), opus (deep planning, architecture, hard debugging, complex/ambiguous implementation), sonnet (reviews and claude-side implementation/refactors), haiku (cheap classify/distill). Best for: planning, architecture, debugging with repo context, ambiguous or multi-file work, and code review. Hand well-scoped implementation from a clear plan to codex (it is faster to a first diff); keep ambiguous/architectural implementation here. Supports per-thread persistent sessions and interactive handoff. Extra option --add-dir <path> for cross-repo work.",
		ExpectedFlags: []string{"--resume", "--output-format", "--model", "--add-dir", "--dangerously-skip-permissions"},
		ResumeProbe:   "--resume",
	},
	{
		CLI:           "codex",
		Bin:           "codex",
		Condensed:     "codex - OpenAI Codex CLI (gpt-5 class). PRIMARY IMPLEMENTER: best for applying well-scoped work from a clear plan or spec (fast to a first diff) - fixing tests, writing/refactoring functions, single-file or tightly-scoped multi-file edits; also algorithmic one-shots, sandboxed script checks, and second-opinion reviews. Headless `codex exec` (applies edits autonomously with `--sandbox workspace-write`). Persistent sessions via native exec resume; no interactive handoff - route ambiguous or architectural implementation to claude instead.",
		ExpectedFlags: []string{"exec", "--model", "--add-dir", "--json"},
		ResumeProbe:   "resume",
	},
	{
		CLI:           "agy",
		Bin:           "agy",
		Condensed:     "agy - Google Antigravity CLI (Gemini, 1M context). Best for: summarizing or explaining very large files/diffs, web-flavored research questions. Headless only; styx maintains conversation continuity for it.",
		ExpectedFlags: []string{"-p", "--add-dir"},
		// agy has --continue/--conversation <id> but never surfaces conversation IDs in --print output (google-antigravity/antigravity-cli#7) — headless resume stays impossible; styx-maintained summaries remain correct until that lands.
		ResumeProbe: "", // no known resume support; doctor reports degraded mode
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
