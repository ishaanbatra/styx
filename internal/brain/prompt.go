package brain

import "strings"

// systemPreamble explains the brain's job and the action vocabulary. Kept
// deliberately short: the brain must stay sub-second.
const systemPreamble = `You are the routing brain of styx, a personal AI dev orchestrator. For each user utterance, decide ONE action and emit ONLY JSON matching the provided schema.

Actions:
- reply: answer small talk, status questions, or anything you can answer from the context below. Put the answer in "reply".
- dispatch: send work to one agent thread. Pick thread + model tier per the capability cards.
- parallel_dispatch: send to 2+ threads when independent perspectives help (e.g. cross-review).
- pipeline: run a styx pipeline; "research" (deep research brief), "auto" (full plan-build-review cycle), "review" (code review of current diff), "intel" (refresh codebase intelligence).
- handoff: the user wants open-ended interactive collaboration ("let's work through this together") - open interactive claude on the thread.
- remember: the user states a durable fact, decision, or preference to keep ("note this", "remember that..."). Put it in "remember". If the user is correcting a routing choice you made, prefix it with "routing-preference: ".
- escalate: you are genuinely unsure how to route.

Model tiers for claude dispatches: opus = judgment-heavy work (brainstorm, architecture, planning, hard debugging) and complex implementation. sonnet = normal implementation, refactors, review. haiku = trivial classification. (There is also a "fable" tier for the most demanding work, but it is currently suspended and maps to opus - prefer opus.)
Set "confidence" to your honest routing confidence (0-1). Respect routing-preference memories - they are corrections from this user.

Capability cards:
`

// BuildPrompt renders a Turn into (system, user) prompts for the brain.
func BuildPrompt(t Turn) (string, string) {
	var sys strings.Builder
	sys.WriteString(systemPreamble)
	for _, c := range CondensedCards() {
		sys.WriteString("- ")
		sys.WriteString(c)
		sys.WriteString("\n")
	}

	var u strings.Builder
	if t.Summary != "" {
		u.WriteString("Conversation summary:\n" + t.Summary + "\n\n")
	}
	if len(t.RecentTurns) > 0 {
		u.WriteString("Recent turns:\n" + strings.Join(t.RecentTurns, "\n") + "\n\n")
	}
	if len(t.ThreadStatus) > 0 {
		u.WriteString("Live threads:\n" + strings.Join(t.ThreadStatus, "\n") + "\n\n")
	}
	if len(t.MemoryHits) > 0 {
		u.WriteString("Relevant memories:\n" + strings.Join(t.MemoryHits, "\n") + "\n\n")
	}
	u.WriteString("User utterance:\n" + t.Utterance)
	return sys.String(), u.String()
}
