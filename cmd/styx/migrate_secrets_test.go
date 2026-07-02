package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateRewriteDeletesAndTightens(t *testing.T) {
	dir := t.TempDir()
	rc := filepath.Join(dir, ".zshrc")
	orig := "export PATH=$PATH:/opt\nexport FOO_API_KEY=sekret\nalias ll='ls -l'\n"
	if err := os.WriteFile(rc, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	// call the rewrite helper directly (extract it if currently inline):
	if err := rewriteRC(rc, []string{"export FOO_API_KEY=sekret"}); err != nil {
		t.Fatalf("rewriteRC: %v", err)
	}
	got, _ := os.ReadFile(rc)
	if strings.Contains(string(got), "sekret") {
		t.Fatalf("secret still present:\n%s", got)
	}
	if !strings.Contains(string(got), "alias ll") || !strings.Contains(string(got), "/opt") {
		t.Fatalf("unrelated lines lost:\n%s", got)
	}
	fi, _ := os.Stat(rc)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("rc perms = %o, want 0600", fi.Mode().Perm())
	}
	bak, err := os.ReadFile(rc + ".styx-bak")
	if err != nil || string(bak) != orig {
		t.Fatalf("backup missing or wrong: %v", err)
	}
	bfi, _ := os.Stat(rc + ".styx-bak")
	if bfi.Mode().Perm() != 0o600 {
		t.Fatalf("backup perms = %o, want 0600", bfi.Mode().Perm())
	}
}
