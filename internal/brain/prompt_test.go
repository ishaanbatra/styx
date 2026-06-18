package brain

import (
	"strings"
	"testing"
)

func TestCardsCoverAllThreads(t *testing.T) {
	want := []string{"claude", "codex", "agy", "ollama"}
	for _, w := range want {
		found := false
		for _, c := range Cards {
			if c.CLI == w {
				found = true
				if c.Condensed == "" {
					t.Errorf("card %s has empty Condensed text", w)
				}
			}
		}
		if !found {
			t.Errorf("no capability card for %s", w)
		}
	}
}

func TestBuildPrompt(t *testing.T) {
	turn := Turn{
		Utterance:     "fix the flaky session test",
		Summary:       "we are refactoring the session loader",
		RecentTurns:   []string{"user: hello", "styx: hi"},
		ThreadStatus:  []string{"claude (claude): 3 turns, context 41%"},
		MemoryHits:    []string{"[decision] use sqlite for memory"},
		BoundProjects: []string{"ai-ta-backend (python): embedding + RAG service [bound]"},
		KnownProjects: []string{"ai-ta-teacher-ui (typescript): teacher upload UI"},
	}
	sys, user := BuildPrompt(turn)
	if !strings.Contains(sys, "routing brain") {
		t.Errorf("system prompt missing role statement:\n%s", sys)
	}
	// Every condensed card must reach the model every turn.
	for _, c := range Cards {
		if !strings.Contains(sys, c.Condensed) {
			t.Errorf("system prompt missing card for %s", c.CLI)
		}
	}
	for _, want := range []string{
		"fix the flaky session test",
		"we are refactoring the session loader",
		"user: hello",
		"claude (claude): 3 turns, context 41%",
		"[decision] use sqlite for memory",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing %q:\n%s", want, user)
		}
	}
	for _, want := range []string{
		"ai-ta-backend (python): embedding + RAG service [bound]",
		"ai-ta-teacher-ui (typescript): teacher upload UI",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing project line %q:\n%s", want, user)
		}
	}
}
