package main

import (
	"reflect"
	"testing"
)

func TestParseGlobalFlags(t *testing.T) {
	cases := []struct {
		name        string
		argv        []string
		wantRest    []string
		wantQuiet   bool
		wantProject string
		wantDir     string
	}{
		{"plain verb", []string{"plan", "do x"}, []string{"plan", "do x"}, false, "", ""},
		{"project flag", []string{"--project", "backend", "review"}, []string{"review"}, false, "backend", ""},
		{"dir flag", []string{"--dir", "/repos/api", "plan", "x"}, []string{"plan", "x"}, false, "", "/repos/api"},
		{"project equals form", []string{"--project=backend", "review"}, []string{"review"}, false, "backend", ""},
		{"quiet still works", []string{"--quiet", "--project", "ui", "auto", "g"}, []string{"auto", "g"}, true, "ui", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest, quiet, _, project, dir := parseGlobalFlags(tc.argv)
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
		})
	}
}
