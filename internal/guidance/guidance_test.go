package guidance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // match paths_test.go convention

	t.Run("seeds default on first load", func(t *testing.T) {
		got, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if !strings.Contains(got, "codex") || !strings.Contains(got, "ship") {
			t.Fatalf("seed missing routing/ship content:\n%s", got)
		}
	})

	t.Run("user edits survive", func(t *testing.T) {
		Load(t.TempDir()) // ensure seeded
		p, _ := guidanceFile()
		if err := os.WriteFile(p, []byte("MY RULES"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, _ := Load(t.TempDir())
		if got != "MY RULES" {
			t.Fatalf("must not overwrite user file, got %q", got)
		}
	})

	t.Run("seed defaults substantive work to dispatch", func(t *testing.T) {
		// Regression: a styx-launched session used zero styx tools for a
		// research prompt because the seed read as an optional routing table.
		// The seed must set dispatch as the default over the host's built-in
		// subagents, state the quota/ledger economics, and map research tasks.
		for _, want := range []string{
			"BY DEFAULT",
			"Agent/Task subagents",
			"budget ledger",
			"pipeline_run research",
		} {
			if !strings.Contains(Seed, want) {
				t.Errorf("seed missing %q", want)
			}
		}
	})

	t.Run("unmodified legacy seed upgrades to current", func(t *testing.T) {
		Load(t.TempDir()) // ensure seeded
		p, _ := guidanceFile()
		if err := os.WriteFile(p, []byte(seedV1), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("Load over legacy seed: %v", err)
		}
		if got != Seed {
			t.Fatalf("legacy unmodified seed must upgrade to current Seed, got:\n%s", got)
		}
		b, _ := os.ReadFile(p)
		if string(b) != Seed {
			t.Fatalf("guidance file on disk must be rewritten to current Seed")
		}
	})

	t.Run("unmodified v2 seed upgrades to current", func(t *testing.T) {
		Load(t.TempDir()) // ensure seeded
		p, _ := guidanceFile()
		if err := os.WriteFile(p, []byte(seedV2), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := Load(t.TempDir())
		if err != nil {
			t.Fatalf("Load over v2 seed: %v", err)
		}
		if got != Seed {
			t.Fatalf("v2 unmodified seed must upgrade to current Seed, got:\n%s", got)
		}
	})

	t.Run("seed names the fable tier", func(t *testing.T) {
		if !strings.Contains(Seed, "fable = the most demanding") {
			t.Errorf("seed model-tier line must name fable, got:\n%s", Seed)
		}
	})

	t.Run("per-repo override appended", func(t *testing.T) {
		repo := t.TempDir()
		if err := os.MkdirAll(filepath.Join(repo, "styx"), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(repo, "styx", "guidance.md"), []byte("REPO RULES"), 0o644)
		got, _ := Load(repo)
		if !strings.Contains(got, "REPO RULES") || !strings.Contains(got, "## Project guidance") {
			t.Fatalf("override not appended:\n%s", got)
		}
	})
}

func TestSeedV3UpgradesToCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := guidanceFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(seedV3), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != Seed {
		t.Fatal("an unmodified v3 seed must upgrade to the current Seed")
	}
	for _, want := range []string{"background: true", "collect", "rate_dispatch"} {
		if !strings.Contains(Seed, want) {
			t.Fatalf("current Seed must teach %q", want)
		}
	}
}

func TestSeedV4UpgradesToCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := guidanceFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(seedV4), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != Seed {
		t.Fatal("an unmodified v4 seed must upgrade to the current Seed")
	}
	// seedV4 predates the route-gate; the current Seed must explain that the
	// gated tools are denied by design so a denial doesn't read as a bug.
	if seedV4 == Seed {
		t.Fatal("seedV4 must differ from the current Seed (it predates the Gated tools section)")
	}
	if !strings.Contains(Seed, "Gated tools") {
		t.Fatal("current Seed must teach the Gated tools policy")
	}
}

func TestSeedV5UpgradesToCurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := guidanceFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(seedV5), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != Seed {
		t.Fatal("an unmodified v5 seed must upgrade to the current Seed")
	}
	for _, want := range []string{"user-preference", "retrospective", "styx learn"} {
		if !strings.Contains(Seed, want) {
			t.Fatalf("current Seed must teach %q", want)
		}
	}
}
