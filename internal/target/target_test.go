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

// chdir switches the process cwd to dir and restores the original on cleanup.
// Equivalent to t.Chdir but works under the module's go1.22 directive.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})
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

// TestResolveAliasExistenceGate guards the existence-gated isUnder fallback:
// when the process cwd is inside a registered project (the common `styx mcp`
// setup), a relative alias that matches no registered project name and does not
// exist on disk must produce the loud "unknown project" error — NOT silently
// resolve to the cwd project via filepath.Abs + isUnder. Existing paths (dir or
// file) under a registered project's tree must still resolve to that project.
func TestResolveAliasExistenceGate(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())

	proj := filepath.Join(t.TempDir(), "demo-proj")
	gitInit(t, proj)
	// Canonicalize so the registered path matches the physical cwd that
	// os.Getwd (hence filepath.Abs of a relative alias) reports after chdir —
	// on macOS t.TempDir lives under a /var -> /private/var symlink.
	proj, err := filepath.EvalSymlinks(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedRegistry(t, config.Project{Name: "demo-proj", Path: proj, Language: "go"})

	// Run with the process cwd inside the registered project — this is what
	// makes filepath.Abs(alias) land under the project's tree. (Manual chdir +
	// restore rather than t.Chdir, which requires go1.24; module is go1.22.)
	chdir(t, proj)

	cases := []struct {
		name     string
		alias    string
		wantName string
		wantErr  string
	}{
		{"unknown relative alias errors, no cwd fallback", "nope-not-real", "", "unknown project"},
		{"existing subdir resolves to project", "sub", "demo-proj", ""},
		{"existing file resolves to project", "file.txt", "demo-proj", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(Spec{Alias: tc.alias})
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
