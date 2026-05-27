package research

import (
	"testing"
)

func TestParse_StrictJSON(t *testing.T) {
	raw := `{"blocking":["b1","b2"],"important":["i1"],"nits":["n1","n2","n3"]}`
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Blocking) != 2 || c.Blocking[0] != "b1" {
		t.Errorf("Blocking parse failed: %#v", c.Blocking)
	}
	if len(c.Important) != 1 {
		t.Errorf("Important parse failed: %#v", c.Important)
	}
	if len(c.Nits) != 3 {
		t.Errorf("Nits parse failed: %#v", c.Nits)
	}
}

func TestParse_EmbeddedJSON(t *testing.T) {
	raw := `Here's my critique:

{"blocking":["b1"],"important":[],"nits":["n1"]}

Hope this helps.`
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Blocking) != 1 || c.Blocking[0] != "b1" {
		t.Errorf("embedded blocking: %#v", c.Blocking)
	}
}

func TestParse_KeywordFallback(t *testing.T) {
	raw := `BLOCKING:
- claim about pgvector dimensions is unverified
- missing context on similarity metric

IMPORTANT:
- doesn't address index rebuild cost

NIT:
- typo on line 3`
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Blocking) != 2 {
		t.Errorf("blocking len = %d, want 2: %#v", len(c.Blocking), c.Blocking)
	}
	if len(c.Important) != 1 {
		t.Errorf("important len = %d, want 1: %#v", len(c.Important), c.Important)
	}
	if len(c.Nits) != 1 {
		t.Errorf("nits len = %d, want 1: %#v", len(c.Nits), c.Nits)
	}
}

func TestParse_GarbageInputTreatsAsSingleImportant(t *testing.T) {
	raw := "this is some unstructured prose with no recognizable sections"
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Important) != 1 {
		t.Errorf("expected garbage -> single IMPORTANT, got: %#v", c)
	}
}

func TestParse_EmptyConverged(t *testing.T) {
	raw := `{"blocking":[],"important":[],"nits":[]}`
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if c.Converged() != true {
		t.Errorf("empty blocking+important should converge, got false")
	}
}

func TestConverged_WithOnlyNits(t *testing.T) {
	c := Critique{Nits: []string{"typo"}}
	if !c.Converged() {
		t.Error("nits-only should converge")
	}
}

func TestConverged_WithImportant(t *testing.T) {
	c := Critique{Important: []string{"missing context"}}
	if c.Converged() {
		t.Error("important findings should NOT converge")
	}
}
