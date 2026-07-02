package launcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeClaude(t *testing.T) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile
}

func TestClaudeHostLaunch(t *testing.T) {
	// paths.StateDir() resolves under paths.ConfigDir(), which honors
	// XDG_CONFIG_HOME (there is no XDG_STATE_HOME in internal/paths — see
	// paths_test.go's TestConfigDir_RespectsXDG / TestUsageDBPath).
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin, argsFile := fakeClaude(t)
	proj := t.TempDir()
	h := &ClaudeHost{Bin: bin}
	err := h.Launch(context.Background(), Opts{
		ProjectPath: proj, StyxBin: "/usr/local/bin/styx", Guidance: "GUIDE ME",
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake claude never ran: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")

	var cfgPath string
	for i, a := range args {
		if a == "--mcp-config" && i+1 < len(args) {
			cfgPath = args[i+1]
		}
	}
	if cfgPath == "" {
		t.Fatalf("missing --mcp-config in %v", args)
	}
	cfg, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("mcp config not written: %v", err)
	}
	var parsed struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatalf("config not valid JSON: %v", err)
	}
	s, ok := parsed.MCPServers["styx"]
	if !ok || s.Command != "/usr/local/bin/styx" || len(s.Args) == 0 || s.Args[0] != "mcp" {
		t.Fatalf("styx server entry wrong: %+v", parsed)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--append-system-prompt GUIDE ME") {
		t.Fatalf("guidance not injected: %v", args)
	}
}

func TestClaudeHostLaunch_ExtraRepos(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	bin, argsFile := fakeClaude(t)
	proj := t.TempDir()
	extra := t.TempDir()
	h := &ClaudeHost{Bin: bin}
	err := h.Launch(context.Background(), Opts{
		ProjectPath: proj, StyxBin: "/usr/local/bin/styx", Guidance: "G",
		ExtraRepos: []string{extra},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake claude never ran: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var found bool
	for i, a := range args {
		if a == "--add-dir" && i+1 < len(args) && args[i+1] == extra {
			found = true
		}
	}
	if !found {
		t.Fatalf("extra repo not passed: %v", args)
	}
}

func TestClaudeHostName(t *testing.T) {
	h := &ClaudeHost{}
	if h.Name() != "claude" {
		t.Fatalf("Name() = %q, want claude", h.Name())
	}
}
