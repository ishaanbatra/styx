package brain

import "testing"

// TestBrainPromptFitsDefaultContextOrSetsNumCtx is a regression audit: it
// measures the real brain prompt size so a future capability-card sprawl is
// visible in test output. The payload test (TestDecideEmitsKeepAlive /
// chat()'s num_ctx sizing) is the actual gate against ollama's 4096 default.
func TestBrainPromptFitsDefaultContextOrSetsNumCtx(t *testing.T) {
	sys, user := BuildPrompt(Turn{Utterance: "fix the flaky test"})
	est := (len(sys) + len(user)) / 4
	t.Logf("brain prompt ≈ %d tokens", est)
	// The chat() sizing rule must engage before ollama's 4096 default truncates.
	if est+1024 > 4096 {
		t.Log("prompt exceeds ollama's default window — chat() must set num_ctx (asserted in the payload test)")
	}
}
