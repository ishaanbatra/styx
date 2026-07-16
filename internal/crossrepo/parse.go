package crossrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/modeljson"
)

const maxFindings = 500

type rawSweep struct {
	Findings []json.RawMessage `json:"findings"`
}

// ParseFindings recovers and validates cross-repository links from strict,
// fenced, or embedded model JSON. Endpoints must name one of the exact mounted
// roots, and a finding must cross between two different roots.
func ParseFindings(raw string, roots []string) ([]Finding, []string) {
	var envelope rawSweep
	parsed := false
	for _, candidate := range modeljson.Candidates(raw) {
		var decoded rawSweep
		if err := json.Unmarshal([]byte(candidate), &decoded); err == nil && decoded.Findings != nil {
			envelope = decoded
			parsed = true
			break
		}
	}
	if !parsed {
		for start := strings.IndexByte(raw, '{'); start >= 0; {
			var decoded rawSweep
			if err := json.NewDecoder(strings.NewReader(raw[start:])).Decode(&decoded); err == nil && decoded.Findings != nil {
				envelope = decoded
				parsed = true
				break
			}
			next := strings.IndexByte(raw[start+1:], '{')
			if next < 0 {
				break
			}
			start += next + 1
		}
	}
	if !parsed {
		return nil, []string{"agy output did not contain a parseable findings object; all findings skipped"}
	}

	allowed := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		allowed[root] = struct{}{}
	}
	limit := len(envelope.Findings)
	warnings := []string{}
	if limit > maxFindings {
		warnings = append(warnings, fmt.Sprintf("agy returned %d findings; only the first %d were considered", limit, maxFindings))
		limit = maxFindings
	}
	findings := make([]Finding, 0, limit)
	for i, item := range envelope.Findings[:limit] {
		var finding Finding
		if err := json.Unmarshal(item, &finding); err != nil {
			warnings = append(warnings, fmt.Sprintf("finding %d skipped: invalid JSON fields: %v", i+1, err))
			continue
		}
		normalizeFinding(&finding)
		if err := validateFinding(finding, allowed); err != nil {
			warnings = append(warnings, fmt.Sprintf("finding %d skipped: %v", i+1, err))
			continue
		}
		findings = append(findings, finding)
	}
	return findings, warnings
}

func normalizeFinding(f *Finding) {
	f.Producer.Root = strings.TrimSpace(f.Producer.Root)
	f.Producer.Path = cleanPath(f.Producer.Path)
	f.Producer.Symbol = strings.TrimSpace(f.Producer.Symbol)
	f.Consumer.Root = strings.TrimSpace(f.Consumer.Root)
	f.Consumer.Path = cleanPath(f.Consumer.Path)
	f.Consumer.Symbol = strings.TrimSpace(f.Consumer.Symbol)
	f.Relationship = strings.ToLower(strings.TrimSpace(f.Relationship))
	f.Contract = strings.TrimSpace(f.Contract)
	f.Reason = strings.TrimSpace(f.Reason)
}

func cleanPath(path string) string {
	return filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
}

func validateFinding(f Finding, roots map[string]struct{}) error {
	if err := validateSite("producer", f.Producer, roots); err != nil {
		return err
	}
	if err := validateSite("consumer", f.Consumer, roots); err != nil {
		return err
	}
	if f.Producer.Root == f.Consumer.Root {
		return errors.New("producer and consumer must be in different mounted repositories")
	}
	switch f.Relationship {
	case "calls", "imports", "implements", "depends-on", "publishes", "consumes", "configures", "generates", "tests", "other":
	default:
		return fmt.Errorf("relationship %q is not an allowed cross-repository relationship", f.Relationship)
	}
	if f.Contract == "" || len(f.Contract) > 512 || strings.ContainsAny(f.Contract, "\r\n") {
		return errors.New("contract must be a non-empty single-line value of at most 512 bytes")
	}
	if f.Reason == "" {
		return errors.New("reason is required")
	}
	return nil
}

func validateSite(label string, site Site, roots map[string]struct{}) error {
	if _, ok := roots[site.Root]; !ok {
		return fmt.Errorf("%s root %q is not one of the exact mounted roots", label, site.Root)
	}
	if site.Path == "" || site.Path == "." || filepath.IsAbs(site.Path) || site.Path == ".." || strings.HasPrefix(site.Path, "../") {
		return fmt.Errorf("%s path %q must stay within its repository", label, site.Path)
	}
	if site.Line < 1 {
		return fmt.Errorf("%s line must be positive", label)
	}
	if site.Symbol == "" || len(site.Symbol) > 256 || strings.ContainsAny(site.Symbol, "\r\n") {
		return fmt.Errorf("%s symbol must be a non-empty single-line value of at most 256 bytes", label)
	}
	return nil
}
