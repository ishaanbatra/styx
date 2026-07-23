package research

import (
	"errors"
	"os"
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

func TestParse_KeywordSections(t *testing.T) {
	liveReview, err := os.ReadFile("testdata/live_markdown_review.md")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		raw           string
		wantBlocking  int
		wantImportant int
		wantNits      int
		wantErr       error
	}{
		{
			name:          "live markdown heading review",
			raw:           string(liveReview),
			wantBlocking:  0,
			wantImportant: 0,
			wantNits:      5,
		},
		{
			name: "bold headings",
			raw: `**BLOCKING**
- b1

__IMPORTANT__:
- i1

### **NITS:**
- n1`,
			wantBlocking:  1,
			wantImportant: 1,
			wantNits:      1,
		},
		{
			name: "bare headings",
			raw: `BLOCKING:
- claim about pgvector dimensions is unverified
- missing context on similarity metric

IMPORTANT:
- doesn't address index rebuild cost

NIT:
- typo on line 3`,
			wantBlocking:  2,
			wantImportant: 1,
			wantNits:      1,
		},
		{
			name: "none sections",
			raw: `BLOCKING:
None

IMPORTANT:
None. (Both reviewers agree.)

NIT:
n/a`,
			wantBlocking:  0,
			wantImportant: 0,
			wantNits:      0,
		},
		{
			name:          "garbage with keyword in prose",
			raw:           "this is unstructured prose mentioning BLOCKING mid-sentence",
			wantBlocking:  0,
			wantImportant: 1,
			wantNits:      0,
			wantErr:       ErrDegraded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(tt.raw)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if got := len(c.Blocking); got != tt.wantBlocking {
				t.Errorf("blocking len = %d, want %d: %#v", got, tt.wantBlocking, c.Blocking)
			}
			if got := len(c.Important); got != tt.wantImportant {
				t.Errorf("important len = %d, want %d: %#v", got, tt.wantImportant, c.Important)
			}
			if got := len(c.Nits); got != tt.wantNits {
				t.Errorf("nits len = %d, want %d: %#v", got, tt.wantNits, c.Nits)
			}
		})
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
