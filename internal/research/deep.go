package research

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
)

// maxFetchedBodyBytes caps how much of a fetched page we embed in the prompt
// to keep agy invocations cheap. 80KB is a rough proxy that fits typical
// article/blog content while staying well under model context limits.
const maxFetchedBodyBytes = 80_000

// Summarizer fetches a URL and returns a summary. Production passes an agy-backed
// implementation; tests pass an in-memory map.
type Summarizer func(ctx context.Context, url string) (string, error)

var urlRE = regexp.MustCompile(`https?://[^\s)>\]]+`)

// ExtractURLs returns a deduplicated slice of URLs found in body.
func ExtractURLs(body string) []string {
	matches := urlRE.FindAllString(body, -1)
	seen := map[string]bool{}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// ChaseSources calls summarize for each URL serially. Returns one Source per URL.
// (Serial keeps it simple — parallelism can come later if performance demands it.)
func ChaseSources(ctx context.Context, urls []string, summarize Summarizer) ([]Source, error) {
	out := make([]Source, 0, len(urls))
	for _, u := range urls {
		s, err := summarize(ctx, u)
		if err != nil {
			out = append(out, Source{URL: u, Summary: "(failed to summarize: " + err.Error() + ")"})
			continue
		}
		out = append(out, Source{URL: u, Summary: s})
	}
	return out, nil
}

// AgySummarizer returns a Summarizer that fetches the URL via curl, embeds
// the (truncated) page body into the prompt, and asks agy to summarize.
//
// If curl fails or returns empty, AgySummarizer returns a "fetch failed" line
// without ever calling agy — that path was the source of hallucinated
// summaries when the model invented content for unreachable URLs.
func AgySummarizer(agy Channel) Summarizer {
	return func(ctx context.Context, url string) (string, error) {
		cmd := exec.CommandContext(ctx, "curl", "-fsSL", "--max-time", "20",
			"-A", "styx/v0.2", url)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return "fetch failed for " + url + ": " + err.Error(), nil
		}
		body := stdout.Bytes()
		if len(body) == 0 {
			return "fetch failed for " + url + ": empty response body", nil
		}
		if len(body) > maxFetchedBodyBytes {
			body = body[:maxFetchedBodyBytes]
		}
		prompt := "Summarize the page at " + url + " in <=200 words based ONLY on the page body below. " +
			"Focus on claims a senior engineer would care about. Do not invent content not present in the body. " +
			"If the body looks like a paywall, login wall, or error page, say so in one line.\n\n" +
			"--- FETCHED PAGE BODY ---\n" +
			string(body) + "\n" +
			"--- END BODY ---"
		return agy.Send(ctx, prompt)
	}
}
