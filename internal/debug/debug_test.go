package debug

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ishaanbatra/styx/internal/progress"
)

const cleanReview = `{"blocking":[],"important":[],"nits":[]}`

type callLog struct {
	mu      sync.Mutex
	entries []string
	prompts map[string]string
}

func (l *callLog) add(name, prompt string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, name)
	if l.prompts == nil {
		l.prompts = map[string]string{}
	}
	l.prompts[name] = prompt
}

func (l *callLog) snapshot() ([]string, map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entries := append([]string(nil), l.entries...)
	prompts := make(map[string]string, len(l.prompts))
	for k, v := range l.prompts {
		prompts[k] = v
	}
	return entries, prompts
}

type fakeChan struct {
	name     string
	response string
	err      error
	log      *callLog
}

func (f *fakeChan) Send(_ context.Context, prompt string) (string, error) {
	if f.log != nil {
		f.log.add(f.name, prompt)
	}
	return f.response, f.err
}

func runWithReviews(t *testing.T, codex, claude *fakeChan) *Report {
	t.Helper()
	rep, err := Run(context.Background(), Options{
		Input:   Input{Bug: "panic on empty input"},
		Sweeper: &fakeChan{name: "sweep", response: "brief body"},
		Codex:   codex, Claude: claude, Prog: progress.Quiet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestRunVerdicts(t *testing.T) {
	tests := []struct {
		name       string
		codex      string
		claude     string
		confirmed  bool
		confidence string
		summary    string
	}{
		{"happy path", cleanReview, cleanReview, true, "high", "survived"},
		{"codex blocking", `{"blocking":["citation is wrong"],"important":[],"nits":[]}`, cleanReview, false, "low", "codex: citation is wrong"},
		{"claude blocking", cleanReview, `{"blocking":["fix masks the cause"],"important":[],"nits":[]}`, false, "low", "claude: fix masks the cause"},
		{"important only", `{"blocking":[],"important":["missing repro"],"nits":[]}`, cleanReview, true, "medium", "survived"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := runWithReviews(t,
				&fakeChan{name: "codex", response: tt.codex},
				&fakeChan{name: "claude", response: tt.claude})
			if rep.Verdict.Confirmed != tt.confirmed || rep.Verdict.Confidence != tt.confidence {
				t.Fatalf("verdict = %+v, want confirmed=%t confidence=%s", rep.Verdict, tt.confirmed, tt.confidence)
			}
			if !strings.Contains(rep.Verdict.Summary, tt.summary) {
				t.Errorf("summary %q missing %q", rep.Verdict.Summary, tt.summary)
			}
		})
	}
}

func TestRunSweepErrorSkipsReviews(t *testing.T) {
	log := &callLog{}
	_, err := Run(context.Background(), Options{
		Input: Input{Bug: "boom"}, Prog: progress.Quiet(),
		Sweeper: &fakeChan{name: "sweep", err: errors.New("agy unavailable"), log: log},
		Codex:   &fakeChan{name: "codex", response: cleanReview, log: log},
		Claude:  &fakeChan{name: "claude", response: cleanReview, log: log},
	})
	if err == nil || !strings.Contains(err.Error(), "agy sweep: agy unavailable") {
		t.Fatalf("want wrapped sweep error, got %v", err)
	}
	entries, _ := log.snapshot()
	if len(entries) != 1 || entries[0] != "sweep" {
		t.Fatalf("calls = %v, want sweep only", entries)
	}
}

func TestRunReviewerErrorStillProducesReport(t *testing.T) {
	rep := runWithReviews(t,
		&fakeChan{name: "codex", err: errors.New("timeout")},
		&fakeChan{name: "claude", response: cleanReview})
	if rep.CodexReview.Err != "timeout" {
		t.Fatalf("codex error = %q", rep.CodexReview.Err)
	}
	if rep.ClaudeReview.Raw != cleanReview {
		t.Fatalf("claude review missing: %+v", rep.ClaudeReview)
	}
	if rep.Verdict.Confidence != "medium" {
		t.Fatalf("confidence = %q, want medium", rep.Verdict.Confidence)
	}
}

func TestRunPersistsBriefBeforeReviews(t *testing.T) {
	log := &callLog{}
	rep, err := Run(context.Background(), Options{
		Input: Input{Bug: "boom"}, Prog: progress.Quiet(),
		Sweeper: &fakeChan{name: "sweep", response: "expensive brief", log: log},
		Codex:   &fakeChan{name: "codex", response: cleanReview, log: log},
		Claude:  &fakeChan{name: "claude", response: cleanReview, log: log},
		PersistBrief: func(got string) (string, error) {
			if got != "expensive brief" {
				t.Errorf("persisted body = %q", got)
			}
			log.add("persist", got)
			return "/tmp/brief.md", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.BriefPath != "/tmp/brief.md" {
		t.Fatalf("brief path = %q", rep.BriefPath)
	}
	entries, _ := log.snapshot()
	if len(entries) != 4 || entries[0] != "sweep" || entries[1] != "persist" {
		t.Fatalf("call order = %v; persist must precede reviews", entries)
	}
}

func TestRunPresetBriefSkipsSweepAndKeepsReviewsIndependent(t *testing.T) {
	log := &callLog{}
	codexText := `{"blocking":[],"important":["CODEX UNIQUE TAKE"],"nits":[]}`
	rep, err := Run(context.Background(), Options{
		Input: Input{Bug: "boom"}, PresetBrief: "PRESET BRIEF", Prog: progress.Quiet(),
		Codex:  &fakeChan{name: "codex", response: codexText, log: log},
		Claude: &fakeChan{name: "claude", response: cleanReview, log: log},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Brief != "PRESET BRIEF" || rep.BriefPath != "" {
		t.Fatalf("preset result = %+v", rep)
	}
	entries, prompts := log.snapshot()
	if len(entries) != 2 {
		t.Fatalf("calls = %v, want reviews only", entries)
	}
	for _, name := range []string{"codex", "claude"} {
		if !strings.Contains(prompts[name], "PRESET BRIEF") {
			t.Errorf("%s prompt missing brief", name)
		}
	}
	if strings.Contains(prompts["claude"], "CODEX UNIQUE TAKE") {
		t.Error("claude prompt was anchored on codex's response")
	}
}

func TestRenderReport(t *testing.T) {
	rep := runWithReviews(t,
		&fakeChan{name: "codex", response: cleanReview},
		&fakeChan{name: "claude", response: cleanReview})
	got := RenderReport(rep)
	for _, want := range []string{"# ultraFerdDebug report", "brief body", "## Codex Review", "## Claude Review", "## Verdict"} {
		if !strings.Contains(got, want) {
			t.Errorf("report missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Sweep modified the working tree") {
		t.Errorf("clean report must not carry a dirty-tree warning:\n%s", got)
	}

	rep.SweepDirtied = []string{"?? stray.txt", " M internal/a.go"}
	dirty := RenderReport(rep)
	for _, want := range []string{"Sweep modified the working tree", "?? stray.txt", " M internal/a.go"} {
		if !strings.Contains(dirty, want) {
			t.Errorf("dirty report missing %q:\n%s", want, dirty)
		}
	}
}
