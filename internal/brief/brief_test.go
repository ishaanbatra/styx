package brief

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteBrief_WritesMarkdownToResearchDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := WriteBrief(WriteOpts{
		ProjectPath: root,
		SubDir:      "docs/research",
		Query:       "pgvector dim limits",
		Body:        "## Findings\nGemini blah.",
		Now:         time.Date(2026, 5, 27, 10, 30, 15, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "20260527-103015-pgvector-dim-limits-brief.md") {
		t.Errorf("path = %q, want suffix 20260527-103015-pgvector-dim-limits-brief.md", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "## Findings") {
		t.Errorf("brief body missing; got:\n%s", string(b))
	}
}

func TestLoadLatest_ReturnsNewestBrief(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"20260101-100000-old-brief.md",
		"20260527-091500-newer-brief.md",
		"20260527-103015-newest-brief.md",
		"unrelated.txt",
	}
	for _, f := range files {
		path := filepath.Join(dir, f)
		if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadLatest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "20260527-103015-newest-brief.md") {
		t.Errorf("got %q, want newest brief", got)
	}
}

func TestLoadLatest_NoBriefsReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadLatest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}
