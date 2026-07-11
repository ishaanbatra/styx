//go:build unix

package channel

import (
	"os/exec"
	"syscall"
)

// KilledBySignal reports whether the process behind ee was terminated by
// SIGKILL or SIGTERM — the signature of a timeout kill on unix platforms.
func KilledBySignal(ee *exec.ExitError) bool {
	status, ok := ee.Sys().(syscall.WaitStatus)
	return ok && (status.Signal() == syscall.SIGKILL || status.Signal() == syscall.SIGTERM)
}
