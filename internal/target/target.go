// Package target resolves the active project for any styx invocation from a
// single seam: a {--project alias, --dir path, cwd} spec. It never silently
// falls back to the cwd when an explicit target was given and failed.
package target

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/project"
)

// Spec describes how to resolve the active project. Precedence: Alias -> Dir -> Cwd.
type Spec struct {
	Alias string
	Dir   string
	Cwd   string
}

// Resolve returns the project for the spec. Alias resolution is exact Name
// match, then unique prefix match, then an existing on-disk path (a directory
// via its git root, or a file/subpath under a registered project's tree). A
// non-existent alias errors — it never falls back to the cwd project.
func Resolve(spec Spec) (project.Project, error) {
	switch {
	case spec.Alias != "":
		return resolveAlias(spec.Alias)
	case spec.Dir != "":
		abs, err := filepath.Abs(spec.Dir)
		if err != nil {
			return project.Project{}, fmt.Errorf("resolve --dir %q: %w", spec.Dir, err)
		}
		return project.CurrentFrom(abs)
	case spec.Cwd != "":
		return project.CurrentFrom(spec.Cwd)
	default:
		return project.Project{}, fmt.Errorf("no target: name a project (--project), pass --dir, or cd into a repo")
	}
}

func resolveAlias(alias string) (project.Project, error) {
	regs, err := config.LoadProjects()
	if err != nil {
		return project.Project{}, fmt.Errorf("load registry: %w", err)
	}
	for _, p := range regs {
		if p.Name == alias {
			return p, nil
		}
	}
	var prefix []config.Project
	for _, p := range regs {
		if strings.HasPrefix(p.Name, alias) {
			prefix = append(prefix, p)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], nil
	}
	if len(prefix) > 1 {
		return project.Project{}, fmt.Errorf("ambiguous project %q: matches %s", alias, names(prefix))
	}

	if abs, absErr := filepath.Abs(alias); absErr == nil {
		// Only treat the alias as a path if it actually exists on disk. An
		// existing directory resolves via its git root; an existing file (or
		// other non-dir) resolves to the registered project whose tree contains
		// it. A non-existent alias must NOT resolve via isUnder — otherwise a
		// typo'd name, when cwd is inside a registered project, would silently
		// resolve to that project instead of erroring.
		if fi, statErr := os.Stat(abs); statErr == nil {
			if fi.IsDir() {
				return project.CurrentFrom(abs)
			}
			for _, p := range regs {
				if isUnder(abs, p.Path) {
					return p, nil
				}
			}
		}
	}
	return project.Project{}, fmt.Errorf("unknown project %q (registered: %s)", alias, names(regs))
}

func isUnder(path, base string) bool {
	if base == "" {
		return false
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func names(projs []config.Project) string {
	if len(projs) == 0 {
		return "(none)"
	}
	ns := make([]string, len(projs))
	for i, p := range projs {
		ns[i] = p.Name
	}
	return strings.Join(ns, ", ")
}
