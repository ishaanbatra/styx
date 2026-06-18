package modelsync

import (
	"strings"
	"testing"
)

func TestMigrateText(t *testing.T) {
	src := `# keep this comment
[[rule]]
verb = "research.critic"
use  = "codex:gpt-5.5"
fallback = ["claude:opus-4-7", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "build"
use  = "claude:interactive"
parallel = ["claude:sonnet-4-6", "codex:gpt-5.5"]
`
	out, changes := MigrateText(src, []string{"opus", "sonnet", "haiku", "fable"})

	if !strings.Contains(out, `use  = "codex"`) {
		t.Errorf("codex not de-pinned:\n%s", out)
	}
	if !strings.Contains(out, `"claude:opus"`) || !strings.Contains(out, `"claude:sonnet"`) {
		t.Errorf("claude not de-pinned to alias:\n%s", out)
	}
	if !strings.Contains(out, "# keep this comment") {
		t.Error("comment not preserved")
	}
	if !strings.Contains(out, `"claude:interactive"`) {
		t.Error("claude:interactive must be left untouched")
	}
	if !strings.Contains(out, "ollama:qwen2.5-coder:14b") {
		t.Error("ollama token must be left untouched")
	}
	if len(changes) == 0 {
		t.Error("expected recorded changes")
	}

	out2, changes2 := MigrateText(out, []string{"opus", "sonnet", "haiku", "fable"})
	if out2 != out || len(changes2) != 0 {
		t.Errorf("migration not idempotent: %d changes", len(changes2))
	}
}
