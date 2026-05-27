package intel

import (
	"testing"
)

func TestSniff_PythonProject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "pyproject.toml", "[tool.pytest.ini_options]\n[tool.mypy]\nstrict = true\n")
	writeFile(t, root, "tests/test_foo.py", "")
	writeFile(t, root, "app/__init__.py", "")
	writeFile(t, root, "app/main.py", "from app.routes import x\n")

	c := Sniff(root)
	if c.TestFramework != "pytest" {
		t.Errorf("TestFramework = %q, want pytest", c.TestFramework)
	}
	if c.TypeSystem == "" {
		t.Errorf("expected non-empty TypeSystem")
	}
}

func TestSniff_TypeScriptProject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "package.json", `{"scripts":{"test":"jest"}}`)
	writeFile(t, root, "tsconfig.json", "{}")
	c := Sniff(root)
	if c.TestFramework != "jest" {
		t.Errorf("TestFramework = %q, want jest", c.TestFramework)
	}
	if c.TypeSystem != "typescript" {
		t.Errorf("TypeSystem = %q, want typescript", c.TypeSystem)
	}
}

func TestSniff_GoProject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module x\n")
	writeFile(t, root, "main.go", "package main\n")
	c := Sniff(root)
	if c.TestFramework != "go test" {
		t.Errorf("TestFramework = %q, want 'go test'", c.TestFramework)
	}
}

func TestSniff_RustProject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Cargo.toml", "[package]\nname = \"x\"\n")
	c := Sniff(root)
	if c.TestFramework != "cargo test" {
		t.Errorf("TestFramework = %q, want 'cargo test'", c.TestFramework)
	}
}

func TestSniff_Empty(t *testing.T) {
	c := Sniff(t.TempDir())
	if c.TestFramework != "" {
		t.Errorf("TestFramework should be empty for empty dir, got %q", c.TestFramework)
	}
}
