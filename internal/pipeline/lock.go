package pipeline

import (
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrLocked is returned when a lock is already held by another run.
var ErrLocked = stderrors.New("another pipeline is in progress")

func lockPath(projectPath string) string {
	return filepath.Join(projectPath, ".styx", "runs", ".lock")
}

// AcquireLock writes runID to the lock file using O_EXCL semantics so only one
// process can hold the lock at a time. Returns ErrLocked if already held.
func AcquireLock(projectPath, runID string) error {
	p := lockPath(projectPath)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if stderrors.Is(err, os.ErrExist) {
			holder, _ := ReadLockHolder(projectPath)
			return fmt.Errorf("%w (held by %s)", ErrLocked, holder)
		}
		return err
	}
	defer f.Close()
	_, err = f.WriteString(runID + "\n")
	return err
}

// ReleaseLock removes the lock file. No error if absent.
func ReleaseLock(projectPath string) error {
	err := os.Remove(lockPath(projectPath))
	if err != nil && !stderrors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ReadLockHolder returns the run-id stored in the lock file, or "" if no lock.
func ReadLockHolder(projectPath string) (string, error) {
	b, err := os.ReadFile(lockPath(projectPath))
	if err != nil {
		if stderrors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
