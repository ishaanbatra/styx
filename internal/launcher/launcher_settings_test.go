package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildConductorSettingsModes(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		wantPre  bool
		wantPost bool
	}{
		{name: "off", mode: "off"},
		{name: "audit", mode: "audit", wantPost: true},
		{name: "block", mode: "block", wantPre: true, wantPost: true},
		{name: "unknown fails closed", mode: "surprise", wantPre: true, wantPost: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := buildConductorSettings("/usr/local/bin/styx", tt.mode)
			if got, ok := s["includeCoAuthoredBy"].(bool); !ok || got {
				t.Fatalf("includeCoAuthoredBy = %v, want false", s["includeCoAuthoredBy"])
			}
			hooks, hasHooks := s["hooks"].(map[string]any)
			if !tt.wantPre && !tt.wantPost {
				if hasHooks {
					t.Fatalf("mode %q must omit hooks, got %v", tt.mode, hooks)
				}
				return
			}
			if !hasHooks {
				t.Fatalf("mode %q has no hooks map: %v", tt.mode, s)
			}
			if _, ok := hooks["PreToolUse"]; ok != tt.wantPre {
				t.Errorf("PreToolUse present = %v, want %v", ok, tt.wantPre)
			}
			if _, ok := hooks["PostToolUse"]; ok != tt.wantPost {
				t.Errorf("PostToolUse present = %v, want %v", ok, tt.wantPost)
			}
		})
	}

	// The block-mode PreToolUse matcher must funnel MCP tools (so MCP web
	// tools reach styx hook) and use shell-free exec form.
	raw, _ := json.Marshal(buildConductorSettings("/usr/local/bin/styx", "block"))
	if !strings.Contains(string(raw), "mcp__") {
		t.Errorf("matcher must include mcp__ funnel; got %s", raw)
	}
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

func TestWriteConductorSettingsOffWritesAttributionOnly(t *testing.T) {
	dir := t.TempDir()
	path, err := writeConductorSettings(dir, "/bin/styx", "off")
	if err != nil {
		t.Fatalf("off mode: %v", err)
	}
	if path != filepath.Join(dir, "conductor-settings.json") {
		t.Errorf("off mode returned path %q, want conductor settings path", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("settings not valid JSON: %v", err)
	}
	if got, ok := parsed["includeCoAuthoredBy"].(bool); !ok || got {
		t.Fatalf("includeCoAuthoredBy = %v, want false", parsed["includeCoAuthoredBy"])
	}
	if _, ok := parsed["hooks"]; ok {
		t.Fatalf("off mode must omit hooks, got %v", parsed["hooks"])
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

func TestClaudeArgsSettingsInAllModesAndNoStrict(t *testing.T) {
	o := Opts{Guidance: "guide", ExtraRepos: []string{"/repo2"}, ResumeArgs: []string{"--continue"}}

	for _, mode := range []string{"off", "audit", "block", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			settingsPath, err := writeConductorSettings(dir, "/bin/styx", mode)
			if err != nil {
				t.Fatalf("write settings: %v", err)
			}
			args := claudeArgs("/cfg.json", settingsPath, o)
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--settings "+settingsPath) {
				t.Errorf("expected --settings in argv, got %v", args)
			}
		})
	}

	args := claudeArgs("/cfg.json", "/settings.json", o)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--mcp-config /cfg.json") {
		t.Errorf("expected --mcp-config in argv, got %v", args)
	}
	if !strings.Contains(joined, "--add-dir /repo2") {
		t.Errorf("expected extra repo --add-dir, got %v", args)
	}
	if args[len(args)-1] != "--continue" {
		t.Errorf("ResumeArgs must be last, got %v", args)
	}
	// Locked decision: we do NOT strip the user's other MCP servers.
	if strings.Contains(joined, "--strict-mcp-config") {
		t.Errorf("--strict-mcp-config must be absent, got %v", args)
	}
}
