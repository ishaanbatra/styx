// Package intel builds and serves the per-project codebase intelligence index
// that styx uses to saturate Claude's context on plan/build.
package intel

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Built-in excludes regardless of .gitignore content.
var builtinExcludes = map[string]bool{
	".git":          true,
	"node_modules":  true,
	"__pycache__":   true,
	".venv":         true,
	"venv":          true,
	".env":          true,
	"target":        true, // rust
	"dist":          true,
	"build":         true,
	".next":         true,
	".cache":        true,
	".pytest_cache": true,
	".mypy_cache":   true,
	".idea":         true,
	".vscode":       true,
	"bin":           true,
}

// DefaultMaxFiles is the hard cap on tracked files per repo.
const DefaultMaxFiles = 50_000

// Walk returns relative file paths under root, respecting .gitignore + builtinExcludes.
func Walk(root string) ([]string, error) {
	return WalkCapped(root, DefaultMaxFiles)
}

// WalkCapped is Walk with an explicit max-files cap. Returns sorted relative paths.
func WalkCapped(root string, maxFiles int) ([]string, error) {
	ignored, err := loadGitignore(root)
	if err != nil {
		return nil, fmt.Errorf("load gitignore: %w", err)
	}
	var files []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		if d.IsDir() {
			if builtinExcludes[base] {
				return filepath.SkipDir
			}
			if ignored.match(rel + "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if ignored.match(rel) {
			return nil
		}
		// Skip the .gitignore file itself; it's project metadata, not content.
		if rel == ".gitignore" {
			return nil
		}
		if len(files) >= maxFiles {
			return filepath.SkipAll
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// gitignoreMatcher matches against a flat slice of literal-or-glob patterns.
// Sufficient for our needs (we don't implement full gitignore semantics, just
// the most common rules: literal names, trailing-slash dirs, prefix patterns).
type gitignoreMatcher struct{ patterns []string }

func (g *gitignoreMatcher) match(rel string) bool {
	if g == nil {
		return false
	}
	for _, p := range g.patterns {
		// Trailing slash means "directory pattern": match if rel starts with p (without slash).
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) || strings.HasPrefix(rel+"/", p) {
				return true
			}
		}
		// Glob match against full rel.
		if ok, _ := filepath.Match(p, rel); ok {
			return true
		}
		// Glob match against basename.
		if ok, _ := filepath.Match(p, filepath.Base(rel)); ok {
			return true
		}
	}
	return false
}

func loadGitignore(root string) (*gitignoreMatcher, error) {
	f, err := os.Open(filepath.Join(root, ".gitignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return &gitignoreMatcher{}, nil
		}
		return nil, err
	}
	defer f.Close()
	var pats []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Ignore negations (we don't support them).
		if strings.HasPrefix(line, "!") {
			continue
		}
		pats = append(pats, line)
	}
	return &gitignoreMatcher{patterns: pats}, s.Err()
}
