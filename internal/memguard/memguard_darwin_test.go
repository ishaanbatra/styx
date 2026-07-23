//go:build darwin

package memguard

import "testing"

func TestLevelFromRaw(t *testing.T) {
	cases := []struct {
		raw  uint32
		want Level
	}{
		{1, Normal},
		{2, Warn},
		{4, Critical},
		{0, Normal},
		{3, Normal},
		{99, Normal},
	}
	for _, tc := range cases {
		if got := levelFromRaw(tc.raw); got != tc.want {
			t.Errorf("levelFromRaw(%d) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
