package paths

import (
	"path/filepath"
	"testing"
)

func TestConfigDir_RespectsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdgconfig")
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/xdgconfig/styx"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestConfigDir_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/tmp/home")
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp/home", ".config", "styx")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRoutingPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/c")
	got, err := RoutingPath()
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/c/styx/routing.toml"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUsageDBPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/c")
	got, err := UsageDBPath()
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/c/styx/state/usage.db"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestMemoryAndThreadsDirs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	md, err := MemoryDir()
	if err != nil {
		t.Fatalf("MemoryDir: %v", err)
	}
	if md != "/tmp/xdg-test/styx/state/memory" {
		t.Errorf("MemoryDir = %q, want /tmp/xdg-test/styx/state/memory", md)
	}
	td, err := ThreadsDir()
	if err != nil {
		t.Fatalf("ThreadsDir: %v", err)
	}
	if td != "/tmp/xdg-test/styx/state/threads" {
		t.Errorf("ThreadsDir = %q, want /tmp/xdg-test/styx/state/threads", td)
	}
}

func TestAuditDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg-test")
	ad, err := AuditDir()
	if err != nil {
		t.Fatalf("AuditDir: %v", err)
	}
	if ad != "/tmp/xdg-test/styx/state/audit" {
		t.Errorf("AuditDir = %q, want /tmp/xdg-test/styx/state/audit", ad)
	}
}
