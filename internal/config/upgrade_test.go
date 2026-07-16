package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureImplementRules_InjectsWhenAbsent(t *testing.T) {
	src := `[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
fallback = ["codex:gpt-5"]
`
	got, changed := EnsureImplementRules(src)
	if !changed {
		t.Fatal("expected changed=true when implement rule is absent")
	}
	if !strings.Contains(got, `verb = "implement"`) {
		t.Errorf("implement verb not injected:\n%s", got)
	}
	if !strings.Contains(got, "codex:gpt-5") {
		t.Error("expected codex as the primary implementer")
	}
	// The complex-signal rule must precede the catch-all so first-match picks it.
	complexIdx := strings.Index(got, `signals = ["complex"]`)
	catchAllIdx := strings.LastIndex(got, `verb = "implement"`)
	if complexIdx == -1 || complexIdx > catchAllIdx {
		t.Error("complex-signal implement rule must come before the catch-all implement rule")
	}
}

func TestEnsureImplementRules_IdempotentWhenPresent(t *testing.T) {
	src := `[[rule]]
verb = "implement"
use  = "codex:gpt-5"
fallback = ["claude:sonnet-4-6"]
`
	got, changed := EnsureImplementRules(src)
	if changed {
		t.Error("expected changed=false when implement rule already present")
	}
	if got != src {
		t.Error("content must be unchanged when implement rule already present")
	}
}

func TestEnsureDebugRules(t *testing.T) {
	src := `[[rule]]
verb = "research"
use = "agy:default"
`
	got, changed := EnsureDebugRules(src)
	if !changed {
		t.Fatal("expected debug rules to be injected")
	}
	for _, verb := range []string{"debug.sweep", "debug.review.codex", "debug.review.claude"} {
		if strings.Count(got, `verb = "`+verb+`"`) != 1 {
			t.Errorf("expected exactly one %s rule:\n%s", verb, got)
		}
	}
	again, changed := EnsureDebugRules(got)
	if changed || again != got {
		t.Fatal("second debug-rule upgrade must be a no-op")
	}
}

func TestEnsureDebugRulesPreservesCustomPartialRule(t *testing.T) {
	src := `[[rule]]
verb = "debug.sweep"
use = "claude:opus"
`
	got, changed := EnsureDebugRules(src)
	if !changed {
		t.Fatal("missing review rules must be injected")
	}
	if strings.Count(got, `verb = "debug.sweep"`) != 1 || !strings.Contains(got, `use = "claude:opus"`) {
		t.Fatalf("custom sweep rule was changed or duplicated:\n%s", got)
	}
}

func TestEnsureAgyModelPin(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		want        string
		wantChanged bool
	}{
		{
			name:        "use and fallback targets",
			content:     "use = \"agy:default\"\nfallback = [\"agy:default\", \"claude:sonnet\"]\n",
			want:        "use = \"agy:Gemini 3.1 Pro (High)\"\nfallback = [\"agy:Gemini 3.1 Pro (High)\", \"claude:sonnet\"]\n",
			wantChanged: true,
		},
		{
			name:        "single quoted target",
			content:     "use = 'agy:default'\n",
			want:        "use = 'agy:Gemini 3.1 Pro (High)'\n",
			wantChanged: true,
		},
		{
			name:        "already pinned",
			content:     "use = \"agy:Gemini 3.1 Pro (High)\"\n",
			want:        "use = \"agy:Gemini 3.1 Pro (High)\"\n",
			wantChanged: false,
		},
		{
			name:        "custom model and comment preserved",
			content:     "# previous use = \"agy:default\"\nuse = \"agy:Gemini Flash\"\n",
			want:        "# previous use = \"agy:default\"\nuse = \"agy:Gemini Flash\"\n",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := EnsureAgyModelPin(tt.content)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if got != tt.want {
				t.Errorf("content mismatch:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestRewriteRoutingGeminiToAgy(t *testing.T) {
	src := `[budget]
claude.cap_pct = 80
gemini_free.cap_pct = 70

[[rule]]
verb = "research"
use  = "gemini:flash"
fallback = ["gemini:pro", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
`
	got, n := RewriteRoutingGeminiToAgy(src)
	if n != 2 {
		t.Errorf("expected 2 substitutions (gemini:flash + gemini:pro), got %d", n)
	}
	if strings.Contains(got, "gemini:flash") {
		t.Error("gemini:flash still present after rewrite")
	}
	if strings.Contains(got, "gemini:pro") {
		t.Error("gemini:pro still present after rewrite")
	}
	if !strings.Contains(got, "agy:default") {
		t.Error("agy:default not present after rewrite")
	}
	if !strings.Contains(got, "migrated from gemini-cli to agy in v0.2") {
		t.Error("expected migration comment in output")
	}
}

func TestRewriteRoutingNoOp(t *testing.T) {
	src := `[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
`
	got, n := RewriteRoutingGeminiToAgy(src)
	if n != 0 {
		t.Errorf("expected 0 substitutions, got %d", n)
	}
	if got != src {
		t.Error("no-op rewrite should return original")
	}
}

func TestUpgrade_BackupsAndRewrites(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	routingDir := filepath.Join(dir, "styx")
	if err := os.MkdirAll(routingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	routingPath := filepath.Join(routingDir, "routing.toml")
	original := `[[rule]]
verb = "research"
use  = "gemini:flash"
`
	if err := os.WriteFile(routingPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	n, _, _, _, hostInjected, watchInjected, debugInjected, agyPinned, err := UpgradeRoutingFile(routingPath)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("expected 1 substitution, got %d", n)
	}
	if !watchInjected {
		t.Error("expected [watch] section to be injected on a config that lacks one")
	}
	if !hostInjected {
		t.Error("expected [conductor] host to be injected on a config that lacks one")
	}
	if !debugInjected {
		t.Error("expected debug rules to be injected on a pre-debug config")
	}
	if !agyPinned {
		t.Error("expected migrated agy route to receive the model pin")
	}
	// Backup exists
	backup := filepath.Join(routingDir, "routing.v0.1.toml.bak")
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("backup not created: %v", err)
	}
	// New file has the deterministic agy model pin.
	b, _ := os.ReadFile(routingPath)
	if !strings.Contains(string(b), agyPinnedTarget) {
		t.Errorf("post-upgrade file missing %s: %s", agyPinnedTarget, b)
	}
	for _, verb := range []string{"debug.sweep", "debug.review.codex", "debug.review.claude"} {
		if !strings.Contains(string(b), `verb = "`+verb+`"`) {
			t.Errorf("post-upgrade file missing %s: %s", verb, b)
		}
	}
}

func TestUpgrade_AgyModelPinRoundTrip(t *testing.T) {
	dir := t.TempDir()
	routingPath := filepath.Join(dir, "routing.toml")
	original := `[[rule]]
verb = "summarize"
use = "agy:default"
fallback = ["claude:sonnet"]
`
	if err := os.WriteFile(routingPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, _, _, _, _, _, pinned, err := UpgradeRoutingFile(routingPath)
	if err != nil {
		t.Fatal(err)
	}
	if !pinned {
		t.Fatal("expected upgrade to pin the unpinned agy route")
	}
	upgraded, err := os.ReadFile(routingPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(upgraded), "agy:default") || !strings.Contains(string(upgraded), agyPinnedTarget) {
		t.Fatalf("upgraded routing has wrong agy target:\n%s", upgraded)
	}
	routing, err := LoadRoutingFile(routingPath)
	if err != nil {
		t.Fatalf("upgraded routing must parse: %v", err)
	}
	if routing.Rules[0].Use != agyPinnedTarget {
		t.Errorf("round-tripped target = %q, want %q", routing.Rules[0].Use, agyPinnedTarget)
	}
	backup, err := os.ReadFile(filepath.Join(dir, "routing.v0.1.toml.bak"))
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != original {
		t.Errorf("backup = %q, want original %q", backup, original)
	}

	_, _, _, _, _, _, _, pinnedAgain, err := UpgradeRoutingFile(routingPath)
	if err != nil {
		t.Fatal(err)
	}
	if pinnedAgain {
		t.Fatal("second upgrade must be a no-op")
	}
}

func TestRewriteRouting_RemovesStaleGeminiBudgetKeys(t *testing.T) {
	src := `[budget]
claude.cap_pct = 80
gemini_free.cap_pct = 70
gemini_paid.cap_pct = 80

[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
`
	got, _ := RewriteRoutingGeminiToAgy(src)
	if strings.Contains(got, "gemini_free.cap_pct") {
		t.Error("gemini_free.cap_pct still present after rewrite")
	}
	if strings.Contains(got, "gemini_paid.cap_pct") {
		t.Error("gemini_paid.cap_pct still present after rewrite")
	}
	if !strings.Contains(got, "agy.cap_pct = 80") {
		t.Error("agy.cap_pct = 80 not present after rewrite")
	}
}

func TestRewriteRouting_DedupesAgyFallback(t *testing.T) {
	src := `[[rule]]
verb = "research"
use  = "agy:default"
fallback = ["agy:default", "agy:default", "ollama:qwen2.5-coder:14b"]
`
	got, _ := RewriteRoutingGeminiToAgy(src)
	// Should dedupe to exactly one agy:default in the fallback
	want := `fallback = ["agy:default", "ollama:qwen2.5-coder:14b"]`
	if !strings.Contains(got, want) {
		t.Errorf("fallback not deduped; output:\n%s", got)
	}
}

func TestRewriteRouting_SeedsMessageLimits(t *testing.T) {
	src := `[budget]
claude.cap_pct = 80

[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
`
	// First pass: should inject message-limit keys for claude, codex, agy
	got1, _ := RewriteRoutingGeminiToAgy(src)

	checks := []string{
		"claude.messages_per_5h   = 45",
		"claude.messages_per_week = 225",
		"codex.messages_per_5h   = 50",
		"codex.messages_per_week = 250",
		"agy.messages_per_5h   = 100",
		"agy.messages_per_week = 500",
	}
	for _, want := range checks {
		if !strings.Contains(got1, want) {
			t.Errorf("first pass: expected %q in output; got:\n%s", want, got1)
		}
	}

	// Second pass: idempotent — running on already-migrated output must not duplicate keys
	got2, _ := RewriteRoutingGeminiToAgy(got1)
	for _, key := range []string{"claude.messages_per_5h", "codex.messages_per_5h", "agy.messages_per_5h"} {
		count := strings.Count(got2, key)
		if count != 1 {
			t.Errorf("idempotency: %q appears %d times after second pass (want 1):\n%s", key, count, got2)
		}
	}
}

func TestEnsureFableTier(t *testing.T) {
	seeded := "[tiers]\nfable  = \"opus\"\nopus   = \"opus\"\n"
	got, changed := EnsureFableTier(seeded)
	if !changed || !strings.Contains(got, `fable  = "fable"`) {
		t.Fatalf("seeded fable mapping must upgrade, got changed=%v:\n%s", changed, got)
	}
	// Idempotent.
	again, changed2 := EnsureFableTier(got)
	if changed2 || again != got {
		t.Fatal("second run must be a no-op")
	}
	// User customization is respected.
	custom := "[tiers]\nfable  = \"sonnet\"\n"
	_, changed3 := EnsureFableTier(custom)
	if changed3 {
		t.Fatal("user-customized fable mapping must be left alone")
	}
}

func TestEnsureConductorTaskCap(t *testing.T) {
	// Seeded-shape config: knob injected inside the existing [conductor] section.
	seeded := "[tiers]\nfable  = \"fable\"\n\n[conductor]\nship_gate = \"handshake\"\n"
	got, changed := EnsureConductorTaskCap(seeded)
	if !changed || !strings.Contains(got, "max_background_tasks = 4") {
		t.Fatalf("must inject the cap knob, got changed=%v:\n%s", changed, got)
	}
	if strings.Index(got, "[conductor]") > strings.Index(got, "max_background_tasks") {
		t.Fatalf("knob must land inside the [conductor] section:\n%s", got)
	}
	// Idempotent.
	again, changed2 := EnsureConductorTaskCap(got)
	if changed2 || again != got {
		t.Fatal("second run must be a no-op")
	}
	// User customization is respected.
	custom := "[conductor]\nship_gate = \"off\"\nmax_background_tasks = 2\n"
	if _, changed3 := EnsureConductorTaskCap(custom); changed3 {
		t.Fatal("a config already carrying the knob must be left alone")
	}
	// Config with no [conductor] section at all: whole section appended.
	bare := "[tiers]\nfable  = \"fable\"\n"
	got4, changed4 := EnsureConductorTaskCap(bare)
	if !changed4 || !strings.Contains(got4, "[conductor]") || !strings.Contains(got4, "max_background_tasks = 4") {
		t.Fatalf("missing section must be appended whole:\n%s", got4)
	}
}

func TestEnsureConductorHost(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantChanged bool
		wantHost    string
	}{
		{
			name:        "existing section receives default",
			content:     "[conductor]\nship_gate = \"handshake\"\n",
			wantChanged: true,
			wantHost:    `host = "claude"`,
		},
		{
			name:        "custom host is preserved",
			content:     "[conductor]\nhost = \"codex\"\n",
			wantChanged: false,
			wantHost:    `host = "codex"`,
		},
		{
			name:        "missing section is appended",
			content:     "[tiers]\nfable = \"fable\"\n",
			wantChanged: true,
			wantHost:    `host = "claude"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := EnsureConductorHost(tt.content)
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if !strings.Contains(got, tt.wantHost) {
				t.Errorf("output missing %q:\n%s", tt.wantHost, got)
			}
			again, changedAgain := EnsureConductorHost(got)
			if changedAgain || again != got {
				t.Error("second run must be a no-op")
			}
		})
	}
}

func TestEnsureWatchSection(t *testing.T) {
	// No [watch] section at all: whole section appended.
	bare := "[tiers]\nfable  = \"fable\"\n"
	got, changed := EnsureWatchSection(bare)
	if !changed {
		t.Fatal("expected changed=true when [watch] section is absent")
	}
	for _, want := range []string{"[watch]", "stall_threshold_seconds = 90", "interval_seconds = 15", "ollama_enabled = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in output; got:\n%s", want, got)
		}
	}
	// Idempotent: already-present [watch] section is left alone.
	again, changed2 := EnsureWatchSection(got)
	if changed2 || again != got {
		t.Fatal("second run must be a no-op")
	}
	// User customization is respected: a differently-configured [watch]
	// section must not be duplicated or altered.
	custom := "[watch]\nstall_threshold_seconds = 30\ninterval_seconds = 5\nollama_enabled = false\n"
	gotCustom, changedCustom := EnsureWatchSection(custom)
	if changedCustom || gotCustom != custom {
		t.Fatal("a config already carrying a [watch] section must be left alone")
	}
}
