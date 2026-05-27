package project

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSniffLanguage(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"python-pyproject", []string{"pyproject.toml"}, "python"},
		{"python-setup-py", []string{"setup.py"}, "python"},
		{"typescript-package", []string{"package.json", "tsconfig.json"}, "typescript"},
		{"javascript-only-package", []string{"package.json"}, "javascript"},
		{"go-mod", []string{"go.mod"}, "go"},
		{"rust-cargo", []string{"Cargo.toml"}, "rust"},
		{"mixed-py-ts", []string{"pyproject.toml", "package.json"}, "mixed"},
		{"unknown-empty", []string{}, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, c.files...)
			got := SniffLanguage(dir)
			if got != c.want {
				t.Errorf("SniffLanguage(%v) = %q, want %q", c.files, got, c.want)
			}
		})
	}
}
