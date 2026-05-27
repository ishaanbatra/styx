// Package brief writes research briefs and implementation plans into a
// project's configured directories and resolves the most recent brief.
package brief

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// WriteOpts configures WriteBrief / WritePlan.
type WriteOpts struct {
	ProjectPath string    // absolute project root
	SubDir      string    // relative to ProjectPath; e.g. "docs/research" or "styx/research"
	Query       string    // used for the slug and as a header
	Body        string    // markdown body
	Kind        string    // "brief" or "plan"
	Now         time.Time // defaults to time.Now() when zero
}

// WriteBrief writes a research brief markdown file and returns its absolute path.
func WriteBrief(o WriteOpts) (string, error) {
	o.Kind = "brief"
	return writeMarkdown(o)
}

// WritePlan writes a plan markdown file and returns its absolute path.
func WritePlan(o WriteOpts) (string, error) {
	o.Kind = "plan"
	return writeMarkdown(o)
}

func writeMarkdown(o WriteOpts) (string, error) {
	if o.ProjectPath == "" {
		return "", errors.New("WriteOpts.ProjectPath is required")
	}
	if o.SubDir == "" {
		return "", errors.New("WriteOpts.SubDir is required")
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	dir := filepath.Join(o.ProjectPath, o.SubDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	stamp := o.Now.UTC().Format("20060102-150405")
	slug := slugify(o.Query)
	name := fmt.Sprintf("%s-%s-%s.md", stamp, slug, o.Kind)
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(o.Body), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", full, err)
	}
	return full, nil
}

// LoadLatest returns the absolute path of the most recent *.md file in dir
// whose name matches the timestamp-prefixed brief/plan format, or "" if none.
func LoadLatest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", dir, err)
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		matches = append(matches, name)
	}
	if len(matches) == 0 {
		return "", nil
	}
	sort.Strings(matches)
	return filepath.Join(dir, matches[len(matches)-1]), nil
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	if s == "" {
		s = "untitled"
	}
	return s
}
