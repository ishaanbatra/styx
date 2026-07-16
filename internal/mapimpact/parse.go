package mapimpact

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

// ParseFindings recovers and validates machine-readable dependency edges from
// strict JSON, fenced JSON, or a JSON object embedded in model chatter.
func ParseFindings(raw string) ([]Finding, []string) {
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
		if err := validateFinding(finding); err != nil {
			warnings = append(warnings, fmt.Sprintf("finding %d skipped: %v", i+1, err))
			continue
		}
		findings = append(findings, finding)
	}
	return findings, warnings
}

func normalizeFinding(f *Finding) {
	f.Source.Path = cleanPath(f.Source.Path)
	f.Source.Symbol = strings.TrimSpace(f.Source.Symbol)
	f.Dependent.Path = cleanPath(f.Dependent.Path)
	f.Dependent.Symbol = strings.TrimSpace(f.Dependent.Symbol)
	f.Relationship = strings.ToLower(strings.TrimSpace(f.Relationship))
	f.Impact = strings.ToLower(strings.TrimSpace(f.Impact))
	f.Reason = strings.TrimSpace(f.Reason)
}

func cleanPath(path string) string {
	return filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
}

func validateFinding(f Finding) error {
	if err := validateSite("source", f.Source); err != nil {
		return err
	}
	if err := validateSite("dependent", f.Dependent); err != nil {
		return err
	}
	if f.Source == f.Dependent {
		return errors.New("source and dependent must identify different sites")
	}
	switch f.Relationship {
	case "calls", "imports", "implements", "configures", "tests", "other":
	default:
		return fmt.Errorf("relationship %q is not calls, imports, implements, configures, tests, or other", f.Relationship)
	}
	switch f.Impact {
	case "direct", "transitive":
	default:
		return fmt.Errorf("impact %q is not direct or transitive", f.Impact)
	}
	if f.Reason == "" {
		return errors.New("reason is required")
	}
	return nil
}

func validateSite(label string, site Site) error {
	if site.Path == "" || site.Path == "." || filepath.IsAbs(site.Path) || site.Path == ".." || strings.HasPrefix(site.Path, "../") {
		return fmt.Errorf("%s path %q must stay within the repository", label, site.Path)
	}
	if site.Line < 1 {
		return fmt.Errorf("%s line must be positive", label)
	}
	if site.Symbol == "" || len(site.Symbol) > 256 || strings.ContainsAny(site.Symbol, "\r\n") {
		return fmt.Errorf("%s symbol must be a non-empty single-line value of at most 256 bytes", label)
	}
	return nil
}
