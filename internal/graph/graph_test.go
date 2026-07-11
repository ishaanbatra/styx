package graph

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ishaanbatra/styx/internal/config"
)

// newTestRepo creates a git repo with one commit and returns a Project for it.
func newTestRepo(t *testing.T) config.Project {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	return config.Project{ID: "abc123def456", Name: "testrepo", Path: dir}
}

// commitChange adds a new commit so HEAD moves.
func commitChange(t *testing.T, proj config.Project) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(proj.Path, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "change"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = proj.Path
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// writeGraphArtifact simulates a completed graphify run in the repo.
func writeGraphArtifact(t *testing.T, proj config.Project) {
	t.Helper()
	dir := filepath.Join(proj.Path, "graphify-out")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graph.json"),
		[]byte(`{"nodes":[{"id":"a"}],"edges":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := newTestRepo(t)
	m := &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: gitHead(proj.Path)}
	if err := SaveMeta(proj, m); err != nil {
		t.Fatalf("SaveMeta: %v", err)
	}
	got, err := LoadMeta(proj)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if got.GitHead != m.GitHead || got.SchemaVersion != SchemaVersion {
		t.Errorf("round trip mismatch: got %+v want %+v", got, m)
	}
}

func TestIsStale(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, proj config.Project)
		stale bool
	}{
		{"no meta yet", func(t *testing.T, proj config.Project) {}, true},
		{"fresh build", func(t *testing.T, proj config.Project) {
			writeGraphArtifact(t, proj)
			m := &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: gitHead(proj.Path)}
			if err := SaveMeta(proj, m); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"HEAD moved since build", func(t *testing.T, proj config.Project) {
			writeGraphArtifact(t, proj)
			m := &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: gitHead(proj.Path)}
			if err := SaveMeta(proj, m); err != nil {
				t.Fatal(err)
			}
			commitChange(t, proj)
		}, true},
		{"meta fresh but graph.json deleted", func(t *testing.T, proj config.Project) {
			m := &Meta{SchemaVersion: SchemaVersion, BuiltAt: time.Now().UTC(), GitHead: gitHead(proj.Path)}
			if err := SaveMeta(proj, m); err != nil {
				t.Fatal(err)
			}
			// no writeGraphArtifact — graph.json absent
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			proj := newTestRepo(t)
			tt.setup(t, proj)
			stale, reason := IsStale(proj)
			if stale != tt.stale {
				t.Errorf("IsStale = %v (%q), want %v", stale, reason, tt.stale)
			}
			if stale && reason == "" {
				t.Error("stale result must carry a reason")
			}
		})
	}
}

func TestIsStale_EmptyProjectID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := newTestRepo(t)
	proj.ID = ""
	stale, _ := IsStale(proj)
	if stale {
		t.Error("empty-ID (unregistered) project must never be reported stale")
	}
}

// fakeGraphify writes a scripted stand-in for the graphify CLI and returns its
// path. The script emulates `graphify update .`: writes graphify-out/graph.json
// in the cwd. Behavior variants via mode: "ok", "fail" (exit 1), "badjson".
func fakeGraphify(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	var body string
	switch mode {
	case "ok":
		body = "#!/bin/sh\n[ \"$1\" = \"update\" ] && [ \"$2\" = \".\" ] || { echo \"unexpected argv: $@\" >&2; exit 2; }\nmkdir -p graphify-out\nprintf '{\"nodes\":[{\"id\":\"a\"}],\"edges\":[]}' > graphify-out/graph.json\n"
	case "fail":
		body = "#!/bin/sh\n[ \"$1\" = \"update\" ] && [ \"$2\" = \".\" ] || { echo \"unexpected argv: $@\" >&2; exit 2; }\necho boom >&2\nexit 1\n"
	case "badjson":
		body = "#!/bin/sh\n[ \"$1\" = \"update\" ] && [ \"$2\" = \".\" ] || { echo \"unexpected argv: $@\" >&2; exit 2; }\nmkdir -p graphify-out\nprintf 'not json' > graphify-out/graph.json\n"
	default:
		t.Fatalf("unknown mode %q", mode)
	}
	p := filepath.Join(dir, "graphify")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBuild(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{"success writes meta", "ok", false},
		{"nonzero exit surfaces error", "fail", true},
		{"unparseable graph.json surfaces error", "badjson", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			proj := newTestRepo(t)
			bin := fakeGraphify(t, tt.mode)
			err := Build(context.Background(), proj, bin)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Build err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if _, lerr := LoadMeta(proj); lerr == nil {
					t.Error("failed build must not write meta")
				}
				return
			}
			m, lerr := LoadMeta(proj)
			if lerr != nil {
				t.Fatalf("LoadMeta after build: %v", lerr)
			}
			if m.GitHead != gitHead(proj.Path) {
				t.Error("meta.GitHead must record the built HEAD")
			}
			if stale, reason := IsStale(proj); stale {
				t.Errorf("freshly built project reported stale: %s", reason)
			}
		})
	}
}

func TestBuild_LockBlocksConcurrent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := newTestRepo(t)
	d, err := StateDir(proj)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate an in-flight build: fresh lock file.
	if err := os.WriteFile(filepath.Join(d, "build.lock"), []byte("pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = Build(context.Background(), proj, fakeGraphify(t, "ok"))
	if !errors.Is(err, ErrBuildInProgress) {
		t.Fatalf("want ErrBuildInProgress, got %v", err)
	}
	// An expired lock (older than BuildTimeout) is reclaimed.
	old := time.Now().Add(-BuildTimeout - time.Minute)
	if err := os.Chtimes(filepath.Join(d, "build.lock"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := Build(context.Background(), proj, fakeGraphify(t, "ok")); err != nil {
		t.Fatalf("expired lock should be reclaimed, got %v", err)
	}
}

func TestAvailable_EnvOff(t *testing.T) {
	t.Setenv("STYX_GRAPHIFY", "off")
	if _, ok := Available(); ok {
		t.Error("STYX_GRAPHIFY=off must disable the feature")
	}
}
