package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/paths"
)

func TestMigrateProjectStateIdempotent(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)

	p := Project{Name: "backend", Path: "/repos/backend", ID: ProjectID("/repos/backend")}

	// Seed legacy Name-keyed state.
	memDir, err := paths.MemoryDir()
	if err != nil {
		t.Fatalf("memory dir: %v", err)
	}
	auditDir, err := paths.AuditDir()
	if err != nil {
		t.Fatalf("audit dir: %v", err)
	}
	threadsDir, err := paths.ThreadsDir()
	if err != nil {
		t.Fatalf("threads dir: %v", err)
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	intelDir := filepath.Join(stateDir, "intel")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(auditDir, p.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(intelDir, p.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memDir, p.Name+".db"), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(threadsDir, p.Name+".json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func() {
		if err := MigrateProjectState([]Project{p}); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	run()
	run() // idempotent: second run must not error or undo.

	if _, err := os.Stat(filepath.Join(memDir, p.ID+".db")); err != nil {
		t.Errorf("memory db not migrated to ID key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(memDir, p.Name+".db")); !os.IsNotExist(err) {
		t.Errorf("legacy memory db still present")
	}
	if _, err := os.Stat(filepath.Join(auditDir, p.ID)); err != nil {
		t.Errorf("audit dir not migrated to ID key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(threadsDir, p.ID+".json")); err != nil {
		t.Errorf("threads file not migrated to ID key: %v", err)
	}
	if _, err := os.Stat(filepath.Join(intelDir, p.ID)); err != nil {
		t.Errorf("intel dir not migrated to ID key: %v", err)
	}
}
