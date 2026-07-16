package deadcode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ishaanbatra/styx/internal/modeljson"
)

const maxFindings = 500

type rawSweep struct {
	Findings []json.RawMessage `json:"findings"`
}

// ParseFindings extracts findings from strict JSON, fenced JSON, or a JSON
// object embedded in surrounding model chatter. Bad entries are skipped.
func ParseFindings(raw string) ([]Finding, []string) {
	candidates := modeljson.Candidates(raw)
	var envelope rawSweep
	parsed := false
	for _, candidate := range candidates {
		var decoded rawSweep
		if err := json.Unmarshal([]byte(candidate), &decoded); err == nil && decoded.Findings != nil {
			envelope = decoded
			parsed = true
			break
		}
	}
	// Model chatter can contain unrelated braces around the actual object. Try
	// decoding from every object start so valid structured output remains
	// recoverable without accepting a made-up schema.
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
		finding.Kind = strings.ToLower(strings.TrimSpace(finding.Kind))
		finding.Symbol = strings.TrimSpace(finding.Symbol)
		finding.Definition.Path = filepath.ToSlash(filepath.Clean(strings.TrimSpace(finding.Definition.Path)))
		finding.Reason = strings.TrimSpace(finding.Reason)
		if err := validateFinding(finding); err != nil {
			warnings = append(warnings, fmt.Sprintf("finding %d skipped: %v", i+1, err))
			continue
		}
		findings = append(findings, finding)
	}
	return findings, warnings
}

func validateFinding(f Finding) error {
	switch f.Kind {
	case "file", "function", "import":
	default:
		return fmt.Errorf("kind %q is not file, function, or import", f.Kind)
	}
	if f.Symbol == "" || len(f.Symbol) > 256 || strings.ContainsAny(f.Symbol, "\r\n") {
		return errors.New("symbol must be a non-empty single-line token of at most 256 bytes")
	}
	if f.Definition.Path == "" || f.Definition.Path == "." || filepath.IsAbs(f.Definition.Path) || f.Definition.Path == ".." || strings.HasPrefix(f.Definition.Path, "../") {
		return fmt.Errorf("definition path %q must stay within the repository", f.Definition.Path)
	}
	if f.Definition.Line < 1 {
		return errors.New("definition line must be positive")
	}
	if f.Reason == "" {
		return errors.New("reason is required")
	}
	return nil
}

// Verify performs one deterministic repository scan for all accepted symbols.
// A finding is CONFIRMED only when no whole-word match exists outside its
// reported definition line; any match conservatively makes it REFUTED.
func Verify(ctx context.Context, root string, findings []Finding) ([]VerifiedFinding, []string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, nil, fmt.Errorf("stat repository root: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("repository root %s is not a directory", root)
	}

	verified := make([]VerifiedFinding, 0, len(findings))
	warnings := []string{}
	for _, finding := range findings {
		definition := filepath.Join(root, filepath.FromSlash(finding.Definition.Path))
		if !pathWithin(root, definition) {
			warnings = append(warnings, fmt.Sprintf("%s skipped: definition path escapes repository", finding.Symbol))
			continue
		}
		if _, err := os.Stat(definition); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s skipped: stat definition %s: %v", finding.Symbol, finding.Definition.Path, err))
			continue
		}
		verified = append(verified, VerifiedFinding{Finding: finding})
	}
	if len(verified) == 0 {
		return verified, warnings, nil
	}

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			warnings = append(warnings, fmt.Sprintf("scan %s: %v", displayPath(root, path), walkErr))
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if err := scanFile(root, path, verified); err != nil {
			warnings = append(warnings, fmt.Sprintf("scan %s: %v", displayPath(root, path), err))
		}
		return nil
	})
	if err != nil {
		return nil, warnings, err
	}
	for i := range verified {
		sort.Slice(verified[i].References, func(a, b int) bool {
			if verified[i].References[a].Path != verified[i].References[b].Path {
				return verified[i].References[a].Path < verified[i].References[b].Path
			}
			return verified[i].References[a].Line < verified[i].References[b].Line
		})
		if len(verified[i].References) == 0 {
			verified[i].Status = "CONFIRMED"
		} else {
			verified[i].Status = "REFUTED"
		}
	}
	return verified, warnings, nil
}

func scanFile(root, path string, findings []VerifiedFinding) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	peek, err := reader.Peek(8192)
	if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
		return err
	}
	if bytes.IndexByte(peek, 0) >= 0 || !utf8.Valid(peek) {
		return nil
	}
	rel := displayPath(root, path)
	for lineNo := 1; ; lineNo++ {
		line, readErr := reader.ReadString('\n')
		for i := range findings {
			if rel == findings[i].Definition.Path && lineNo == findings[i].Definition.Line {
				continue
			}
			if containsWholeWord(line, findings[i].Symbol) {
				findings[i].References = append(findings[i].References, Reference{Path: rel, Line: lineNo})
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func containsWholeWord(line, symbol string) bool {
	for start := 0; ; {
		idx := strings.Index(line[start:], symbol)
		if idx < 0 {
			return false
		}
		idx += start
		beforeOK := idx == 0 || !identifierRuneBefore(line[:idx])
		after := idx + len(symbol)
		afterOK := after == len(line) || !identifierRuneAfter(line[after:])
		if beforeOK && afterOK {
			return true
		}
		start = idx + len(symbol)
		if start >= len(line) {
			return false
		}
	}
}

func identifierRuneBefore(s string) bool {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func identifierRuneAfter(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func displayPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}
