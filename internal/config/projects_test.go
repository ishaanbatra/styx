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
			Name:         "hoot-backend",
			Path:         "/Users/x/Documents/GitHub/ai-ta-backend",
			Language:     "python",
			ResearchDir:  "docs/research",
			PlansDir:     "docs/plans",
			DefaultVerbs: []string{"plan", "build", "review"},
		},
		{
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
