//go:build !unix

package channel

import "os/exec"

// KilledBySignal always reports false where POSIX termination signals don't
// exist (Windows); ClassifyExecError classifies timeouts there via the caller's
// dead context instead.
func KilledBySignal(_ *exec.ExitError) bool { return false }
