package intel

import (
	"os"
	"path/filepath"
	"strings"
)

// Conventions describes the deterministic-to-detect project conventions.
type Conventions struct {
	TestFramework string `json:"test_framework,omitempty"` // "pytest" | "jest" | "vitest" | "go test" | "cargo test"
	TypeSystem    string `json:"type_system,omitempty"`    // "mypy strict" | "typescript" | etc.
	Naming        string `json:"naming,omitempty"`         // "snake_case modules, PascalCase classes" etc.
	Imports       string `json:"imports,omitempty"`        // "absolute from app.*" | "relative" | ""
	ErrorHandling string `json:"error_handling,omitempty"` // free text
}

// Sniff inspects root and returns detected conventions. Deterministic, no LLM.
func Sniff(root string) Conventions {
	c := Conventions{}
	c.TestFramework = sniffTestFramework(root)
	c.TypeSystem = sniffTypeSystem(root)
	c.Naming = sniffNaming(root)
	c.Imports = sniffImports(root)
	c.ErrorHandling = sniffErrorHandling(root)
	return c
}

func exists(root, rel string) bool {
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

func readFile(root, rel string) string {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return ""
	}
	return string(b)
}

func sniffTestFramework(root string) string {
	if exists(root, "pyproject.toml") {
		pp := readFile(root, "pyproject.toml")
		if strings.Contains(pp, "pytest") {
			return "pytest"
		}
	}
	if exists(root, "package.json") {
		pj := readFile(root, "package.json")
		switch {
		case strings.Contains(pj, "\"vitest\""):
			return "vitest"
		case strings.Contains(pj, "\"jest\""):
			return "jest"
		}
	}
	if exists(root, "go.mod") {
		return "go test"
	}
	if exists(root, "Cargo.toml") {
		return "cargo test"
	}
	if exists(root, "setup.py") || exists(root, "requirements.txt") {
		return "pytest"
	}
	return ""
}

func sniffTypeSystem(root string) string {
	if exists(root, "tsconfig.json") {
		return "typescript"
	}
	if exists(root, "mypy.ini") {
		return "mypy"
	}
	if exists(root, "pyproject.toml") && strings.Contains(readFile(root, "pyproject.toml"), "[tool.mypy]") {
		if strings.Contains(readFile(root, "pyproject.toml"), "strict = true") {
			return "mypy strict"
		}
		return "mypy"
	}
	if exists(root, "go.mod") {
		return "go static typing"
	}
	if exists(root, "Cargo.toml") {
		return "rust static typing"
	}
	return ""
}

func sniffNaming(root string) string {
	// Sample top-level dirs; if snake_case dominates, report it.
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	snake, kebab, camel := 0, 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		if strings.Contains(n, "_") {
			snake++
		} else if strings.Contains(n, "-") {
			kebab++
		} else if n != strings.ToLower(n) {
			camel++
		}
	}
	switch {
	case snake > kebab && snake > camel:
		return "snake_case dirs/modules"
	case kebab > snake && kebab > camel:
		return "kebab-case dirs"
	case camel > 0:
		return "mixed-case dirs"
	}
	return ""
}

func sniffImports(root string) string {
	// Python: look for "from app." or "from src." absolute imports in 3 sample files.
	if exists(root, "pyproject.toml") || exists(root, "setup.py") {
		var sampleAbs, sampleRel int
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".py") {
				return nil
			}
			b, _ := os.ReadFile(path)
			s := string(b)
			if strings.Contains(s, "\nfrom .") || strings.Contains(s, "\nfrom ..") {
				sampleRel++
			}
			if strings.Contains(s, "\nfrom app.") || strings.Contains(s, "\nfrom src.") {
				sampleAbs++
			}
			if sampleAbs+sampleRel >= 5 {
				return filepath.SkipAll
			}
			return nil
		})
		if sampleAbs > sampleRel {
			return "absolute"
		}
		if sampleRel > sampleAbs {
			return "relative"
		}
	}
	return ""
}

func sniffErrorHandling(root string) string {
	if exists(root, "go.mod") {
		return "explicit error returns"
	}
	if exists(root, "Cargo.toml") {
		return "Result + ? operator"
	}
	if exists(root, "pyproject.toml") || exists(root, "setup.py") {
		return "exceptions"
	}
	if exists(root, "tsconfig.json") || exists(root, "package.json") {
		return "thrown errors / Promise rejection"
	}
	return ""
}
