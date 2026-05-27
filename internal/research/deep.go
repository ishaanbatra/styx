package research

import (
	"context"
	"regexp"
)

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

// AgySummarizer returns a Summarizer that fetches the URL via curl into a tmpfile
// and asks agy to summarize the local copy. This is the production path.
func AgySummarizer(agy Channel) Summarizer {
	return func(ctx context.Context, url string) (string, error) {
		prompt := "Summarize the page at " + url + " in <=200 words. Focus on claims a senior engineer would care about. " +
			"If the page is unreachable or paywalled, say so in one line."
		return agy.Send(ctx, prompt)
	}
}
