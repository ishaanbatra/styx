package memguard

import "testing"

func TestCurrentReturnsValidLevel(t *testing.T) {
	switch l := Current(); l {
	case Normal, Warn, Critical:
	default:
		t.Fatalf("Current() = %v, want one of Normal/Warn/Critical", l)
	}
}

func TestLevelString(t *testing.T) {
	cases := map[Level]string{
		Normal:    "normal",
		Warn:      "warn",
		Critical:  "critical",
		Level(99): "normal",
	}
	for level, want := range cases {
		if got := level.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", level, got, want)
		}
	}
}
