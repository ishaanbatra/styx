package update

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// SpawnBackgroundCheck starts a detached check-only Styx process when the
// cache is stale. The child owns the two-second request timeout in Check.
func SpawnBackgroundCheck() error {
	if os.Getenv("STYX_INTERNAL_UPDATE_CHECK") == "1" ||
		os.Getenv("STYX_NO_UPDATE_CHECK") != "" || os.Getenv("DO_NOT_TRACK") == "1" {
		return nil
	}
	launchConfig.RLock()
	version := launchConfig.version
	launchConfig.RUnlock()
	if version != "" && isDevelopmentVersion(version) {
		return nil
	}
	_, latestPath, _, err := cachePaths()
	if err != nil {
		return err
	}
	if cacheFresh(latestPath, time.Now()) {
		return nil
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve styx executable for update check: %w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open null device for update check: %w", err)
	}
	defer devNull.Close()

	cmd := exec.Command(executable, "update", "--check-only")
	cmd.Env = append(os.Environ(), "STYX_INTERNAL_UPDATE_CHECK=1")
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	configureDetached(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start background update check: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("detach background update check: %w", err)
	}
	return nil
}
