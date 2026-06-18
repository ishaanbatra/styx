package target

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
}

func seedRegistry(t *testing.T, projs ...config.Project) {
	t.Helper()
	if err := config.SaveProjects(projs); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())

	backend := filepath.Join(t.TempDir(), "ai-ta-backend")
	teacher := filepath.Join(t.TempDir(), "ai-ta-teacher-ui")
	gitInit(t, backend)
	gitInit(t, teacher)
	seedRegistry(t,
		config.Project{Name: "ai-ta-backend", Path: backend, Language: "python"},
		config.Project{Name: "ai-ta-teacher-ui", Path: teacher, Language: "typescript"},
	)

	cases := []struct {
		name     string
		spec     Spec
		wantName string
		wantErr  string
	}{
		{"exact alias", Spec{Alias: "ai-ta-backend"}, "ai-ta-backend", ""},
		{"unique prefix", Spec{Alias: "ai-ta-teacher"}, "ai-ta-teacher-ui", ""},
		{"ambiguous prefix", Spec{Alias: "ai-ta-"}, "", "ambiguous"},
		{"dir resolves to registered repo", Spec{Dir: teacher}, "ai-ta-teacher-ui", ""},
		{"cwd walk-up", Spec{Cwd: backend}, "ai-ta-backend", ""},
		{"unknown alias errors, no cwd fallback", Spec{Alias: "nope", Cwd: backend}, "", "unknown project"},
		{"empty spec errors", Spec{}, "", "no target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.Name != tc.wantName {
				t.Errorf("got %q, want %q", got.Name, tc.wantName)
			}
		})
	}
}
