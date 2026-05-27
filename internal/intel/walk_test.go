package intel

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWalk_RespectsGitignore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "secrets.env\ndist/\n")
	writeFile(t, root, "app/main.py", "")
	writeFile(t, root, "secrets.env", "k=v")
	writeFile(t, root, "dist/bundle.js", "")
	writeFile(t, root, "README.md", "")

	got, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	want := []string{"README.md", "app/main.py"}
	if len(got) != len(want) {
		t.Fatalf("got %d files, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] got %q, want %q", i, got[i], w)
		}
	}
}

func TestWalk_ExcludesBuiltinDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "node_modules/foo/index.js", "")
	writeFile(t, root, ".git/HEAD", "ref")
	writeFile(t, root, "__pycache__/x.pyc", "")
	writeFile(t, root, ".venv/lib/python.py", "")
	writeFile(t, root, "target/debug/x", "")
	writeFile(t, root, "src/main.go", "")

	got, err := Walk(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "src/main.go" {
		t.Errorf("expected only [src/main.go], got %v", got)
	}
}

func TestWalk_CapsAtMaxFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 100; i++ {
		writeFile(t, root, filepath.Join("src", "file"+itoa(i)+".go"), "")
	}
	got, err := WalkCapped(root, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Errorf("expected 50 files (capped), got %d", len(got))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
