package main

import (
	"reflect"
	"runtime/debug"
	"testing"
)

func TestResolveStyxDisplayVersion(t *testing.T) {
	tests := []struct {
		name    string
		version string
		info    debug.BuildInfo
		want    string
	}{
		{
			name:    "release stamp wins",
			version: "0.5.0",
			info: debug.BuildInfo{
				Main: debug.Module{Version: "v0.4.0"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "1234567890abcdef"},
				},
			},
			want: "0.5.0",
		},
		{
			name:    "module version wins over vcs",
			version: "0.4.0-dev",
			info: debug.BuildInfo{
				Main: debug.Module{Version: "v0.4.1"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "1234567890abcdef"},
				},
			},
			want: "v0.4.1",
		},
		{
			name:    "clean vcs revision",
			version: "0.4.0-dev",
			info: debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "1234567890abcdef"},
					{Key: "vcs.modified", Value: "false"},
				},
			},
			want: "dev-1234567",
		},
		{
			name:    "dirty vcs revision",
			version: "0.4.0-dev",
			info: debug.BuildInfo{
				Settings: []debug.BuildSetting{
					{Key: "vcs.modified", Value: "true"},
					{Key: "vcs.revision", Value: "abcdef1234567890"},
				},
			},
			want: "dev-abcdef1-dirty",
		},
		{
			name:    "short vcs revision",
			version: "0.4.0-dev",
			info: debug.BuildInfo{
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "abc123"},
				},
			},
			want: "dev-abc123",
		},
		{
			name:    "development fallback",
			version: "0.4.0-dev",
			info: debug.BuildInfo{
				Main: debug.Module{Version: "(devel)"},
			},
			want: "0.4.0-dev",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveStyxDisplayVersion(tt.version, tt.info); got != tt.want {
				t.Errorf("resolveStyxDisplayVersion() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetupFreeVerb(t *testing.T) {
	tests := []struct {
		verb string
		want bool
	}{
		{verb: "help", want: true},
		{verb: "-h", want: true},
		{verb: "--help", want: true},
		{verb: "version", want: true},
		{verb: "--version", want: true},
		{verb: "-V", want: true},
		{verb: "upgrade", want: false},
		{verb: "dead-code", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.verb, func(t *testing.T) {
			if got := setupFreeVerb(tt.verb); got != tt.want {
				t.Errorf("setupFreeVerb(%q) = %t, want %t", tt.verb, got, tt.want)
			}
		})
	}
}

func TestParseGlobalFlags(t *testing.T) {
	cases := []struct {
		name        string
		argv        []string
		wantRest    []string
		wantQuiet   bool
		wantProject string
		wantDir     string
		wantHost    string
	}{
		{"plain verb", []string{"plan", "do x"}, []string{"plan", "do x"}, false, "", "", ""},
		{"project flag", []string{"--project", "backend", "review"}, []string{"review"}, false, "backend", "", ""},
		{"dir flag", []string{"--dir", "/repos/api", "plan", "x"}, []string{"plan", "x"}, false, "", "/repos/api", ""},
		{"project equals form", []string{"--project=backend", "review"}, []string{"review"}, false, "backend", "", ""},
		{"quiet still works", []string{"--quiet", "--project", "ui", "auto", "g"}, []string{"auto", "g"}, true, "ui", "", ""},
		{"host flag", []string{"--host", "codex", "launch"}, []string{"launch"}, false, "", "", "codex"},
		{"host equals form", []string{"--host=claude", "resume"}, []string{"resume"}, false, "", "", "claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest, quiet, _, project, dir, host := parseGlobalFlags(tc.argv)
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
			if quiet != tc.wantQuiet {
				t.Errorf("quiet = %v, want %v", quiet, tc.wantQuiet)
			}
			if project != tc.wantProject {
				t.Errorf("project = %q, want %q", project, tc.wantProject)
			}
			if dir != tc.wantDir {
				t.Errorf("dir = %q, want %q", dir, tc.wantDir)
			}
			if host != tc.wantHost {
				t.Errorf("host = %q, want %q", host, tc.wantHost)
			}
		})
	}
}
