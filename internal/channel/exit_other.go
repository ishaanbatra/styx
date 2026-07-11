//go:build !unix

package channel

import "os/exec"

// KilledBySignal always reports false where POSIX termination signals don't
// exist (Windows); adapters classify timeouts there via the caller's dead
// context instead — see each adapter's classifyExecError.
func KilledBySignal(_ *exec.ExitError) bool { return false }
