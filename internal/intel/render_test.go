package intel

import (
	"strings"
	"testing"
	"time"
)

func TestToMarkdown_ContainsAllSections(t *testing.T) {
	idx := &Index{
		Project:  "demo",
		Language: "python",
		BuiltAt:  time.Date(2026, 5, 27, 18, 30, 0, 0, time.UTC),
		GitHead:  "abc1234",
		Conventions: Conventions{
			TestFramework: "pytest",
			TypeSystem:    "mypy strict",
			Naming:        "snake_case",
			Imports:       "absolute",
		},
		Modules: []Module{
			{Path: "app/routes", Purpose: "FastAPI handlers", EntryPoints: []string{"chat.py:chat"}},
		},
		KeySymbols: []KeySymbol{
			{Name: "ChatService", File: "app/services/chat.py:42", Why: "central streaming"},
		},
		RecentCommits: []Commit{
			{SHA: "abc1234", Subject: "feat: streaming", FilesTouched: 3},
		},
		OpenTodos: []Todo{
			{File: "app/routes/chat.py:88", Text: "TODO: rate-limit"},
		},
	}
	out := ToMarkdown(idx)
	for _, must := range []string{
		"# Project Context",
		"demo",
		"pytest",
		"mypy strict",
		"## Module map",
		"app/routes",
		"## Key symbols",
		"ChatService",
		"## Recent activity",
		"feat: streaming",
		"## Open TODOs",
		"rate-limit",
	} {
		if !strings.Contains(out, must) {
			t.Errorf("rendered output missing %q. full output:\n%s", must, out)
		}
	}
}

func TestToMarkdown_EmptySectionsOmitted(t *testing.T) {
	idx := &Index{
		Project:  "minimal",
		Language: "go",
		BuiltAt:  time.Now(),
		Conventions: Conventions{TestFramework: "go test"},
	}
	out := ToMarkdown(idx)
	if strings.Contains(out, "## Module map") {
		t.Error("empty modules should not produce ## Module map section")
	}
	if !strings.Contains(out, "go test") {
		t.Error("test framework should still be rendered")
	}
}
