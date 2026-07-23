//go:build !darwin

package memguard

// current always reports Normal on non-darwin platforms: the jetsam failure
// mode this package guards against is darwin-specific, and there is no
// equivalent pressure probe wired up for Linux/Windows yet.
func current() Level {
	return Normal
}
