// Package paths resolves Styx's on-disk locations following the XDG Base
// Directory Specification with sensible macOS fallbacks.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "styx"

// ConfigDir returns ~/.config/styx (or $XDG_CONFIG_HOME/styx if set).
func ConfigDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", appName), nil
}

// StateDir returns the directory for app state (sqlite, indexes).
func StateDir() (string, error) {
	c, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "state"), nil
}

// CacheDir returns ~/.cache/styx (or $XDG_CACHE_HOME/styx if set).
func CacheDir() (string, error) {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".cache", appName), nil
}

// LogDir returns the directory for log files.
func LogDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, appName, "logs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".local", "share", appName, "logs"), nil
}

// RoutingPath returns the absolute path to routing.toml.
func RoutingPath() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "routing.toml"), nil
}

// ProjectsPath returns the absolute path to projects.toml.
func ProjectsPath() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "projects.toml"), nil
}

// ModelsCachePath is where the model-discovery cache (models.json) lives.
func ModelsCachePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "models.json"), nil
}

// UsageDBPath returns the absolute path to the sqlite usage log.
func UsageDBPath() (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "usage.db"), nil
}

// MemoryDir returns the directory holding per-project memory databases.
func MemoryDir() (string, error) {
	s, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(s, "memory"), nil
}

// AuditDir returns the directory holding per-project session audit logs.
func AuditDir() (string, error) {
	s, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(s, "audit"), nil
}

// ThreadsDir returns the directory holding per-project agent-thread state.
func ThreadsDir() (string, error) {
	s, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(s, "threads"), nil
}

// EnsureDir creates dir (and parents) with 0755.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
