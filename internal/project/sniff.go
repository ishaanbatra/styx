package project

import (
	"os"
	"path/filepath"
)

// SniffLanguage inspects dir for canonical project files and returns the
// dominant language tag: "python" | "javascript" | "typescript" | "go" |
// "rust" | "mixed" | "unknown".
func SniffLanguage(dir string) string {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(dir, name))
		return err == nil
	}

	langs := []string{}
	switch {
	case has("pyproject.toml"), has("setup.py"), has("requirements.txt"):
		langs = append(langs, "python")
	}
	if has("package.json") {
		if has("tsconfig.json") {
			langs = append(langs, "typescript")
		} else {
			langs = append(langs, "javascript")
		}
	}
	if has("go.mod") {
		langs = append(langs, "go")
	}
	if has("Cargo.toml") {
		langs = append(langs, "rust")
	}

	switch len(langs) {
	case 0:
		return "unknown"
	case 1:
		return langs[0]
	default:
		return "mixed"
	}
}
