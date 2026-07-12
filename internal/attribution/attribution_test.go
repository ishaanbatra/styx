package attribution

import (
	"strings"
	"testing"
)

func TestIdentityConstants(t *testing.T) {
	// Values duplicated on purpose: changing the identity must touch the
	// spec (docs/superpowers/specs/2026-07-11-styx-attribution-design.md),
	// the constants, and this test together.
	wantTrailer := "Co-Authored-By: styx-thetrickster[bot] <302670164+styx-thetrickster[bot]@users.noreply.github.com>"
	if Trailer != wantTrailer {
		t.Errorf("Trailer = %q, want %q", Trailer, wantTrailer)
	}
	wantFooter := "Generated with [styx](https://github.com/ishaanbatra/styx)"
	if PRFooter != wantFooter {
		t.Errorf("PRFooter = %q, want %q", PRFooter, wantFooter)
	}
	if !strings.Contains(CommitInstruction, Trailer) {
		t.Error("CommitInstruction must embed Trailer verbatim")
	}
}
