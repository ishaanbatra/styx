// Package memguard reports host memory pressure so dispatch can avoid
// launching subprocesses the OS is about to jetsam-kill. It exposes a single
// read: Current(). Callers that need to act on it (channel decorators, the
// background task queue) take a func() Level rather than importing this
// package directly, so tests can inject fakes without touching real sysctls.
package memguard

// Level is the host's current memory-pressure state.
type Level int

const (
	// Normal means no known memory pressure; dispatch as usual.
	Normal Level = iota
	// Warn means the host is under moderate pressure; dispatch may proceed
	// but callers should narrate the risk.
	Warn
	// Critical means the host is close to jetsam territory; callers should
	// refuse to launch new subprocesses rather than risk an external kill.
	Critical
)

// String renders the level for logging and narration.
func (l Level) String() string {
	switch l {
	case Warn:
		return "warn"
	case Critical:
		return "critical"
	default:
		return "normal"
	}
}

// Current returns the host's current memory-pressure level. Platforms
// without a pressure probe (or a probe that errors) always report Normal:
// this package fails open, since a broken probe must never block dispatch.
func Current() Level {
	return current()
}
