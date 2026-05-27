package research

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeChan emits canned responses for each call.
type fakeChan struct {
	responses []string
	calls     int
}

func (f *fakeChan) Send(ctx context.Context, prompt string) (string, error) {
	if f.calls >= len(f.responses) {
		return "", errors.New("ran out of canned responses")
	}
	r := f.responses[f.calls]
	f.calls++
	return r, nil
}

func TestLoop_ConvergesImmediately(t *testing.T) {
	drafter := &fakeChan{responses: []string{"draft 1"}}
	critic := &fakeChan{responses: []string{
		`{"blocking":[],"important":[],"nits":["small typo"]}`,
	}}
	b, err := Loop(context.Background(), "what is X?", drafter, critic)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "converged" {
		t.Errorf("Status = %q, want converged", b.Status)
	}
	if len(b.Drafts) != 1 {
		t.Errorf("Drafts = %d, want 1", len(b.Drafts))
	}
	if drafter.calls != 1 {
		t.Errorf("drafter calls = %d, want 1", drafter.calls)
	}
	if critic.calls != 1 {
		t.Errorf("critic calls = %d, want 1", critic.calls)
	}
}

func TestLoop_ConvergesAfterRevise(t *testing.T) {
	drafter := &fakeChan{responses: []string{"draft 1", "draft 2 (revised)"}}
	critic := &fakeChan{responses: []string{
		`{"blocking":["a","b"],"important":["c"],"nits":[]}`,
		`{"blocking":[],"important":[],"nits":[]}`,
	}}
	b, err := Loop(context.Background(), "q", drafter, critic)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "converged" {
		t.Errorf("Status = %q, want converged", b.Status)
	}
	if len(b.Drafts) != 2 {
		t.Errorf("Drafts = %d, want 2", len(b.Drafts))
	}
}

func TestLoop_MaxRoundsExhausted(t *testing.T) {
	// 6 rounds + 1 initial draft = 7 drafter calls, 6 critic calls.
	drafts := []string{
		"d1", "d2-different", "d3-different",
		"d4-different", "d5-different", "d6-different", "d7-different",
	}
	criticBlocking := `{"blocking":["x"],"important":[],"nits":[]}`
	criticResponses := []string{
		criticBlocking, criticBlocking, criticBlocking,
		criticBlocking, criticBlocking, criticBlocking,
	}
	drafter := &fakeChan{responses: drafts}
	critic := &fakeChan{responses: criticResponses}
	b, err := Loop(context.Background(), "q", drafter, critic)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "max_rounds_exhausted" {
		t.Errorf("Status = %q, want max_rounds_exhausted", b.Status)
	}
}

func TestLoop_OscillatesAndBails(t *testing.T) {
	// drafter alternates between A and B; critic always says blocking.
	// After round 3 (draft index 3) we should detect draft[3] == draft[1].
	drafter := &fakeChan{responses: []string{"A", "B", "A", "B"}}
	critic := &fakeChan{responses: []string{
		`{"blocking":["x"],"important":[],"nits":[]}`,
		`{"blocking":["x"],"important":[],"nits":[]}`,
		`{"blocking":["x"],"important":[],"nits":[]}`,
	}}
	b, err := Loop(context.Background(), "q", drafter, critic)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "oscillating" {
		t.Errorf("Status = %q, want oscillating; drafts=%v", b.Status, b.Drafts)
	}
}

func TestRenderBrief_HasAllSections(t *testing.T) {
	b := &Brief{
		Query:  "q",
		Status: "converged",
		Drafts: []string{"final draft"},
		Critiques: []Critique{
			{Blocking: []string{"a"}, Important: []string{"b"}, Nits: []string{"n"}},
			{Nits: []string{"only-nit"}},
		},
		DrafterChannel: "agy",
		CriticChannel:  "codex",
	}
	out := RenderBrief(b)
	for _, want := range []string{
		"# Research Brief",
		"q",
		"converged",
		"final draft",
		"Convergence Trace",
		"Remaining Nits",
		"only-nit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q. out:\n%s", want, out)
		}
	}
}

func TestExtractURLs(t *testing.T) {
	body := `Source: https://example.com/foo and also https://example.org/bar.
Visit http://nope.test/baz too.
Duplicate https://example.com/foo should not appear twice.`
	got := ExtractURLs(body)
	if len(got) != 3 {
		t.Errorf("want 3 unique URLs, got %d: %v", len(got), got)
	}
}

func TestChaseSources_SummarizesEachURL(t *testing.T) {
	// fakeSummarizer returns a deterministic summary per URL.
	fake := func(ctx context.Context, url string) (string, error) {
		return "summary of " + url, nil
	}
	urls := []string{"https://a.test", "https://b.test"}
	got, err := ChaseSources(context.Background(), urls, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sources, want 2", len(got))
	}
	if got[0].URL != "https://a.test" || !strings.Contains(got[0].Summary, "summary of") {
		t.Errorf("source 0 unexpected: %+v", got[0])
	}
}

func TestAgySummarizer_FetchFailureDoesNotInvokeAgy(t *testing.T) {
	// Use an unroutable URL so curl fails fast.
	called := 0
	stubAgy := &fakeChan{responses: []string{"agy would have hallucinated this"}}
	wrappedAgy := &countingChannel{inner: stubAgy, count: &called}
	summarize := AgySummarizer(wrappedAgy)
	got, err := summarize(context.Background(), "http://127.0.0.1:1/styx-fetch-fail-test")
	if err != nil {
		// Either error or graceful-degrade string is fine; just don't hallucinate.
	}
	if called != 0 {
		t.Errorf("agy.Send must not be invoked when curl fails; got %d calls. Returned: %q", called, got)
	}
	if !strings.Contains(strings.ToLower(got), "fetch failed") && err == nil {
		t.Errorf("expected 'fetch failed' marker or non-nil err, got %q", got)
	}
}

// countingChannel wraps a Channel and counts how many Send calls it gets.
type countingChannel struct {
	inner Channel
	count *int
}

func (c *countingChannel) Send(ctx context.Context, prompt string) (string, error) {
	*c.count++
	return c.inner.Send(ctx, prompt)
}
