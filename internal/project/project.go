// Package project discovers and tracks code projects via git roots.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
)

// Project is the public record for a registered repo.
type Project = config.Project

// ErrNotInGitRepo is returned when no .git ancestor is found.
var ErrNotInGitRepo = errors.New("not inside a git repository")

// ErrUnknown is returned when an alias is not registered.
var ErrUnknown = errors.New("project not registered")

// Current resolves the project for the current working directory.
func Current() (Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Project{}, fmt.Errorf("getwd: %w", err)
	}
	return CurrentFrom(cwd)
}

// CurrentFrom resolves the project containing dir; auto-registers if new.
func CurrentFrom(dir string) (Project, error) {
	root, err := findGitRoot(dir)
	if err != nil {
		return Project{}, err
	}
	regs, err := config.LoadProjects()
	if err != nil {
		return Project{}, fmt.Errorf("load registry: %w", err)
	}
	for _, p := range regs {
		if p.Path == root {
			return p, nil
		}
	}
	p := autoRegister(root, regs)
	regs = append(regs, p)
	if err := config.SaveProjects(regs); err != nil {
		return Project{}, fmt.Errorf("save registry: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[styx] registered new project: %s (%s) at %s\n", p.Name, p.Language, p.Path)
	return p, nil
}

// Resolve looks up a project by friendly alias.
func Resolve(alias string) (Project, error) {
	regs, err := config.LoadProjects()
	if err != nil {
		return Project{}, err
	}
	for _, p := range regs {
		if p.Name == alias {
			return p, nil
		}
	}
	return Project{}, fmt.Errorf("%w: %q", ErrUnknown, alias)
}

// List returns the full registry.
func List() ([]Project, error) {
	return config.LoadProjects()
}

// Register adds or replaces an entry by Name.
func Register(p Project) error {
	regs, err := config.LoadProjects()
	if err != nil {
		return err
	}
	for i, existing := range regs {
		if existing.Name == p.Name {
			regs[i] = p
			return config.SaveProjects(regs)
		}
	}
	regs = append(regs, p)
	return config.SaveProjects(regs)
}

// Forget removes the entry with name `alias`. No error if absent.
func Forget(alias string) error {
	regs, err := config.LoadProjects()
	if err != nil {
		return err
	}
	out := regs[:0]
	for _, p := range regs {
		if p.Name != alias {
			out = append(out, p)
		}
	}
	return config.SaveProjects(out)
}

func findGitRoot(start string) (string, error) {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w (searched up from %s)", ErrNotInGitRepo, start)
		}
		dir = parent
	}
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	s := strings.ToLower(name)
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	if s == "" {
		s = "project"
	}
	return s
}

func autoRegister(root string, existing []Project) Project {
	base := slug(filepath.Base(root))
	name := base
	suffix := 2
	for nameTaken(name, existing) {
		name = fmt.Sprintf("%s-%d", base, suffix)
		suffix++
	}
	return Project{
		ID:          config.ProjectID(root),
		Name:        name,
		Path:        root,
		Language:    SniffLanguage(root),
		ResearchDir: "styx/research",
		PlansDir:    "styx/plans",
	}
}

func nameTaken(name string, regs []Project) bool {
	for _, p := range regs {
		if p.Name == name {
			return true
		}
	}
	return false
}
