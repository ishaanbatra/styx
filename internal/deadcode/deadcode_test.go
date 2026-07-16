package deadcode

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

func TestRunReviewsConfirmedSampleOnce(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "dead.go"), []byte("func lonely() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sweep := &testChannel{response: `{"findings":[{"kind":"function","symbol":"lonely","definition":{"path":"dead.go","line":1},"reason":"no callers"}]}`}
	codex := &testChannel{response: "UPHELD: dead.go:1 has no callers"}
	afterCalls := 0
	rep, err := Run(context.Background(), Options{
		Input: Input{Target: ".", ProjectPath: root}, Sweeper: sweep, Codex: codex,
		AfterSweep: func() []string { afterCalls++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.requests) != 1 || len(codex.requests) != 1 || afterCalls != 1 {
		t.Fatalf("sweep/codex/guard calls = %d/%d/%d", len(sweep.requests), len(codex.requests), afterCalls)
	}
	if len(rep.Findings) != 1 || rep.Findings[0].Status != "CONFIRMED" || rep.ReviewedCount != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if !strings.Contains(sweep.requests[0], "Return ONLY one JSON object") || !strings.Contains(codex.requests[0], "lonely") {
		t.Errorf("prompts missing structure/sample: sweep=%q codex=%q", sweep.requests[0], codex.requests[0])
	}
}

func TestRunGarbageSkipsReviewWithoutCrashing(t *testing.T) {
	root := t.TempDir()
	sweep := &testChannel{response: "model chatter"}
	codex := &testChannel{response: "should not run"}
	rep, err := Run(context.Background(), Options{
		Input: Input{Target: ".", ProjectPath: root}, Sweeper: sweep, Codex: codex,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 || len(rep.Warnings) == 0 || len(codex.requests) != 0 {
		t.Fatalf("garbage report/reviewer = %+v/%v", rep, codex.requests)
	}
	if !strings.Contains(RenderReport(rep), "all findings skipped") {
		t.Error("rendered report omitted parser warning")
	}
}

func TestRunGuardsTreeWhenSweepFails(t *testing.T) {
	sweep := &testChannel{err: errors.New("agy failed")}
	afterCalls := 0
	rep, err := Run(context.Background(), Options{
		Input:   Input{Target: ".", ProjectPath: t.TempDir()},
		Sweeper: sweep,
		AfterSweep: func() []string {
			afterCalls++
			return []string{"stray.txt"}
		},
	})
	if err == nil || !strings.Contains(err.Error(), "dead-code sweep: agy failed") {
		t.Fatalf("Run error = %v", err)
	}
	if rep != nil {
		t.Fatalf("Run report = %+v, want nil on sweep failure", rep)
	}
	if afterCalls != 1 {
		t.Fatalf("tree guard calls = %d, want 1", afterCalls)
	}
}

func TestConfirmedSampleIsBoundedAndStable(t *testing.T) {
	findings := make([]VerifiedFinding, 0, 7)
	for _, symbol := range []string{"one", "refuted", "two", "three", "four", "five", "six"} {
		status := "CONFIRMED"
		if symbol == "refuted" {
			status = "REFUTED"
		}
		findings = append(findings, VerifiedFinding{
			Finding: Finding{Symbol: symbol},
			Status:  status,
		})
	}
	got := confirmedSample(findings, maxReviewSample)
	if len(got) != maxReviewSample {
		t.Fatalf("sample size = %d, want %d", len(got), maxReviewSample)
	}
	var symbols []string
	for _, finding := range got {
		symbols = append(symbols, finding.Symbol)
	}
	if joined := strings.Join(symbols, ","); joined != "one,two,three,four,five" {
		t.Fatalf("sample symbols = %q", joined)
	}
}
