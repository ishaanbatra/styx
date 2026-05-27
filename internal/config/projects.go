package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Project is one registered code project.
type Project struct {
	Name         string   `toml:"name"`
	Path         string   `toml:"path"`
	Language     string   `toml:"language"`
	ResearchDir  string   `toml:"research_dir,omitempty"`
	PlansDir     string   `toml:"plans_dir,omitempty"`
	DefaultVerbs []string `toml:"default_verbs,omitempty"`
}

type projectsFile struct {
	Project []Project `toml:"project"`
}

// LoadProjects loads the projects.toml registry. Missing file is not an error
// (returns empty slice) so first-run auto-registration can proceed.
func LoadProjects() ([]Project, error) {
	p, err := paths.ProjectsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Project{}, nil
		}
		return nil, fmt.Errorf("read projects.toml: %w", err)
	}
	var pf projectsFile
	if err := toml.Unmarshal(b, &pf); err != nil {
		return nil, fmt.Errorf("parse projects.toml: %w", err)
	}
	return pf.Project, nil
}

// SaveProjects writes projects.toml atomically (tmpfile + rename).
func SaveProjects(projs []Project) error {
	target, err := paths.ProjectsPath()
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(filepath.Dir(target)); err != nil {
		return fmt.Errorf("ensure config dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "projects-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(projectsFile{Project: projs}); err != nil {
		tmp.Close()
		return fmt.Errorf("encode projects.toml: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("rename tmp to target: %w", err)
	}
	return nil
}
