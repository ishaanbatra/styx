package crossrepo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type testChannel struct {
	response string
	err      error
	requests []string
}

func (c *testChannel) Send(_ context.Context, prompt string) (string, error) {
	c.requests = append(c.requests, prompt)
	return c.response, c.err
}

func testFinding(root1, root2 string, n int) string {
	return `{"producer":{"root":"` + root1 + `","path":"api.go","line":1,"symbol":"API"},"consumer":{"root":"` + root2 + `","path":"client.go","line":` + string(rune('0'+n)) + `,"symbol":"Client"},"relationship":"calls","contract":"API.Do","reason":"Client invokes API.Do"}`
}

func TestParseFindings(t *testing.T) {
	roots := []string{"/repo/producer", "/repo/consumer"}
	valid := testFinding(roots[0], roots[1], 2)
	tests := []struct {
		name         string
		raw          string
		want         int
		wantWarnings int
	}{
		{name: "strict", raw: `{"findings":[` + valid + `]}`, want: 1},
		{name: "fenced", raw: "```json\n" + `{"findings":[` + valid + `]}` + "\n```", want: 1},
		{name: "embedded after unrelated braces", raw: `analysis {nope} answer {"findings":[` + valid + `]} trailing`, want: 1},
		{name: "unknown root", raw: strings.Replace(`{"findings":[`+valid+`]}`, roots[1], "/repo/unmounted", 1), wantWarnings: 1},
		{name: "same root", raw: strings.Replace(`{"findings":[`+valid+`]}`, roots[1], roots[0], 1), wantWarnings: 1},
		{name: "escaping path", raw: strings.Replace(`{"findings":[`+valid+`]}`, "client.go", "../secret", 1), wantWarnings: 1},
		{name: "garbage", raw: "not json", wantWarnings: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings := ParseFindings(tt.raw, roots)
			if len(got) != tt.want || len(warnings) != tt.wantWarnings {
				t.Fatalf("findings/warnings = %+v/%v, want %d/%d", got, warnings, tt.want, tt.wantWarnings)
			}
		})
	}
}

func TestRunReviewsBoundedSampleOnce(t *testing.T) {
	roots := []string{"/repo/producer", "/repo/consumer"}
	items := make([]string, 0, 7)
	for i := 1; i <= 7; i++ {
		items = append(items, testFinding(roots[0], roots[1], i))
	}
	sweep := &testChannel{response: `{"findings":[` + strings.Join(items, ",") + `]}`}
	codex := &testChannel{response: "VERIFIED: client calls API"}
	guardCalls := 0
	rep, err := Run(context.Background(), Options{
		Roots: roots, Question: "Who consumes API.Do?", Sweeper: sweep, Codex: codex,
		AfterSweep: func() ([]TreeChange, error) { guardCalls++; return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.requests) != 1 || len(codex.requests) != 1 || guardCalls != 1 {
		t.Fatalf("sweep/codex/guard calls = %d/%d/%d", len(sweep.requests), len(codex.requests), guardCalls)
	}
	if len(rep.Findings) != 7 || rep.ReviewedCount != maxReviewSample {
		t.Fatalf("report = %+v", rep)
	}
	if strings.Count(codex.requests[0], `"consumer"`) != maxReviewSample || !strings.Contains(codex.requests[0], "actually relies") {
		t.Errorf("review prompt is not bounded/specific:\n%s", codex.requests[0])
	}
	if !strings.Contains(sweep.requests[0], roots[0]) || !strings.Contains(sweep.requests[0], roots[1]) || !strings.Contains(sweep.requests[0], "Who consumes API.Do?") {
		t.Errorf("sweep prompt missing roots/question:\n%s", sweep.requests[0])
	}
	rendered := RenderReport(rep)
	const findingsHeader = "## Machine-readable findings\n\n```json\n"
	start := strings.Index(rendered, findingsHeader)
	if start < 0 {
		t.Fatalf("missing structured findings:\n%s", rendered)
	}
	jsonStart := start + len(findingsHeader)
	jsonEnd := strings.Index(rendered[jsonStart:], "\n```")
	if jsonEnd < 0 || !json.Valid([]byte(rendered[jsonStart:jsonStart+jsonEnd])) {
		t.Fatalf("rendered findings are not JSON:\n%s", rendered[jsonStart:])
	}
}

func TestRunTreeGuardFailsClosedBeforeReview(t *testing.T) {
	roots := []string{"/repo/one", "/repo/two"}
	sweep := &testChannel{response: `{"findings":[` + testFinding(roots[0], roots[1], 2) + `]}`}
	codex := &testChannel{response: "must not run"}
	tests := []struct {
		name      string
		guard     func() ([]TreeChange, error)
		wantError string
	}{
		{name: "mutation", guard: func() ([]TreeChange, error) {
			return []TreeChange{{Root: roots[1], Paths: []string{"?? stray.txt"}}}, nil
		}, wantError: ErrTreeChanged.Error()},
		{name: "post snapshot error", guard: func() ([]TreeChange, error) { return nil, errors.New("git status failed") }, wantError: "tree guard"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := Run(context.Background(), Options{Roots: roots, Sweeper: sweep, Codex: codex, AfterSweep: tt.guard})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
			if rep == nil || len(codex.requests) != 0 {
				t.Fatalf("report/review = %+v/%v", rep, codex.requests)
			}
			if !strings.Contains(RenderReport(rep), "SAFETY ABORT") {
				t.Error("forensic report missing loud safety abort")
			}
		})
	}
}

func TestRunRequiresAllRootsGuard(t *testing.T) {
	_, err := Run(context.Background(), Options{Roots: []string{"/a", "/b"}, Sweeper: &testChannel{}})
	if err == nil || !strings.Contains(err.Error(), "tree guard is required") {
		t.Fatalf("error = %v", err)
	}
}
