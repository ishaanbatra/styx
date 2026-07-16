package launcher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fakeCodex(t *testing.T) (bin, argsFile, cwdFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	cwdFile = filepath.Join(dir, "cwd.txt")
	t.Setenv("STYX_TEST_CODEX_ARGS", argsFile)
	t.Setenv("STYX_TEST_CODEX_CWD", cwdFile)
	bin = filepath.Join(dir, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STYX_TEST_CODEX_ARGS\"\npwd > \"$STYX_TEST_CODEX_CWD\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argsFile, cwdFile
}

func TestCodexHostLaunch(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	bin, argsFile, cwdFile := fakeCodex(t)
	projectPath := t.TempDir()
	h := &CodexHost{Bin: bin}
	err := h.Launch(context.Background(), Opts{
		ProjectPath: projectPath,
		StyxBin:     `/opt/styx bin/styx`,
		Guidance:    "say \"hi\"\nuse C:\\repo",
		ExtraRepos:  []string{"/repo/two"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	rawArgs, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("fake codex never ran: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(rawArgs)), "\n")
	want := []string{
		"-c", `mcp_servers.styx.command="/opt/styx bin/styx"`,
		"-c", `mcp_servers.styx.args=["mcp"]`,
		"-c", `developer_instructions="say \"hi\"\nuse C:\\repo"`,
		"--add-dir", "/repo/two",
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("argv = %#v, want %#v", args, want)
	}
	rawCWD, err := os.ReadFile(cwdFile)
	if err != nil {
		t.Fatalf("read fake codex cwd: %v", err)
	}
	if got := strings.TrimSpace(string(rawCWD)); got != projectPath {
		t.Errorf("cwd = %q, want %q", got, projectPath)
	}
	entries, err := os.ReadDir(configHome)
	if err != nil {
		t.Fatalf("read config home: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("Codex launch wrote user config state: %v", entries)
	}
}

func TestCodexArgs(t *testing.T) {
	tests := []struct {
		name string
		opts Opts
		want []string
	}{
		{
			name: "launch with encoded guidance and extra repos",
			opts: Opts{
				StyxBin:    `/usr/local/bin/styx`,
				Guidance:   "say \"hi\"\nuse C:\\repo",
				ExtraRepos: []string{"/repo/two", "/repo/three"},
			},
			want: []string{
				"-c", `mcp_servers.styx.command="/usr/local/bin/styx"`,
				"-c", `mcp_servers.styx.args=["mcp"]`,
				"-c", `developer_instructions="say \"hi\"\nuse C:\\repo"`,
				"--add-dir", "/repo/two", "--add-dir", "/repo/three",
			},
		},
		{
			name: "resume subcommand comes first",
			opts: Opts{
				StyxBin:    "/bin/styx",
				Guidance:   "guide",
				ResumeArgs: []string{"resume", "session-1"},
			},
			want: []string{
				"resume", "session-1",
				"-c", `mcp_servers.styx.command="/bin/styx"`,
				"-c", `mcp_servers.styx.args=["mcp"]`,
				"-c", `developer_instructions="guide"`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := codexArgs(tt.opts); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("codexArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestTOMLBasicString(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"plain", "guidance", `"guidance"`},
		{"quotes", `say "hi"`, `"say \"hi\""`},
		{"newlines", "one\ntwo", `"one\ntwo"`},
		{"backslashes", `C:\repo\file`, `"C:\\repo\\file"`},
		{"controls", "a\tb\rc", `"a\tb\rc"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tomlBasicString(tt.value); got != tt.want {
				t.Errorf("tomlBasicString(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestHostResumeArgs(t *testing.T) {
	tests := []struct {
		name      string
		host      Host
		sessionID string
		want      []string
	}{
		{"claude latest", &ClaudeHost{}, "", []string{"--continue"}},
		{"claude session", &ClaudeHost{}, "abc", []string{"--resume", "abc"}},
		{"codex latest", &CodexHost{}, "", []string{"resume", "--last"}},
		{"codex session", &CodexHost{}, "abc", []string{"resume", "abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.host.ResumeArgs(tt.sessionID); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ResumeArgs(%q) = %v, want %v", tt.sessionID, got, tt.want)
			}
		})
	}
}
