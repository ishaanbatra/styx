package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/paths"
)

// MigrateProjectState renames legacy Name-keyed per-project state to the stable
// ID key. It is idempotent and safe to re-run: each rename only happens when the
// old path exists and the new one does not. Covers the memory db, audit dir,
// intel dir, and agent threads file. global.db is shared and never touched.
func MigrateProjectState(projs []Project) error {
	memDir, err := paths.MemoryDir()
	if err != nil {
		return fmt.Errorf("memory dir: %w", err)
	}
	auditDir, err := paths.AuditDir()
	if err != nil {
		return fmt.Errorf("audit dir: %w", err)
	}
	threadsDir, err := paths.ThreadsDir()
	if err != nil {
		return fmt.Errorf("threads dir: %w", err)
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		return fmt.Errorf("state dir: %w", err)
	}
	intelDir := filepath.Join(stateDir, "intel")

	for _, p := range projs {
		if p.ID == "" || p.Name == "" || p.ID == p.Name {
			continue
		}
		moves := [][2]string{
			{filepath.Join(memDir, p.Name+".db"), filepath.Join(memDir, p.ID+".db")},
			{filepath.Join(threadsDir, p.Name+".json"), filepath.Join(threadsDir, p.ID+".json")},
			{filepath.Join(auditDir, p.Name), filepath.Join(auditDir, p.ID)},
			{filepath.Join(intelDir, p.Name), filepath.Join(intelDir, p.ID)},
		}
		for _, m := range moves {
			if err := renameIfNeeded(m[0], m[1]); err != nil {
				return fmt.Errorf("migrate %s -> %s: %w", m[0], m[1], err)
			}
		}
	}
	return nil
}

// renameIfNeeded moves old to new only when old exists and new does not. If
// both exist, the legacy Name-keyed path is left in place rather than deleted
// so migration stays non-destructive.
func renameIfNeeded(old, new string) error {
	if _, err := os.Stat(old); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if _, err := os.Stat(new); err == nil {
		fmt.Fprintf(os.Stderr, "[styx] migration: both %s and %s exist; leaving legacy copy (delete manually if unneeded)\n", old, new)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(new), 0o755); err != nil {
		return err
	}
	return os.Rename(old, new)
}
