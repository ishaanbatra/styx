//go:build darwin

package memguard

import "golang.org/x/sys/unix"

// pressureSysctl is the kernel's jetsam pressure signal: 1 (normal),
// 2 (warn), 4 (critical). Reading it is pure Go via x/sys/unix — no cgo, no
// shelling out to an OS tool.
const pressureSysctl = "kern.memorystatus_vm_pressure_level"

func current() Level {
	raw, err := unix.SysctlUint32(pressureSysctl)
	if err != nil {
		// Fail open: a broken or unavailable probe must never block dispatch.
		return Normal
	}
	return levelFromRaw(raw)
}

// levelFromRaw maps the raw kern.memorystatus_vm_pressure_level value to a
// Level. Factored out from current() so the mapping is testable without a
// live sysctl call. Any value outside the known set fails open to Normal.
func levelFromRaw(raw uint32) Level {
	switch raw {
	case 1:
		return Normal
	case 2:
		return Warn
	case 4:
		return Critical
	default:
		return Normal
	}
}
