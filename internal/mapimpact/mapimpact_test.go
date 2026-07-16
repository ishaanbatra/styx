package mapimpact

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

func TestParseFindings(t *testing.T) {
	valid := `{"source":{"path":"internal/store.go","line":12,"symbol":"Store.Save"},"dependent":{"path":"cmd/app.go","line":20,"symbol":"run"},"relationship":"calls","impact":"direct","reason":"run invokes Store.Save"}`
	tests := []struct {
		name         string
		raw          string
		want         int
		wantWarnings int
	}{
		{name: "strict object", raw: `{"findings":[` + valid + `]}`, want: 1},
		{name: "fenced with one bad edge", raw: "prose\n```json\n" + `{"findings":[` + valid + `,{"source":{"path":"../escape","line":1,"symbol":"x"}}]}` + "\n```", want: 1, wantWarnings: 1},
		{name: "embedded after unrelated braces", raw: `analysis {nope} answer {"findings":[` + valid + `]} trailing`, want: 1},
		{name: "garbage", raw: "not-json", wantWarnings: 1},
		{name: "invalid relationship", raw: `{"findings":[{"source":{"path":"a.go","line":1,"symbol":"A"},"dependent":{"path":"b.go","line":2,"symbol":"B"},"relationship":"resembles","impact":"direct","reason":"claim"}]}`, wantWarnings: 1},
		{name: "invalid impact", raw: `{"findings":[{"source":{"path":"a.go","line":1,"symbol":"A"},"dependent":{"path":"b.go","line":2,"symbol":"B"},"relationship":"calls","impact":"maybe","reason":"claim"}]}`, wantWarnings: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings := ParseFindings(tt.raw)
			if len(got) != tt.want || len(warnings) != tt.wantWarnings {
				t.Fatalf("findings/warnings = %+v/%v, want %d/%d", got, warnings, tt.want, tt.wantWarnings)
			}
		})
	}
}

func TestRunReviewsBoundedSampleOnce(t *testing.T) {
	edges := make([]string, 0, 7)
	for i := 1; i <= 7; i++ {
		edges = append(edges, `{"source":{"path":"source.go","line":1,"symbol":"Source"},"dependent":{"path":"dep.go","line":`+string(rune('0'+i))+`,"symbol":"Dep`+string(rune('0'+i))+`"},"relationship":"calls","impact":"direct","reason":"claimed call"}`)
	}
	sweep := &testChannel{response: `{"findings":[` + strings.Join(edges, ",") + `]}`}
	codex := &testChannel{response: "VERIFIED: dep.go references source.go"}
	afterCalls := 0
	rep, err := Run(context.Background(), Options{
		Input: Input{Kind: "symbol", Value: "Source"}, Sweeper: sweep, Codex: codex,
		AfterSweep: func() []string { afterCalls++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.requests) != 1 || len(codex.requests) != 1 || afterCalls != 1 {
		t.Fatalf("sweep/codex/guard calls = %d/%d/%d", len(sweep.requests), len(codex.requests), afterCalls)
	}
	if len(rep.Findings) != 7 || rep.ReviewedCount != maxReviewSample {
		t.Fatalf("report = %+v", rep)
	}
	if strings.Contains(codex.requests[0], "Dep6") || !strings.Contains(codex.requests[0], "does the dependent site actually") {
		t.Errorf("review prompt is not bounded/specific:\n%s", codex.requests[0])
	}
	if !strings.Contains(sweep.requests[0], `{"kind":"symbol","value":"Source"}`) || !strings.Contains(sweep.requests[0], `"findings"`) {
		t.Errorf("sweep prompt missing target/schema:\n%s", sweep.requests[0])
	}
	rendered := RenderReport(rep)
	const findingsHeader = "## Machine-readable findings\n\n```json\n"
	start := strings.Index(rendered, findingsHeader)
	if start < 0 {
		t.Fatalf("rendered report missing structured findings:\n%s", rendered)
	}
	jsonStart := start + len(findingsHeader)
	jsonEnd := strings.Index(rendered[jsonStart:], "\n```")
	if jsonEnd < 0 || !json.Valid([]byte(rendered[jsonStart:jsonStart+jsonEnd])) {
		t.Fatalf("rendered findings are not valid JSON:\n%s", rendered[jsonStart:])
	}
}

func TestRunMalformedOutputSkipsReview(t *testing.T) {
	sweep := &testChannel{response: "model chatter"}
	codex := &testChannel{response: "should not run"}
	rep, err := Run(context.Background(), Options{
		Input: Input{Kind: "file", Value: "source.go"}, Sweeper: sweep, Codex: codex,
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
		Input: Input{Kind: "diff", Value: "HEAD~1"}, Sweeper: sweep,
		AfterSweep: func() []string { afterCalls++; return []string{"stray.txt"} },
	})
	if err == nil || !strings.Contains(err.Error(), "map-impact sweep: agy failed") {
		t.Fatalf("Run error = %v", err)
	}
	if rep != nil || afterCalls != 1 {
		t.Fatalf("Run report/guard = %+v/%d", rep, afterCalls)
	}
}
