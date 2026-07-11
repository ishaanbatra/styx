package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildConductorSettingsBlock(t *testing.T) {
	s := buildConductorSettings("/usr/local/bin/styx", "block")
	hooks, ok := s["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks map: %v", s)
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Errorf("block mode must install PreToolUse")
	}
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Errorf("block mode must install PostToolUse")
	}
	// The PreToolUse matcher must funnel MCP tools (so MCP web tools reach styx hook).
	raw, _ := json.Marshal(s)
	if !strings.Contains(string(raw), "mcp__") {
		t.Errorf("matcher must include mcp__ funnel; got %s", raw)
	}
	// Exec form: bin + args array, no shell, no quoting — portable to
	// Windows where hooks otherwise run under Git Bash/PowerShell.
	if strings.Contains(string(raw), "styx' hook") {
		t.Errorf("hook must use exec form (args array), not a shell-quoted string; got %s", raw)
	}
	if !strings.Contains(string(raw), `"command":"/usr/local/bin/styx"`) {
		t.Errorf("hook command must be the bare binary path; got %s", raw)
	}
	if !strings.Contains(string(raw), `"args":["hook","pretooluse"]`) {
		t.Errorf("PreToolUse hook must carry args [hook pretooluse]; got %s", raw)
	}
}

func TestHookMatcherExecFormSurvivesSpecialPaths(t *testing.T) {
	cases := []struct{ name, bin string }{
		{"spaces", "/Users/dev name/bin/styx"},
		{"single quote", "/Users/o'brien/bin/styx"},
		{"windows backslashes", `C:\Users\dev\bin\styx.exe`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := hookMatcher("Bash", c.bin, "posttooluse")
			hook := m["hooks"].([]any)[0].(map[string]any)
			if hook["command"] != c.bin {
				t.Errorf("command = %q, want the untouched bin path %q", hook["command"], c.bin)
			}
			args, ok := hook["args"].([]string)
			if !ok || len(args) != 2 || args[0] != "hook" || args[1] != "posttooluse" {
				t.Errorf("args = %v, want [hook posttooluse]", hook["args"])
			}
		})
	}
}

func TestBuildConductorSettingsAudit(t *testing.T) {
	s := buildConductorSettings("/usr/local/bin/styx", "audit")
	hooks := s["hooks"].(map[string]any)
	if _, ok := hooks["PreToolUse"]; ok {
		t.Errorf("audit mode must NOT install PreToolUse (never blocks)")
	}
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Errorf("audit mode must install PostToolUse")
	}
}

func TestWriteConductorSettingsOffWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path, err := writeConductorSettings(dir, "/bin/styx", "off")
	if err != nil {
		t.Fatalf("off mode: %v", err)
	}
	if path != "" {
		t.Errorf("off mode returned path %q, want empty", path)
	}
	if _, err := os.Stat(filepath.Join(dir, "conductor-settings.json")); !os.IsNotExist(err) {
		t.Errorf("off mode must not write a settings file")
	}
}

func TestWriteConductorSettingsBlockWritesFile(t *testing.T) {
	dir := t.TempDir()
	path, err := writeConductorSettings(dir, "/bin/styx", "block")
	if err != nil {
		t.Fatalf("block mode: %v", err)
	}
	if path == "" {
		t.Fatal("block mode returned empty path")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("settings not valid JSON: %v", err)
	}
}

func TestClaudeArgsSettingsAndNoStrict(t *testing.T) {
	o := Opts{Guidance: "guide", ExtraRepos: []string{"/repo2"}, ExtraArgs: []string{"--continue"}}

	// With a settings path, --settings is present.
	args := claudeArgs("/cfg.json", "/settings.json", o)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--settings /settings.json") {
		t.Errorf("expected --settings in argv, got %v", args)
	}
	if !strings.Contains(joined, "--mcp-config /cfg.json") {
		t.Errorf("expected --mcp-config in argv, got %v", args)
	}
	if !strings.Contains(joined, "--add-dir /repo2") {
		t.Errorf("expected extra repo --add-dir, got %v", args)
	}
	if args[len(args)-1] != "--continue" {
		t.Errorf("ExtraArgs must be last, got %v", args)
	}
	// Locked decision: we do NOT strip the user's other MCP servers.
	if strings.Contains(joined, "--strict-mcp-config") {
		t.Errorf("--strict-mcp-config must be absent, got %v", args)
	}

	// Without a settings path (off mode), --settings is absent.
	if strings.Contains(strings.Join(claudeArgs("/cfg.json", "", o), " "), "--settings") {
		t.Errorf("no --settings expected when settingsPath is empty")
	}
}
