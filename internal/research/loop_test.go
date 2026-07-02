package research

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/progress"
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
	b, err := Loop(context.Background(), "what is X?", drafter, critic, progress.Quiet())
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
	b, err := Loop(context.Background(), "q", drafter, critic, progress.Quiet())
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
	b, err := Loop(context.Background(), "q", drafter, critic, progress.Quiet())
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
	b, err := Loop(context.Background(), "q", drafter, critic, progress.Quiet())
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
	got, err := ChaseSources(context.Background(), urls, fake, progress.Quiet())
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

func TestChaseSources_NarratesPerURL(t *testing.T) {
	// stub summarizer: returns error for URLs containing "bad", summary otherwise.
	stub := func(ctx context.Context, url string) (string, error) {
		if strings.Contains(url, "bad") {
			return "", errors.New("connection refused")
		}
		return "summary of " + url + " (123 chars)", nil
	}

	var buf bytes.Buffer
	prog := progress.New(&buf, false, false)

	urls := []string{"https://good.test/page", "https://bad.test/page", "https://also-good.test/page"}
	got, err := ChaseSources(context.Background(), urls, stub, prog)
	if err != nil {
		t.Fatal(err)
	}

	// Verify returned sources are correct.
	if len(got) != 3 {
		t.Fatalf("got %d sources, want 3", len(got))
	}
	if !strings.Contains(got[1].Summary, "(failed to summarize:") {
		t.Errorf("bad URL source should have failure summary, got %q", got[1].Summary)
	}
	if !strings.Contains(got[0].Summary, "summary of") {
		t.Errorf("good URL source[0] should have summary, got %q", got[0].Summary)
	}
	if !strings.Contains(got[2].Summary, "summary of") {
		t.Errorf("good URL source[2] should have summary, got %q", got[2].Summary)
	}

	out := buf.String()

	// Progress buffer should contain a "[1/" line (per-URL stage).
	if !strings.Contains(out, "[1/") {
		t.Errorf("expected '[1/' in progress output, got:\n%s", out)
	}
	// Should mention the failed URL.
	if !strings.Contains(out, "bad.test") {
		t.Errorf("expected bad URL to appear in progress output, got:\n%s", out)
	}
	// Should contain the final summary stage.
	if !strings.Contains(out, "Source chase complete") {
		t.Errorf("expected 'Source chase complete' in progress output, got:\n%s", out)
	}
	// Should report 2 succeeded, 1 failed.
	if !strings.Contains(out, "2 succeeded") {
		t.Errorf("expected '2 succeeded' in progress output, got:\n%s", out)
	}
	if !strings.Contains(out, "1 failed") {
		t.Errorf("expected '1 failed' in progress output, got:\n%s", out)
	}
}

func TestAgySummarizer_FetchFailureDoesNotInvokeAgy(t *testing.T) {
	// .invalid is an RFC 2606 reserved TLD: DNS resolution deterministically
	// fails, so curl fails without any real network dependency — while
	// hostBlocked still allows the URL (public-looking https host), so this
	// exercises the genuine curl-failure branch, not the SSRF short-circuit.
	const fixture = "https://curl-must-fail.invalid/page"
	if hostBlocked(fixture) {
		t.Fatalf("fixture %q must pass hostBlocked so the test reaches curl", fixture)
	}
	called := 0
	stubAgy := &fakeChan{responses: []string{"agy would have hallucinated this"}}
	wrappedAgy := &countingChannel{inner: stubAgy, count: &called}
	summarize := AgySummarizer(wrappedAgy)
	got, err := summarize(context.Background(), fixture)
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

func TestAgySummarizer_BlockedHostSkipsWithoutFetch(t *testing.T) {
	// Loopback host: the hostBlocked SSRF guard must short-circuit before
	// curl runs, returning the "skipped" marker and never invoking agy.Send.
	called := 0
	stubAgy := &fakeChan{responses: []string{"agy would have hallucinated this"}}
	wrappedAgy := &countingChannel{inner: stubAgy, count: &called}
	summarize := AgySummarizer(wrappedAgy)
	got, err := summarize(context.Background(), "http://127.0.0.1:1/styx-blocked-host-test")
	if err != nil {
		t.Fatalf("blocked host should degrade gracefully, got err: %v", err)
	}
	if called != 0 {
		t.Errorf("agy.Send must not be invoked for a blocked host; got %d calls. Returned: %q", called, got)
	}
	if !strings.HasPrefix(got, "skipped ") || !strings.Contains(got, "private/non-http host") {
		t.Errorf("expected 'skipped <url>: private/non-http host' marker, got %q", got)
	}
}

func TestCurlFetch_RefusesRedirects(t *testing.T) {
	// hostBlocked only vets the initial URL, so a page that 302s to a
	// private/loopback/link-local host (e.g. the AWS metadata service or a
	// local ollama) must not be silently followed. Both servers here are
	// loopback, so we call curlFetch directly rather than going through
	// AgySummarizer/hostBlocked (which would short-circuit the loopback
	// entry URL before curl ever ran).
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.Write([]byte("should never be fetched"))
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	body, err := curlFetch(context.Background(), redirector.URL)
	if err == nil {
		t.Fatalf("expected curlFetch to fail when the server responds with a redirect, got body %q", body)
	}
	if targetHit {
		t.Errorf("curl followed the redirect to the target server; a redirect must never be followed since hostBlocked only vets the initial URL")
	}
}

func TestLoop_EmitsRoundProgress(t *testing.T) {
	// drafter: initial draft then a revised draft.
	drafter := &fakeChan{responses: []string{"draft 1", "draft 2 revised"}}
	// critic: first round has findings (forces a revise), second converges.
	critic := &fakeChan{responses: []string{
		`{"blocking":["missing context"],"important":["weak evidence"],"nits":[]}`,
		`{"blocking":[],"important":[],"nits":[]}`,
	}}

	var buf bytes.Buffer
	prog := progress.New(&buf, false, false)
	b, err := Loop(context.Background(), "test query", drafter, critic, prog)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != "converged" {
		t.Errorf("Status = %q, want converged", b.Status)
	}

	out := buf.String()
	if !strings.Contains(out, "drafting initial response") {
		t.Errorf("expected 'drafting initial response' in progress output, got:\n%s", out)
	}
	if !strings.Contains(out, "critiquing draft") {
		t.Errorf("expected 'critiquing draft' in progress output, got:\n%s", out)
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
