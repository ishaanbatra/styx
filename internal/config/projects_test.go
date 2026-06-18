package config

import (
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSaveAndLoadProjects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := []Project{
		{
			ID:           ProjectID("/Users/x/Documents/GitHub/ai-ta-backend"),
			Name:         "hoot-backend",
			Path:         "/Users/x/Documents/GitHub/ai-ta-backend",
			Language:     "python",
			ResearchDir:  "docs/research",
			PlansDir:     "docs/plans",
			DefaultVerbs: []string{"plan", "build", "review"},
		},
		{
			ID:       ProjectID("/Users/x/Documents/GitHub/VoiceResumeBot"),
			Name:     "voiceresumebot",
			Path:     "/Users/x/Documents/GitHub/VoiceResumeBot",
			Language: "python",
		},
	}
	if err := SaveProjects(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProjects()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}

	// Verify the file actually exists at the expected path.
	if _, err := filepath.Glob(filepath.Join(dir, "styx", "projects.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProjects_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := LoadProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d projects", len(got))
	}
}

func TestProjectIDStableAndDistinct(t *testing.T) {
	a := ProjectID("/Users/x/Documents/GitHub/ai-ta-backend")
	again := ProjectID("/Users/x/Documents/GitHub/ai-ta-backend")
	b := ProjectID("/Users/x/Documents/GitHub/ai-ta-teacher-ui")
	if a != again {
		t.Errorf("ID not stable: %q vs %q", a, again)
	}
	if a == b {
		t.Errorf("distinct paths share an ID: %q", a)
	}
	if len(a) != 12 {
		t.Errorf("ID length = %d, want 12", len(a))
	}
}

func TestLoadProjectsBackfillsID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// Write a legacy entry with no ID.
	if err := SaveProjects([]Project{{Name: "legacy", Path: "/repos/legacy", Language: "go"}}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != ProjectID("/repos/legacy") {
		t.Errorf("ID not backfilled: %+v", got)
	}
}
