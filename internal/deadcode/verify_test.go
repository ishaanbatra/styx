package deadcode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFindings(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantSymbols  []string
		wantWarnings int
	}{
		{
			name:        "strict object",
			raw:         `{"findings":[{"kind":"function","symbol":"lonely","definition":{"path":"a.go","line":3},"reason":"no callers"}]}`,
			wantSymbols: []string{"lonely"},
		},
		{
			name:        "fenced object with one bad finding",
			raw:         "prose\n```json\n" + `{"findings":[{"kind":"import","symbol":"unusedpkg","definition":{"path":"a.go","line":2},"reason":"not used"},{"kind":"mystery","symbol":"x","definition":{"path":"a.go","line":1},"reason":"bad"}]}` + "\n```",
			wantSymbols: []string{"unusedpkg"}, wantWarnings: 1,
		},
		{
			name:        "embedded object among unrelated braces",
			raw:         `analysis {not-json} result {"findings":[{"kind":"file","symbol":"orphan.go","definition":{"path":"orphan.go","line":1},"reason":"not referenced"}]} trailing {chatter}`,
			wantSymbols: []string{"orphan.go"},
		},
		{name: "garbage", raw: "not json at all", wantWarnings: 1},
		{name: "invalid fields", raw: `{"findings":[{"kind":"function","symbol":7}]}`, wantWarnings: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warnings := ParseFindings(tt.raw)
			var symbols []string
			for _, finding := range got {
				symbols = append(symbols, finding.Symbol)
			}
			if strings.Join(symbols, ",") != strings.Join(tt.wantSymbols, ",") {
				t.Errorf("symbols = %v, want %v", symbols, tt.wantSymbols)
			}
			if len(warnings) != tt.wantWarnings {
				t.Errorf("warnings = %v, want %d", warnings, tt.wantWarnings)
			}
		})
	}
}

func TestVerifyWholeWordOutsideDefinition(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("defs.go", "func lonely() {}\nfunc used() {}\nfunc unusedExtra() {}\n")
	write("calls.go", "package sample\nfunc call() { used() }\n")
	findings := []Finding{
		{Kind: "function", Symbol: "lonely", Definition: Definition{Path: "defs.go", Line: 1}, Reason: "no callers"},
		{Kind: "function", Symbol: "used", Definition: Definition{Path: "defs.go", Line: 2}, Reason: "claimed unused"},
		{Kind: "function", Symbol: "unused", Definition: Definition{Path: "defs.go", Line: 3}, Reason: "prefix only"},
	}
	got, warnings, err := Verify(context.Background(), root, findings)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 || len(got) != 3 {
		t.Fatalf("verified/warnings = %v/%v", got, warnings)
	}
	if got[0].Status != "CONFIRMED" || got[1].Status != "REFUTED" || got[2].Status != "CONFIRMED" {
		t.Errorf("statuses = %s/%s/%s", got[0].Status, got[1].Status, got[2].Status)
	}
	if len(got[1].References) != 1 || got[1].References[0].Path != "calls.go" || got[1].References[0].Line != 2 {
		t.Errorf("used references = %+v", got[1].References)
	}
}

func TestVerifySkipsInvalidDefinitionPath(t *testing.T) {
	root := t.TempDir()
	findings := []Finding{{Kind: "function", Symbol: "gone", Definition: Definition{Path: "missing.go", Line: 1}, Reason: "missing"}}
	got, warnings, err := Verify(context.Background(), root, findings)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "stat definition") {
		t.Fatalf("verified/warnings = %v/%v", got, warnings)
	}
}
