# Styx v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the Hoot-scoped bash `~/bin/styx` (387 lines) with a Go binary that orchestrates Claude, Codex, Gemini-CLI, and Ollama across any git repo, picking models via an editable rules table with budget-aware fallback.

**Architecture:** Go CLI with a thin `cmd/styx` verb dispatcher delegating to an `internal/router` that consults `~/.config/styx/routing.toml`; channels (Claude/Codex/Gemini/Ollama) each implement a shared `Channel` interface so they're interchangeable; SQLite usage log feeds the router's budget-aware degradation logic; projects are auto-registered on first use via pwd-walk for `.git`.

**Tech Stack:** Go 1.22+, `github.com/BurntSushi/toml` (config), `modernc.org/sqlite` (pure-Go SQLite, no cgo), `github.com/google/go-cmp` (test diffs), stdlib `testing` + stdlib `net/http`. macOS Keychain via `security` CLI.

**Spec:** `docs/superpowers/specs/2026-05-26-styx-v2-design.md`

---

## File Structure

Each file has one clear responsibility. Files that change together live together.

```
github.com/ishaanbatra/styx/
├── go.mod, go.sum                       # module + deps
├── Makefile                             # build, test, install
├── README.md                            # quick reference
├── install.sh                           # standalone installer (mv old, drop new)
│
├── cmd/styx/                            # one file per verb; pure dispatchers
│   ├── main.go                          # arg routing + dispatch
│   ├── help.go                          # help text
│   ├── grunt.go, think.go               # simple ollama-pass verbs
│   ├── explain.go, summarize.go, critique.go
│   ├── research.go                      # compound: research + research.critic
│   ├── deep_research.go                 # browser + template
│   ├── plan.go, build.go, review.go     # claude-heavy verbs
│   ├── check.go                         # dashboard
│   ├── budget.go                        # `styx budget` summary
│   ├── route.go                         # `styx route --explain`
│   ├── project.go                       # `styx project {ls,add,rm,rename}`
│   └── migrate_secrets.go               # one-time secrets migration
│
├── internal/paths/                      # XDG path resolution
│   ├── paths.go, paths_test.go
│
├── internal/config/                     # TOML loaders + Keychain wrapper
│   ├── routing.go, routing_test.go
│   ├── projects.go, projects_test.go
│   ├── secrets.go, secrets_test.go
│
├── internal/budget/                     # SQLite usage log + state
│   ├── budget.go, budget_test.go
│
├── internal/project/                    # discovery + registry
│   ├── project.go, project_test.go
│   ├── sniff.go, sniff_test.go
│
├── internal/signals/                    # signal extraction
│   ├── signals.go, signals_test.go
│
├── internal/channel/                    # interface + 4 impls
│   ├── channel.go                       # interface + types + contract test
│   ├── channel_test.go
│   ├── claude/  claude.go, claude_test.go
│   ├── codex/   codex.go, codex_test.go
│   ├── gemini/  gemini.go, gemini_test.go
│   └── ollama/  ollama.go, ollama_test.go
│
├── internal/brief/                      # brief + plan file I/O
│   ├── brief.go, brief_test.go
│
├── internal/router/                     # rule eval + decision
│   ├── router.go, router_test.go
│
└── testdata/
    └── routing/                         # test fixtures
        └── *.toml
```

**Module path:** `github.com/ishaanbatra/styx` (placeholder; rename `go.mod` if your GitHub username is different — easy global find/replace).

---

## Phase 1 — Foundation (Tasks 1–3)

### Task 1: Install Go, initialize module, Makefile, README

**Files:**
- Create: `/Users/ishaanbatra/Documents/GitHub/styx/go.mod`
- Create: `/Users/ishaanbatra/Documents/GitHub/styx/Makefile`
- Create: `/Users/ishaanbatra/Documents/GitHub/styx/README.md`
- Create: `/Users/ishaanbatra/Documents/GitHub/styx/.gitignore`

- [ ] **Step 1: Install Go**

Run:
```bash
brew install go
go version
```
Expected: `go version go1.22.x darwin/arm64` (or newer)

- [ ] **Step 2: Initialize Go module**

Run from `/Users/ishaanbatra/Documents/GitHub/styx`:
```bash
go mod init github.com/ishaanbatra/styx
```
Expected: creates `go.mod` with `module github.com/ishaanbatra/styx` and `go 1.22`.

- [ ] **Step 3: Write Makefile**

Create `Makefile`:
```makefile
.PHONY: build test install clean fmt vet

BIN_DIR := $(HOME)/bin
BIN     := styx

build:
	go build -o ./bin/$(BIN) ./cmd/styx

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

install: build
	@if [ -f $(BIN_DIR)/$(BIN) ] && [ ! -L $(BIN_DIR)/$(BIN) ]; then \
		mv $(BIN_DIR)/$(BIN) $(BIN_DIR)/$(BIN).old.bak; \
		echo "Backed up existing $(BIN) to $(BIN_DIR)/$(BIN).old.bak"; \
	fi
	mkdir -p $(BIN_DIR)
	cp ./bin/$(BIN) $(BIN_DIR)/$(BIN)
	@echo "Installed to $(BIN_DIR)/$(BIN)"

clean:
	rm -rf ./bin
```

- [ ] **Step 4: Write README**

Create `README.md`:
```markdown
# Styx

Personal multi-model dev orchestration CLI. Routes work between Claude, Codex,
Gemini-CLI, and Ollama based on a hand-curated rules table.

See `docs/superpowers/specs/2026-05-26-styx-v2-design.md` for design.

## Build

    make build       # produces ./bin/styx
    make test        # runs all tests
    make install     # installs to ~/bin/styx (backs up any existing one)
```

- [ ] **Step 5: Write .gitignore**

Create `.gitignore`:
```
/bin/
*.test
*.out
.DS_Store
```

- [ ] **Step 6: Commit**

```bash
cd /Users/ishaanbatra/Documents/GitHub/styx
git add go.mod Makefile README.md .gitignore
git commit -m "chore: initialize Go module + Makefile + README"
```

---

### Task 2: Add dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add TOML parser**

Run:
```bash
cd /Users/ishaanbatra/Documents/GitHub/styx
go get github.com/BurntSushi/toml@v1.3.2
```
Expected: `go.sum` written; `go.mod` lists `github.com/BurntSushi/toml v1.3.2`.

- [ ] **Step 2: Add pure-Go SQLite driver**

Run:
```bash
go get modernc.org/sqlite@v1.28.0
```
Expected: `go.mod` lists `modernc.org/sqlite v1.28.0`. (Pure-Go = no cgo, easier install.)

- [ ] **Step 3: Add go-cmp for test diffs**

Run:
```bash
go get github.com/google/go-cmp@v0.6.0
```

- [ ] **Step 4: Verify with go mod tidy**

Run:
```bash
go mod tidy
go build ./...
```
Expected: no errors; an empty `./...` build succeeds.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add toml, sqlite, and go-cmp dependencies"
```

---

### Task 3: Lay down empty package skeleton

**Files:**
- Create: 18 empty Go files representing the package skeleton (see code blocks)

- [ ] **Step 1: Create internal package directories with placeholder files**

Run:
```bash
cd /Users/ishaanbatra/Documents/GitHub/styx
mkdir -p cmd/styx internal/{paths,config,budget,project,signals,channel/{claude,codex,gemini,ollama},brief,router} testdata/routing
```

- [ ] **Step 2: Write minimal `package` declarations so `go build ./...` passes**

Create `cmd/styx/main.go`:
```go
package main

func main() {}
```

Create `internal/paths/paths.go`:
```go
package paths
```

Create `internal/config/routing.go`:
```go
package config
```

Create `internal/config/projects.go`:
```go
package config
```

Create `internal/config/secrets.go`:
```go
package config
```

Create `internal/budget/budget.go`:
```go
package budget
```

Create `internal/project/project.go`:
```go
package project
```

Create `internal/project/sniff.go`:
```go
package project
```

Create `internal/signals/signals.go`:
```go
package signals
```

Create `internal/channel/channel.go`:
```go
package channel
```

Create `internal/channel/claude/claude.go`:
```go
package claude
```

Create `internal/channel/codex/codex.go`:
```go
package codex
```

Create `internal/channel/gemini/gemini.go`:
```go
package gemini
```

Create `internal/channel/ollama/ollama.go`:
```go
package ollama
```

Create `internal/brief/brief.go`:
```go
package brief
```

Create `internal/router/router.go`:
```go
package router
```

- [ ] **Step 3: Verify whole-tree build succeeds**

Run:
```bash
go build ./...
go vet ./...
```
Expected: no output, exit code 0.

- [ ] **Step 4: Commit**

```bash
git add cmd internal testdata
git commit -m "chore: scaffold package skeleton"
```

---

## Phase 2 — Paths + Secrets (Tasks 4–5)

### Task 4: XDG path resolution helper

**Files:**
- Create: `internal/paths/paths.go`
- Create: `internal/paths/paths_test.go`

- [ ] **Step 1: Write failing test**

Create `internal/paths/paths_test.go`:
```go
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
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/paths/...`
Expected: build failure (`ConfigDir`, `RoutingPath`, `UsageDBPath` undefined).

- [ ] **Step 3: Implement paths.go**

Overwrite `internal/paths/paths.go`:
```go
// Package paths resolves Styx's on-disk locations following the XDG Base
// Directory Specification with sensible macOS fallbacks.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "styx"

// ConfigDir returns ~/.config/styx (or $XDG_CONFIG_HOME/styx if set).
func ConfigDir() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".config", appName), nil
}

// StateDir returns the directory for app state (sqlite, indexes).
func StateDir() (string, error) {
	c, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(c, "state"), nil
}

// CacheDir returns ~/.cache/styx (or $XDG_CACHE_HOME/styx if set).
func CacheDir() (string, error) {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".cache", appName), nil
}

// LogDir returns the directory for log files.
func LogDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, appName, "logs"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".local", "share", appName, "logs"), nil
}

// RoutingPath returns the absolute path to routing.toml.
func RoutingPath() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "routing.toml"), nil
}

// ProjectsPath returns the absolute path to projects.toml.
func ProjectsPath() (string, error) {
	d, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "projects.toml"), nil
}

// UsageDBPath returns the absolute path to the sqlite usage log.
func UsageDBPath() (string, error) {
	d, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "usage.db"), nil
}

// EnsureDir creates dir (and parents) with 0755.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}
```

- [ ] **Step 4: Run test, verify it passes**

Run: `go test ./internal/paths/... -v`
Expected: 4 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paths/
git commit -m "feat(paths): XDG-aware config/state/cache resolution"
```

---

### Task 5: Keychain wrapper for secrets

**Files:**
- Create: `internal/config/secrets_test.go`
- Modify: `internal/config/secrets.go`

- [ ] **Step 1: Write failing test**

Create `internal/config/secrets_test.go`:
```go
package config

import (
	"strings"
	"testing"
)

func TestSecretName_Validation(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"gemini_api_key", false},
		{"openai-token", false},
		{"a", false},
		{"", true},
		{"has spaces", true},
		{"has;semicolon", true},
		{strings.Repeat("a", 256), true},
	}
	for _, c := range cases {
		err := validateSecretName(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("validateSecretName(%q): got err=%v, wantErr=%v", c.in, err, c.wantErr)
		}
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/config/... -run TestSecretName`
Expected: build failure (`validateSecretName` undefined).

- [ ] **Step 3: Implement secrets.go**

Overwrite `internal/config/secrets.go`:
```go
package config

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// keychainService is the macOS Keychain "service" name for all Styx secrets.
const keychainService = "styx"

var secretNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validateSecretName(name string) error {
	if name == "" {
		return errors.New("secret name is empty")
	}
	if !secretNameRE.MatchString(name) {
		return fmt.Errorf("invalid secret name %q (allowed: A-Z a-z 0-9 _ -, length 1-128)", name)
	}
	return nil
}

// Secret reads a secret from the macOS Keychain.
// Returns ("", ErrSecretNotFound) if the secret is not stored.
func Secret(name string) (string, error) {
	if err := validateSecretName(name); err != nil {
		return "", err
	}
	out, err := exec.Command("security", "find-generic-password",
		"-s", keychainService, "-a", name, "-w").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return "", ErrSecretNotFound
		}
		return "", fmt.Errorf("read keychain secret %q: %w", name, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// SetSecret writes a secret to the macOS Keychain (creates or updates).
func SetSecret(name, value string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	cmd := exec.Command("security", "add-generic-password",
		"-U", "-s", keychainService, "-a", name, "-w", value)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write keychain secret %q: %w (output: %s)", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteSecret removes a secret from the Keychain. Returns nil if not present.
func DeleteSecret(name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	cmd := exec.Command("security", "delete-generic-password",
		"-s", keychainService, "-a", name)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return nil
		}
		return fmt.Errorf("delete keychain secret %q: %w", name, err)
	}
	return nil
}

// ErrSecretNotFound is returned when a Keychain item does not exist.
var ErrSecretNotFound = errors.New("secret not found in Keychain")
```

- [ ] **Step 4: Run test, verify it passes**

Run: `go test ./internal/config/... -run TestSecretName -v`
Expected: PASS.

- [ ] **Step 5: Smoke test against real Keychain (manual)**

Run from a terminal:
```bash
security add-generic-password -U -s styx -a smoke_test -w hello
go run -exec '' ./cmd/styx 2>/dev/null || true   # ensure styx builds
cat <<'EOF' > /tmp/styx_secret_smoke.go
package main

import (
	"fmt"
	"github.com/ishaanbatra/styx/internal/config"
)

func main() {
	v, err := config.Secret("smoke_test")
	fmt.Println(v, err)
}
EOF
go run /tmp/styx_secret_smoke.go
security delete-generic-password -s styx -a smoke_test
rm /tmp/styx_secret_smoke.go
```
Expected: prints `hello <nil>`.

- [ ] **Step 6: Commit**

```bash
git add internal/config/secrets.go internal/config/secrets_test.go
git commit -m "feat(config): macOS Keychain wrapper with name validation"
```

---

## Phase 3 — Config loaders (Tasks 6–7)

### Task 6: Routing TOML loader

**Files:**
- Create: `internal/config/routing_test.go`
- Modify: `internal/config/routing.go`
- Create: `testdata/routing/basic.toml`

- [ ] **Step 1: Write test fixture**

Create `testdata/routing/basic.toml`:
```toml
[budget]
claude.cap_pct = 80
codex.cap_pct = 75

[[rule]]
verb = "plan"
signals = ["complex"]
use = "claude:opus-4-7"
fallback = ["claude:sonnet-4-6"]

[[rule]]
verb = "plan"
use = "claude:sonnet-4-6"
fallback = ["codex:gpt-5", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "review"
parallel = ["claude:sonnet-4-6", "codex:gpt-5"]
synthesize_with = "claude:sonnet-4-6"
```

- [ ] **Step 2: Write failing test**

Create `internal/config/routing_test.go`:
```go
package config

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLoadRoutingFile(t *testing.T) {
	got, err := LoadRoutingFile("../../testdata/routing/basic.toml")
	if err != nil {
		t.Fatal(err)
	}
	want := Routing{
		Budget: BudgetCaps{
			Claude: ChannelCap{CapPct: 80},
			Codex:  ChannelCap{CapPct: 75},
		},
		Rules: []Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus-4-7", Fallback: []string{"claude:sonnet-4-6"}},
			{Verb: "plan", Use: "claude:sonnet-4-6", Fallback: []string{"codex:gpt-5", "ollama:qwen2.5-coder:14b"}},
			{Verb: "review", Parallel: []string{"claude:sonnet-4-6", "codex:gpt-5"}, SynthesizeWith: "claude:sonnet-4-6"},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

func TestLoadRoutingFile_Missing(t *testing.T) {
	_, err := LoadRoutingFile("/nonexistent/path.toml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}
```

- [ ] **Step 3: Run test, verify it fails**

Run: `go test ./internal/config/... -run TestLoadRoutingFile`
Expected: build failure (`Routing`, `BudgetCaps`, `ChannelCap`, `Rule`, `LoadRoutingFile` undefined).

- [ ] **Step 4: Implement routing.go**

Overwrite `internal/config/routing.go`:
```go
package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Routing is the parsed routing.toml.
type Routing struct {
	Budget BudgetCaps `toml:"budget"`
	Rules  []Rule     `toml:"rule"`
}

// BudgetCaps holds the per-channel cap percentages.
type BudgetCaps struct {
	Claude     ChannelCap `toml:"claude"`
	Codex      ChannelCap `toml:"codex"`
	GeminiFree ChannelCap `toml:"gemini_free"`
	GeminiPaid ChannelCap `toml:"gemini_paid"`
}

// ChannelCap is the maximum percentage of a channel's budget to use before degrading.
type ChannelCap struct {
	CapPct float64 `toml:"cap_pct"`
}

// Rule is a single routing rule. First match wins.
//
// Either Use (single channel) OR Parallel+SynthesizeWith (multi-channel review pattern) must be set.
type Rule struct {
	Verb           string   `toml:"verb"`
	Signals        []string `toml:"signals"`
	Use            string   `toml:"use"`             // "channel:model" for single-channel rules
	Parallel       []string `toml:"parallel"`        // for parallel review-style verbs
	SynthesizeWith string   `toml:"synthesize_with"` // channel that merges parallel outputs
	Fallback       []string `toml:"fallback"`        // ordered fallback chain
}

// LoadRouting loads routing.toml from the default config path.
func LoadRouting() (Routing, error) {
	p, err := paths.RoutingPath()
	if err != nil {
		return Routing{}, err
	}
	return LoadRoutingFile(p)
}

// LoadRoutingFile loads routing config from an explicit path.
func LoadRoutingFile(path string) (Routing, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Routing{}, fmt.Errorf("read routing config %s: %w", path, err)
	}
	var r Routing
	if err := toml.Unmarshal(b, &r); err != nil {
		return Routing{}, fmt.Errorf("parse routing config %s: %w", path, err)
	}
	return r, nil
}
```

- [ ] **Step 5: Run test, verify it passes**

Run: `go test ./internal/config/... -run TestLoadRoutingFile -v`
Expected: 2 tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/routing.go internal/config/routing_test.go testdata/routing/basic.toml
git commit -m "feat(config): parse routing.toml into typed Rules + BudgetCaps"
```

---

### Task 7: Projects TOML loader with atomic save

**Files:**
- Create: `internal/config/projects_test.go`
- Modify: `internal/config/projects.go`

- [ ] **Step 1: Write failing test**

Create `internal/config/projects_test.go`:
```go
package config

import (
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestSaveAndLoadProjects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	want := []Project{
		{
			Name: "hoot-backend",
			Path: "/Users/x/Documents/GitHub/ai-ta-backend",
			Language: "python",
			ResearchDir: "docs/research",
			PlansDir: "docs/plans",
			DefaultVerbs: []string{"plan", "build", "review"},
		},
		{
			Name: "voiceresumebot",
			Path: "/Users/x/Documents/GitHub/VoiceResumeBot",
			Language: "python",
		},
	}
	if err := SaveProjects(want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProjects()
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}

	// Verify the file actually exists at the expected path.
	if _, err := filepath.Glob(filepath.Join(dir, "styx", "projects.toml")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadProjects_MissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := LoadProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %d projects", len(got))
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/config/... -run TestSaveAndLoadProjects`
Expected: build failure (`Project`, `SaveProjects`, `LoadProjects` undefined).

- [ ] **Step 3: Implement projects.go**

Overwrite `internal/config/projects.go`:
```go
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Project is one registered code project.
type Project struct {
	Name         string   `toml:"name"`
	Path         string   `toml:"path"`
	Language     string   `toml:"language"`
	ResearchDir  string   `toml:"research_dir,omitempty"`
	PlansDir     string   `toml:"plans_dir,omitempty"`
	DefaultVerbs []string `toml:"default_verbs,omitempty"`
}

type projectsFile struct {
	Project []Project `toml:"project"`
}

// LoadProjects loads the projects.toml registry. Missing file is not an error
// (returns empty slice) so first-run auto-registration can proceed.
func LoadProjects() ([]Project, error) {
	p, err := paths.ProjectsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Project{}, nil
		}
		return nil, fmt.Errorf("read projects.toml: %w", err)
	}
	var pf projectsFile
	if err := toml.Unmarshal(b, &pf); err != nil {
		return nil, fmt.Errorf("parse projects.toml: %w", err)
	}
	return pf.Project, nil
}

// SaveProjects writes projects.toml atomically (tmpfile + rename).
func SaveProjects(projs []Project) error {
	target, err := paths.ProjectsPath()
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(filepath.Dir(target)); err != nil {
		return fmt.Errorf("ensure config dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "projects-*.toml.tmp")
	if err != nil {
		return fmt.Errorf("create tmp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	enc := toml.NewEncoder(tmp)
	if err := enc.Encode(projectsFile{Project: projs}); err != nil {
		tmp.Close()
		return fmt.Errorf("encode projects.toml: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp file: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("rename tmp to target: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test, verify it passes**

Run: `go test ./internal/config/... -run TestSaveAndLoadProjects -v`
Run: `go test ./internal/config/... -run TestLoadProjects_MissingFileReturnsEmpty -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/projects.go internal/config/projects_test.go
git commit -m "feat(config): atomic save/load for projects.toml registry"
```

---

## Phase 4 — Budget tracker (Tasks 8–9)

### Task 8: SQLite usage log

**Files:**
- Create: `internal/budget/budget_test.go`
- Modify: `internal/budget/budget.go`

- [ ] **Step 1: Write failing test**

Create `internal/budget/budget_test.go`:
```go
package budget

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func newTestTracker(t *testing.T) *Tracker {
	t.Helper()
	dir := t.TempDir()
	tr, err := New(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func TestRecord_AppendsRow(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 100, TokensOut: 200, Success: true}); err != nil {
		t.Fatal(err)
	}
	if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 50, TokensOut: 30, Success: true}); err != nil {
		t.Fatal(err)
	}
	got, err := tr.totalTokens(ctx, "claude", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if want := 380; got != want {
		t.Errorf("totalTokens: got %d, want %d", got, want)
	}
}

func TestState_UsedPctReflectsCap(t *testing.T) {
	tr := newTestTracker(t)
	tr.SetCap("claude", 100_000) // 100k tokens for the window
	ctx := context.Background()
	if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", TokensIn: 30_000, TokensOut: 20_000, Success: true}); err != nil {
		t.Fatal(err)
	}
	st, err := tr.State(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if st.UsedPct < 49 || st.UsedPct > 51 {
		t.Errorf("UsedPct = %.2f, want ~50", st.UsedPct)
	}
}

func TestState_UnknownChannelHasZeroUsage(t *testing.T) {
	tr := newTestTracker(t)
	st, err := tr.State(context.Background(), "ollama")
	if err != nil {
		t.Fatal(err)
	}
	if st.UsedPct != 0 {
		t.Errorf("UsedPct for unrecorded channel: got %.2f, want 0", st.UsedPct)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/budget/...`
Expected: build failure (`Tracker`, `New`, `Event`, `Record`, `State`, `SetCap` undefined).

- [ ] **Step 3: Implement budget.go**

Overwrite `internal/budget/budget.go`:
```go
// Package budget tracks per-channel usage via an append-only SQLite log
// and computes used-percentage against configured caps.
package budget

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ishaanbatra/styx/internal/paths"
)

// Tracker is the budget API. Methods are safe for concurrent use.
type Tracker struct {
	db   *sql.DB
	mu   sync.RWMutex
	caps map[string]int // channel name -> token cap per window
	wind map[string]time.Duration
}

// Event is a single usage record.
type Event struct {
	Channel   string
	Verb      string
	TokensIn  int
	TokensOut int
	Success   bool
	ErrorKind string // "", "timeout", "429", "5xx", "other"
}

// State is the current spend posture for a channel.
type State struct {
	Channel       string
	Window        time.Duration
	UsedPct       float64
	LimitHit      bool
	CooldownUntil time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS usage (
    ts          INTEGER NOT NULL,
    channel     TEXT    NOT NULL,
    verb        TEXT    NOT NULL,
    tokens_in   INTEGER NOT NULL,
    tokens_out  INTEGER NOT NULL,
    success     INTEGER NOT NULL,
    error_kind  TEXT
);
CREATE INDEX IF NOT EXISTS usage_channel_ts ON usage (channel, ts DESC);
CREATE TABLE IF NOT EXISTS cooldown (
    channel TEXT PRIMARY KEY,
    until   INTEGER NOT NULL
);
`

// Default returns a Tracker opened at the standard usage.db path.
func Default() (*Tracker, error) {
	p, err := paths.UsageDBPath()
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(filepath.Dir(p)); err != nil {
		return nil, err
	}
	return New(p)
}

// New opens (and migrates) the sqlite database at path.
func New(path string) (*Tracker, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Tracker{
		db:   db,
		caps: map[string]int{},
		wind: map[string]time.Duration{
			"claude":      30 * 24 * time.Hour,
			"codex":       30 * 24 * time.Hour,
			"gemini_paid": 30 * 24 * time.Hour,
			"gemini_free": 24 * time.Hour,
			"ollama":      24 * time.Hour, // unlimited but bounded for reporting
		},
	}, nil
}

// Close releases the underlying database handle.
func (t *Tracker) Close() error { return t.db.Close() }

// SetCap configures the token cap for a channel within its window.
// caps default to 0 (no cap) until SetCap is called.
func (t *Tracker) SetCap(channel string, tokens int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.caps[channel] = tokens
}

// Window returns the rolling window over which usage is summed for `channel`.
func (t *Tracker) Window(channel string) time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if w, ok := t.wind[channel]; ok {
		return w
	}
	return 30 * 24 * time.Hour
}

// Record appends an event.
func (t *Tracker) Record(ctx context.Context, e Event) error {
	successInt := 0
	if e.Success {
		successInt = 1
	}
	_, err := t.db.ExecContext(ctx,
		`INSERT INTO usage (ts, channel, verb, tokens_in, tokens_out, success, error_kind) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), e.Channel, e.Verb, e.TokensIn, e.TokensOut, successInt, e.ErrorKind)
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

// State computes UsedPct + cooldown for a channel.
func (t *Tracker) State(ctx context.Context, channel string) (State, error) {
	st := State{Channel: channel, Window: t.Window(channel)}
	total, err := t.totalTokens(ctx, channel, st.Window)
	if err != nil {
		return State{}, err
	}
	t.mu.RLock()
	cap := t.caps[channel]
	t.mu.RUnlock()
	if cap > 0 {
		st.UsedPct = float64(total) / float64(cap) * 100
		if st.UsedPct >= 100 {
			st.LimitHit = true
		}
	}
	cd, err := t.cooldownUntil(ctx, channel)
	if err != nil {
		return State{}, err
	}
	st.CooldownUntil = cd
	return st, nil
}

func (t *Tracker) totalTokens(ctx context.Context, channel string, window time.Duration) (int, error) {
	cutoff := time.Now().Add(-window).Unix()
	var total sql.NullInt64
	row := t.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(tokens_in + tokens_out), 0) FROM usage WHERE channel = ? AND ts >= ?`,
		channel, cutoff)
	if err := row.Scan(&total); err != nil {
		return 0, fmt.Errorf("sum tokens for %s: %w", channel, err)
	}
	return int(total.Int64), nil
}

func (t *Tracker) cooldownUntil(ctx context.Context, channel string) (time.Time, error) {
	row := t.db.QueryRowContext(ctx, `SELECT until FROM cooldown WHERE channel = ?`, channel)
	var until int64
	switch err := row.Scan(&until); err {
	case nil:
		ts := time.Unix(until, 0)
		if time.Now().After(ts) {
			return time.Time{}, nil
		}
		return ts, nil
	case sql.ErrNoRows:
		return time.Time{}, nil
	default:
		return time.Time{}, fmt.Errorf("read cooldown for %s: %w", channel, err)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/budget/... -v`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/budget/
git commit -m "feat(budget): sqlite usage log + State with cap-based UsedPct"
```

---

### Task 9: Cooldown and circuit breaker

**Files:**
- Modify: `internal/budget/budget.go`
- Modify: `internal/budget/budget_test.go`

- [ ] **Step 1: Write failing tests (append to existing file)**

Append to `internal/budget/budget_test.go`:
```go
func TestMarkCooldown_ReflectsInState(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	until := time.Now().Add(15 * time.Minute)
	if err := tr.MarkCooldown(ctx, "codex", until); err != nil {
		t.Fatal(err)
	}
	st, err := tr.State(ctx, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if st.CooldownUntil.IsZero() {
		t.Error("CooldownUntil zero after MarkCooldown")
	}
	if d := st.CooldownUntil.Sub(until); d > time.Second || d < -time.Second {
		t.Errorf("CooldownUntil drift: %v (want within 1s of %v)", st.CooldownUntil, until)
	}
}

func TestRecentErrors_TriggersCircuit(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = tr.Record(ctx, Event{Channel: "gemini", Verb: "research", Success: false, ErrorKind: "5xx"})
	}
	tripped, err := tr.ShouldCircuitBreak(ctx, "gemini", 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !tripped {
		t.Error("circuit should trip after 5 errors in 60s")
	}
}

func TestRecentErrors_DoesNotTripBelowThreshold(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_ = tr.Record(ctx, Event{Channel: "gemini", Verb: "research", Success: false, ErrorKind: "5xx"})
	}
	tripped, err := tr.ShouldCircuitBreak(ctx, "gemini", 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if tripped {
		t.Error("circuit should not trip with only 3 errors")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/budget/... -run TestMarkCooldown`
Expected: build failure (`MarkCooldown`, `ShouldCircuitBreak` undefined).

- [ ] **Step 3: Add MarkCooldown and ShouldCircuitBreak to budget.go**

Append to `internal/budget/budget.go`:
```go
// MarkCooldown sets a cooldown deadline for a channel. Subsequent State()
// calls report this until the time passes.
func (t *Tracker) MarkCooldown(ctx context.Context, channel string, until time.Time) error {
	_, err := t.db.ExecContext(ctx,
		`INSERT INTO cooldown (channel, until) VALUES (?, ?)
		 ON CONFLICT(channel) DO UPDATE SET until = excluded.until`,
		channel, until.Unix())
	if err != nil {
		return fmt.Errorf("mark cooldown for %s: %w", channel, err)
	}
	return nil
}

// ShouldCircuitBreak returns true if `channel` has had >= `threshold`
// failures within the last `window`. The router uses this to short-circuit
// thrashing on a broken channel.
func (t *Tracker) ShouldCircuitBreak(ctx context.Context, channel string, threshold int, window time.Duration) (bool, error) {
	cutoff := time.Now().Add(-window).Unix()
	row := t.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage WHERE channel = ? AND ts >= ? AND success = 0`,
		channel, cutoff)
	var n int
	if err := row.Scan(&n); err != nil {
		return false, fmt.Errorf("count failures for %s: %w", channel, err)
	}
	return n >= threshold, nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/budget/... -v`
Expected: 5 tests PASS (the 3 from Task 8 + 2 new ones).

- [ ] **Step 5: Commit**

```bash
git add internal/budget/
git commit -m "feat(budget): cooldown deadlines + circuit-breaker check"
```

---

## Phase 5 — Project discovery (Tasks 10–11)

### Task 10: Language sniffer

**Files:**
- Create: `internal/project/sniff_test.go`
- Modify: `internal/project/sniff.go`

- [ ] **Step 1: Write failing test**

Create `internal/project/sniff_test.go`:
```go
package project

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSniffLanguage(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
	}{
		{"python-pyproject", []string{"pyproject.toml"}, "python"},
		{"python-setup-py", []string{"setup.py"}, "python"},
		{"typescript-package", []string{"package.json", "tsconfig.json"}, "typescript"},
		{"javascript-only-package", []string{"package.json"}, "javascript"},
		{"go-mod", []string{"go.mod"}, "go"},
		{"rust-cargo", []string{"Cargo.toml"}, "rust"},
		{"mixed-py-ts", []string{"pyproject.toml", "package.json"}, "mixed"},
		{"unknown-empty", []string{}, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, c.files...)
			got := SniffLanguage(dir)
			if got != c.want {
				t.Errorf("SniffLanguage(%v) = %q, want %q", c.files, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/project/... -run TestSniffLanguage`
Expected: build failure (`SniffLanguage` undefined).

- [ ] **Step 3: Implement sniff.go**

Overwrite `internal/project/sniff.go`:
```go
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
```

- [ ] **Step 4: Run test, verify it passes**

Run: `go test ./internal/project/... -run TestSniffLanguage -v`
Expected: 8 sub-tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/project/sniff.go internal/project/sniff_test.go
git commit -m "feat(project): language sniffer (python/ts/js/go/rust/mixed)"
```

---

### Task 11: Project discovery and registry

**Files:**
- Create: `internal/project/project_test.go`
- Modify: `internal/project/project.go`

- [ ] **Step 1: Write failing tests**

Create `internal/project/project_test.go`:
```go
package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v (%s)", err, out)
	}
}

func TestCurrent_WalksUpToGitRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	gitInit(t, repo)
	writeFiles(t, repo, "pyproject.toml")
	sub := filepath.Join(repo, "src", "deep", "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("HOME", t.TempDir())

	p, err := CurrentFrom(sub)
	if err != nil {
		t.Fatal(err)
	}
	if p.Path != repo {
		t.Errorf("Path = %q, want %q", p.Path, repo)
	}
	if p.Language != "python" {
		t.Errorf("Language = %q, want python", p.Language)
	}
	if p.Name != "repo" {
		t.Errorf("Name = %q, want repo", p.Name)
	}
}

func TestCurrent_AutoRegistersOnFirstUse(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "newproj")
	gitInit(t, repo)
	writeFiles(t, repo, "go.mod")

	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	if _, err := CurrentFrom(repo); err != nil {
		t.Fatal(err)
	}
	// After CurrentFrom, projects.toml should exist with the new entry.
	registry := filepath.Join(cfgDir, "styx", "projects.toml")
	b, err := os.ReadFile(registry)
	if err != nil {
		t.Fatalf("registry not written: %v", err)
	}
	if !contains(string(b), "newproj") {
		t.Errorf("registry missing project name; got:\n%s", string(b))
	}
	if !contains(string(b), "go") {
		t.Errorf("registry missing language tag; got:\n%s", string(b))
	}
}

func TestCurrent_NameCollisionAppendsSuffix(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	repo1 := filepath.Join(root1, "shared-name")
	repo2 := filepath.Join(root2, "shared-name")
	gitInit(t, repo1)
	gitInit(t, repo2)

	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)

	p1, err := CurrentFrom(repo1)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := CurrentFrom(repo2)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Name == p2.Name {
		t.Errorf("expected distinct names, got %q == %q", p1.Name, p2.Name)
	}
	if p2.Name != "shared-name-2" {
		t.Errorf("expected shared-name-2, got %q", p2.Name)
	}
}

func TestResolve_LooksUpByAlias(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	root := t.TempDir()
	repo := filepath.Join(root, "alpha")
	gitInit(t, repo)
	if _, err := CurrentFrom(repo); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != repo {
		t.Errorf("Path = %q, want %q", got.Path, repo)
	}
}

func TestForget(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	root := t.TempDir()
	repo := filepath.Join(root, "willforget")
	gitInit(t, repo)
	if _, err := CurrentFrom(repo); err != nil {
		t.Fatal(err)
	}
	if err := Forget("willforget"); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve("willforget"); err == nil {
		t.Error("expected error resolving forgotten project")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests, verify they fail**

Run: `go test ./internal/project/... -run TestCurrent`
Expected: build failure (`CurrentFrom`, `Resolve`, `Forget`, `Project` undefined).

- [ ] **Step 3: Implement project.go**

Overwrite `internal/project/project.go`:
```go
// Package project discovers and tracks code projects via git roots.
package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
)

// Project is the public record for a registered repo.
type Project = config.Project

// ErrNotInGitRepo is returned when no .git ancestor is found.
var ErrNotInGitRepo = errors.New("not inside a git repository")

// ErrUnknown is returned when an alias is not registered.
var ErrUnknown = errors.New("project not registered")

// Current resolves the project for the current working directory.
func Current() (Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Project{}, fmt.Errorf("getwd: %w", err)
	}
	return CurrentFrom(cwd)
}

// CurrentFrom resolves the project containing dir; auto-registers if new.
func CurrentFrom(dir string) (Project, error) {
	root, err := findGitRoot(dir)
	if err != nil {
		return Project{}, err
	}
	regs, err := config.LoadProjects()
	if err != nil {
		return Project{}, fmt.Errorf("load registry: %w", err)
	}
	for _, p := range regs {
		if p.Path == root {
			return p, nil
		}
	}
	p := autoRegister(root, regs)
	regs = append(regs, p)
	if err := config.SaveProjects(regs); err != nil {
		return Project{}, fmt.Errorf("save registry: %w", err)
	}
	fmt.Fprintf(os.Stderr, "[styx] registered new project: %s (%s) at %s\n", p.Name, p.Language, p.Path)
	return p, nil
}

// Resolve looks up a project by friendly alias.
func Resolve(alias string) (Project, error) {
	regs, err := config.LoadProjects()
	if err != nil {
		return Project{}, err
	}
	for _, p := range regs {
		if p.Name == alias {
			return p, nil
		}
	}
	return Project{}, fmt.Errorf("%w: %q", ErrUnknown, alias)
}

// List returns the full registry.
func List() ([]Project, error) {
	return config.LoadProjects()
}

// Register adds or replaces an entry by Name.
func Register(p Project) error {
	regs, err := config.LoadProjects()
	if err != nil {
		return err
	}
	for i, existing := range regs {
		if existing.Name == p.Name {
			regs[i] = p
			return config.SaveProjects(regs)
		}
	}
	regs = append(regs, p)
	return config.SaveProjects(regs)
}

// Forget removes the entry with name `alias`. No error if absent.
func Forget(alias string) error {
	regs, err := config.LoadProjects()
	if err != nil {
		return err
	}
	out := regs[:0]
	for _, p := range regs {
		if p.Name != alias {
			out = append(out, p)
		}
	}
	return config.SaveProjects(out)
}

func findGitRoot(start string) (string, error) {
	dir := start
	for {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && (fi.IsDir() || !fi.IsDir()) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("%w (searched up from %s)", ErrNotInGitRepo, start)
		}
		dir = parent
	}
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slug(name string) string {
	s := strings.ToLower(name)
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	if s == "" {
		s = "project"
	}
	return s
}

func autoRegister(root string, existing []Project) Project {
	base := slug(filepath.Base(root))
	name := base
	suffix := 2
	for nameTaken(name, existing) {
		name = fmt.Sprintf("%s-%d", base, suffix)
		suffix++
	}
	return Project{
		Name:        name,
		Path:        root,
		Language:    SniffLanguage(root),
		ResearchDir: "styx/research",
		PlansDir:    "styx/plans",
	}
}

func nameTaken(name string, regs []Project) bool {
	for _, p := range regs {
		if p.Name == name {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/project/... -v`
Expected: TestCurrent_WalksUpToGitRoot, TestCurrent_AutoRegistersOnFirstUse, TestCurrent_NameCollisionAppendsSuffix, TestResolve_LooksUpByAlias, TestForget all PASS; plus sniff tests from Task 10 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/project/
git commit -m "feat(project): discovery + auto-registration with collision suffix"
```

---

## Phase 6 — Channel framework + Ollama (Tasks 12–13)

### Task 12: Channel interface and shared contract test

**Files:**
- Create: `internal/channel/channel_test.go`
- Modify: `internal/channel/channel.go`

- [ ] **Step 1: Write failing test**

Create `internal/channel/channel_test.go`:
```go
package channel

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeChannel is a test double used to verify the interface contract.
type fakeChannel struct {
	name     string
	sendErr  error
	respText string
	budget   Budget
	budErr   error
	sleep    time.Duration
}

func (f *fakeChannel) Name() string { return f.name }
func (f *fakeChannel) Send(ctx context.Context, req Request) (Response, error) {
	if f.sleep > 0 {
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(f.sleep):
		}
	}
	if f.sendErr != nil {
		return Response{}, f.sendErr
	}
	return Response{Text: f.respText, EstTokensIn: 10, EstTokensOut: 20}, nil
}
func (f *fakeChannel) BudgetState(ctx context.Context) (Budget, error) {
	return f.budget, f.budErr
}

func TestContract_NameNonEmpty(t *testing.T) {
	c := &fakeChannel{name: "fake"}
	if c.Name() == "" {
		t.Error("Name() returned empty string")
	}
}

func TestContract_SendReturnsResponse(t *testing.T) {
	c := &fakeChannel{name: "fake", respText: "hello"}
	resp, err := c.Send(context.Background(), Request{Prompt: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello" {
		t.Errorf("Text = %q, want hello", resp.Text)
	}
}

func TestContract_SendHonorsContextCancel(t *testing.T) {
	c := &fakeChannel{name: "fake", sleep: 500 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := c.Send(ctx, Request{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
}

func TestContract_BudgetReachable(t *testing.T) {
	c := &fakeChannel{name: "fake", budget: Budget{UsedPct: 12.5}}
	got, err := c.BudgetState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedPct != 12.5 {
		t.Errorf("UsedPct = %.2f, want 12.5", got.UsedPct)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/channel/`
Expected: build failure (`Channel`, `Request`, `Response`, `Budget` undefined).

- [ ] **Step 3: Implement channel.go**

Overwrite `internal/channel/channel.go`:
```go
// Package channel defines the abstraction every model provider implements
// so the router can treat them interchangeably.
package channel

import (
	"context"
	"time"
)

// Channel is the provider abstraction (Claude, Codex, Gemini, Ollama).
type Channel interface {
	Name() string
	Send(ctx context.Context, req Request) (Response, error)
	BudgetState(ctx context.Context) (Budget, error)
}

// Request is a single outbound call.
type Request struct {
	Model       string       // provider-specific identifier ("sonnet-4-6", "qwen2.5-coder:14b")
	System      string       // optional system prompt
	Prompt      string       // user prompt
	Attachments []Attachment // file contents to inline-include
	Interactive bool         // if true, exec interactively (build verb); response will be empty
	WorkingDir  string       // execute relative to this dir (used for interactive verbs)
}

// Attachment is a file the channel should consider as context.
type Attachment struct {
	Path string
	Mime string // e.g. "text/markdown"; optional
}

// Response is the channel's reply.
type Response struct {
	Text         string
	EstTokensIn  int
	EstTokensOut int
}

// Budget is the channel's current spend posture.
type Budget struct {
	UsedPct       float64
	LimitHit      bool
	CooldownUntil time.Time
}

// ErrorKind classifies a Send error for telemetry. Channels SHOULD wrap their
// errors so the router can call ErrorKind(err) to label them.
type ErrorKindLabel string

const (
	ErrKindTimeout ErrorKindLabel = "timeout"
	ErrKindRateLimit ErrorKindLabel = "429"
	ErrKindServer    ErrorKindLabel = "5xx"
	ErrKindOther     ErrorKindLabel = "other"
)

// ClassifiedError lets channels emit a structured error kind.
type ClassifiedError struct {
	Kind ErrorKindLabel
	Err  error
}

func (c *ClassifiedError) Error() string { return c.Err.Error() }
func (c *ClassifiedError) Unwrap() error { return c.Err }
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/channel/ -v`
Expected: 4 contract tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/channel.go internal/channel/channel_test.go
git commit -m "feat(channel): define Channel interface + Request/Response/Budget types"
```

---

### Task 13: Ollama channel

**Files:**
- Create: `internal/channel/ollama/ollama_test.go`
- Modify: `internal/channel/ollama/ollama.go`

- [ ] **Step 1: Write failing test**

Create `internal/channel/ollama/ollama_test.go`:
```go
package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func TestSend_ParsesChatResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"hi back"},"done":true}`))
	}))
	defer srv.Close()

	c := NewWithBaseURL(srv.URL)
	resp, err := c.Send(context.Background(), channel.Request{Model: "qwen2.5-coder:14b", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi back" {
		t.Errorf("Text = %q, want %q", resp.Text, "hi back")
	}
}

func TestSend_EmitsCorrectPayload(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"message":{"content":"ok"}}`))
	}))
	defer srv.Close()
	c := NewWithBaseURL(srv.URL)
	_, err := c.Send(context.Background(), channel.Request{Model: "qwen2.5-coder:14b", System: "be terse", Prompt: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "qwen2.5-coder:14b" {
		t.Errorf("model = %v, want qwen2.5-coder:14b", gotBody["model"])
	}
	if gotBody["stream"] != false {
		t.Errorf("stream = %v, want false", gotBody["stream"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2 (system + user)", len(msgs))
	}
}

func TestSend_NetworkErrorIsClassified(t *testing.T) {
	c := NewWithBaseURL("http://127.0.0.1:1") // unreachable
	_, err := c.Send(context.Background(), channel.Request{Model: "x", Prompt: "y"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *channel.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClassifiedError, got %v", err)
	}
}

func TestSend_HonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewWithBaseURL(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Send(ctx, channel.Request{Model: "x", Prompt: "y"})
	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Errorf("expected context error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/channel/ollama/...`
Expected: build failure (`NewWithBaseURL` undefined).

- [ ] **Step 3: Implement ollama.go**

Overwrite `internal/channel/ollama/ollama.go`:
```go
// Package ollama implements the Channel interface against a local Ollama
// instance (http://localhost:11434 by default).
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/channel"
)

const defaultBaseURL = "http://localhost:11434"

// Channel is the Ollama implementation.
type Channel struct {
	baseURL string
	client  *http.Client
}

// New returns a Channel pointing at the default localhost endpoint.
func New() *Channel { return NewWithBaseURL(defaultBaseURL) }

// NewWithBaseURL is used in tests / non-default deployments.
func NewWithBaseURL(base string) *Channel {
	return &Channel{
		baseURL: strings.TrimRight(base, "/"),
		client:  &http.Client{Timeout: 15 * time.Minute},
	}
}

// Name implements channel.Channel.
func (c *Channel) Name() string { return "ollama" }

// BudgetState implements channel.Channel. Ollama is local-only and unlimited.
func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

// Send implements channel.Channel.
func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return channel.Response{}, errors.New("ollama channel does not support interactive mode")
	}
	if err := c.ensureUp(ctx); err != nil {
		return channel.Response{}, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
	}

	msgs := []chatMessage{}
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	prompt := req.Prompt
	for _, a := range req.Attachments {
		prompt += "\n\n--- FILE: " + a.Path + " ---\n" // attachments inlined for ollama
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: prompt})

	body, err := json.Marshal(chatRequest{Model: req.Model, Stream: false, Messages: msgs})
	if err != nil {
		return channel.Response{}, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return channel.Response{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return channel.Response{}, classifyHTTPError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return channel.Response{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return channel.Response{}, &channel.ClassifiedError{
			Kind: classifyStatus(resp.StatusCode),
			Err:  fmt.Errorf("ollama %d: %s", resp.StatusCode, string(raw)),
		}
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return channel.Response{}, fmt.Errorf("parse response: %w", err)
	}
	return channel.Response{
		Text:         cr.Message.Content,
		EstTokensIn:  estimateTokens(prompt + req.System),
		EstTokensOut: estimateTokens(cr.Message.Content),
	}, nil
}

func (c *Channel) ensureUp(ctx context.Context) error {
	if c.ping(ctx) {
		return nil
	}
	// Try to launch the Ollama desktop app (macOS).
	_ = exec.CommandContext(ctx, "open", "-a", "Ollama").Run()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		if c.ping(ctx) {
			return nil
		}
	}
	return errors.New("Ollama did not respond on /api/tags after 20s")
}

func (c *Channel) ping(ctx context.Context) bool {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(pingCtx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

func estimateTokens(s string) int { return len(s) / 4 }

func classifyHTTPError(err error) error {
	if uerr, ok := err.(*url.Error); ok {
		if uerr.Timeout() {
			return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
		}
	}
	return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
}

func classifyStatus(code int) channel.ErrorKindLabel {
	switch {
	case code == http.StatusTooManyRequests:
		return channel.ErrKindRateLimit
	case code >= 500:
		return channel.ErrKindServer
	default:
		return channel.ErrKindOther
	}
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/channel/ollama/... -v`
Expected: 4 tests PASS.

- [ ] **Step 5: Integration smoke (manual, requires Ollama running)**

Run a tiny program:
```bash
cat <<'EOF' > /tmp/styx_ollama_smoke.go
package main

import (
	"context"
	"fmt"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/channel/ollama"
)

func main() {
	c := ollama.New()
	r, err := c.Send(context.Background(), channel.Request{
		Model:  "qwen2.5-coder:7b",
		Prompt: "Reply with the single word: pong",
	})
	if err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Println("text:", r.Text)
}
EOF
cd /Users/ishaanbatra/Documents/GitHub/styx && go run /tmp/styx_ollama_smoke.go
rm /tmp/styx_ollama_smoke.go
```
Expected: prints `text: pong` (or similar single word).

- [ ] **Step 6: Commit**

```bash
git add internal/channel/ollama/
git commit -m "feat(channel/ollama): HTTP chat API + auto-launch + error classification"
```

---

## Phase 7 — Router (Tasks 14–15)

### Task 14: Signal extractor

**Files:**
- Create: `internal/signals/signals_test.go`
- Modify: `internal/signals/signals.go`

- [ ] **Step 1: Write failing test**

Create `internal/signals/signals_test.go`:
```go
package signals

import (
	"sort"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/ishaanbatra/styx/internal/config"
)

func TestExtract(t *testing.T) {
	cases := []struct {
		name string
		verb string
		args []string
		proj config.Project
		want []string
	}{
		{
			name: "grunt-trivial",
			verb: "grunt",
			args: []string{"format this json"},
			proj: config.Project{Language: "python"},
			want: []string{"lang:python", "trivial"},
		},
		{
			name: "grunt-not-trivial",
			verb: "grunt",
			args: []string{strings.Repeat("a", 200)},
			proj: config.Project{Language: "go"},
			want: []string{"lang:go"},
		},
		{
			name: "plan-complex-keyword",
			verb: "plan",
			args: []string{"refactor the auth middleware"},
			proj: config.Project{Language: "python"},
			want: []string{"complex", "lang:python"},
		},
		{
			name: "build-interactive",
			verb: "build",
			args: nil,
			proj: config.Project{Language: "typescript"},
			want: []string{"interactive", "lang:typescript"},
		},
		{
			name: "think-deep",
			verb: "think",
			args: []string{"deep: should we adopt event sourcing"},
			proj: config.Project{Language: "go"},
			want: []string{"deep", "lang:go"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Extract(c.verb, c.args, c.proj)
			sort.Strings(got)
			sort.Strings(c.want)
			if diff := cmp.Diff(c.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/signals/...`
Expected: build failure (`Extract` undefined).

- [ ] **Step 3: Implement signals.go**

Overwrite `internal/signals/signals.go`:
```go
// Package signals classifies a request into routing tags consumed by the
// router's rule evaluator. Pure function; no I/O.
package signals

import (
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
)

const trivialMaxChars = 50

var complexKeywords = []string{
	"architecture", "refactor", "migrate", "redesign", "rewrite",
}

// Extract turns (verb, args, project) into a deduplicated, sorted set of signals.
func Extract(verb string, args []string, proj config.Project) []string {
	set := map[string]struct{}{}
	add := func(s string) { set[s] = struct{}{} }
	joined := strings.ToLower(strings.Join(args, " "))

	if proj.Language != "" {
		add("lang:" + proj.Language)
	}

	switch verb {
	case "build":
		add("interactive")
	case "grunt":
		if len(joined) > 0 && len(joined) < trivialMaxChars {
			add("trivial")
		}
	case "think":
		if strings.HasPrefix(joined, "deep:") || strings.Contains(joined, "deep think") {
			add("deep")
		}
	}

	for _, kw := range complexKeywords {
		if strings.Contains(joined, kw) {
			add("complex")
			break
		}
	}

	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/signals/... -v`
Expected: 5 sub-tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/signals/
git commit -m "feat(signals): pure-function classifier (lang, trivial, complex, interactive, deep)"
```

---

### Task 15: Router with budget-aware fallback

**Files:**
- Create: `internal/router/router_test.go`
- Modify: `internal/router/router.go`

- [ ] **Step 1: Write failing test**

Create `internal/router/router_test.go`:
```go
package router

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/ishaanbatra/styx/internal/config"
)

type stubBudget struct {
	used map[string]float64
}

func (s *stubBudget) UsedPct(_ context.Context, channel string) (float64, error) {
	return s.used[channel], nil
}

func newRouter(rules []config.Rule, caps config.BudgetCaps, used map[string]float64) *Router {
	return &Router{
		Rules: rules,
		Caps:  caps,
		Budget: &stubBudget{used: used},
	}
}

func TestRoute_FirstMatchWins(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus-4-7"},
			{Verb: "plan", Use: "claude:sonnet-4-6"},
		},
		config.BudgetCaps{},
		nil,
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan", Signals: []string{"complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "claude" || dec.Model != "opus-4-7" {
		t.Errorf("got %s:%s, want claude:opus-4-7", dec.Channel, dec.Model)
	}
}

func TestRoute_SignalsMustAllMatch(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "grunt", Signals: []string{"trivial"}, Use: "ollama:qwen2.5-coder:7b"},
			{Verb: "grunt", Use: "ollama:qwen2.5-coder:14b"},
		},
		config.BudgetCaps{},
		nil,
	)
	// Without "trivial" signal, second rule should win.
	dec, err := r.Route(context.Background(), Request{Verb: "grunt", Signals: []string{"lang:python"}})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Model != "qwen2.5-coder:14b" {
		t.Errorf("got model %q, want qwen2.5-coder:14b", dec.Model)
	}
}

func TestRoute_BudgetCapDegradesToFallback(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Use: "claude:sonnet-4-6", Fallback: []string{"codex:gpt-5", "ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}},
		map[string]float64{"claude": 90},
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "codex" {
		t.Errorf("expected degradation to codex, got %s:%s", dec.Channel, dec.Model)
	}
	if dec.Degraded == false {
		t.Errorf("expected Degraded=true when primary is over cap")
	}
}

func TestRoute_NoMatchDefaultsToOllama(t *testing.T) {
	r := newRouter(nil, config.BudgetCaps{}, nil)
	dec, err := r.Route(context.Background(), Request{Verb: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "ollama" {
		t.Errorf("default channel = %s, want ollama", dec.Channel)
	}
}

func TestRoute_ParallelRule(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "review", Parallel: []string{"claude:sonnet-4-6", "codex:gpt-5"}, SynthesizeWith: "claude:sonnet-4-6"},
		},
		config.BudgetCaps{},
		nil,
	)
	dec, err := r.Route(context.Background(), Request{Verb: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.Parallel {
		t.Fatal("expected Parallel=true")
	}
	wantTargets := []ChannelModel{{"claude", "sonnet-4-6"}, {"codex", "gpt-5"}}
	if diff := cmp.Diff(wantTargets, dec.ParallelTargets); diff != "" {
		t.Errorf("ParallelTargets mismatch:\n%s", diff)
	}
	if dec.SynthesizeWith.Channel != "claude" || dec.SynthesizeWith.Model != "sonnet-4-6" {
		t.Errorf("SynthesizeWith = %+v, want claude:sonnet-4-6", dec.SynthesizeWith)
	}
}

func TestExplain_DescribesPickedRule(t *testing.T) {
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Use: "claude:sonnet-4-6"},
		},
		config.BudgetCaps{},
		nil,
	)
	out := r.Explain(context.Background(), Request{Verb: "plan"})
	if !contains(out, "claude:sonnet-4-6") || !contains(out, "rule") {
		t.Errorf("Explain output missing expected content:\n%s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/router/...`
Expected: build failure (`Router`, `Request`, `Decision`, `ChannelModel`, `BudgetSource` undefined).

- [ ] **Step 3: Implement router.go**

Overwrite `internal/router/router.go`:
```go
// Package router evaluates routing.toml rules and picks a (channel, model)
// for each request, with budget-aware degradation and fallback chains.
package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
)

// BudgetSource abstracts the per-channel usage backend (sqlite in prod,
// in-memory stub in tests).
type BudgetSource interface {
	UsedPct(ctx context.Context, channel string) (float64, error)
}

// Router evaluates rules + budget state to produce a Decision.
type Router struct {
	Rules  []config.Rule
	Caps   config.BudgetCaps
	Budget BudgetSource
}

// Request is the input to Route.
type Request struct {
	Verb    string
	Args    []string
	Signals []string
}

// ChannelModel is a fully-qualified target.
type ChannelModel struct {
	Channel string
	Model   string
}

// Decision describes the chosen channel + fallback chain.
type Decision struct {
	Channel  string
	Model    string
	Fallback []ChannelModel
	RuleIdx  int    // -1 if no rule matched (default)
	Reason   string // human-readable trace

	// Parallel-rule fields (review verb)
	Parallel        bool
	ParallelTargets []ChannelModel
	SynthesizeWith  ChannelModel

	// Degraded is true when budget caused a fallback to be selected.
	Degraded bool
}

// FromConfig builds a Router using the standard config + sqlite budget tracker.
func FromConfig(routing config.Routing, b BudgetSource) *Router {
	return &Router{Rules: routing.Rules, Caps: routing.Budget, Budget: b}
}

// Route picks a channel:model for `req`.
func (r *Router) Route(ctx context.Context, req Request) (Decision, error) {
	idx, rule, ok := r.matchRule(req)
	if !ok {
		return Decision{
			Channel: "ollama", Model: "qwen2.5-coder:14b",
			RuleIdx: -1,
			Reason:  fmt.Sprintf("no rule matched verb=%q; defaulting to ollama:qwen2.5-coder:14b", req.Verb),
		}, nil
	}

	if len(rule.Parallel) > 0 {
		targets := make([]ChannelModel, 0, len(rule.Parallel))
		for _, p := range rule.Parallel {
			cm, err := parseChannelModel(p)
			if err != nil {
				return Decision{}, err
			}
			targets = append(targets, cm)
		}
		synth, err := parseChannelModel(rule.SynthesizeWith)
		if err != nil {
			return Decision{}, err
		}
		return Decision{
			Channel: targets[0].Channel, Model: targets[0].Model,
			RuleIdx: idx, Parallel: true,
			ParallelTargets: targets, SynthesizeWith: synth,
			Reason: fmt.Sprintf("matched rule #%d (parallel)", idx),
		}, nil
	}

	primary, err := parseChannelModel(rule.Use)
	if err != nil {
		return Decision{}, err
	}
	fallback := []ChannelModel{}
	for _, f := range rule.Fallback {
		cm, err := parseChannelModel(f)
		if err != nil {
			return Decision{}, err
		}
		fallback = append(fallback, cm)
	}

	// Budget-aware degradation: if primary is over its cap, walk into fallback.
	chosen := primary
	degraded := false
	reason := fmt.Sprintf("matched rule #%d -> %s:%s", idx, chosen.Channel, chosen.Model)
	if r.overCap(ctx, chosen.Channel) {
		degraded = true
		for _, f := range fallback {
			if !r.overCap(ctx, f.Channel) {
				reason = fmt.Sprintf("rule #%d primary (%s:%s) over cap; degraded to %s:%s",
					idx, primary.Channel, primary.Model, f.Channel, f.Model)
				chosen = f
				break
			}
		}
	}
	return Decision{
		Channel: chosen.Channel, Model: chosen.Model,
		Fallback: fallback, RuleIdx: idx, Reason: reason, Degraded: degraded,
	}, nil
}

// Explain returns a human-readable trace of routing for `req`.
func (r *Router) Explain(ctx context.Context, req Request) string {
	d, err := r.Route(ctx, req)
	if err != nil {
		return "router error: " + err.Error()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "verb=%q signals=%v\n", req.Verb, req.Signals)
	fmt.Fprintf(&b, "decision: %s:%s\n", d.Channel, d.Model)
	fmt.Fprintf(&b, "reason: %s\n", d.Reason)
	if len(d.Fallback) > 0 {
		fmt.Fprintf(&b, "fallback: ")
		for i, f := range d.Fallback {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%s:%s", f.Channel, f.Model)
		}
		b.WriteString("\n")
	}
	if d.Parallel {
		fmt.Fprintf(&b, "parallel targets: %v synthesize_with: %s:%s\n", d.ParallelTargets, d.SynthesizeWith.Channel, d.SynthesizeWith.Model)
	}
	return b.String()
}

func (r *Router) matchRule(req Request) (int, config.Rule, bool) {
	for i, rule := range r.Rules {
		if rule.Verb != req.Verb {
			continue
		}
		if !signalsContainAll(req.Signals, rule.Signals) {
			continue
		}
		return i, rule, true
	}
	return -1, config.Rule{}, false
}

func signalsContainAll(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := map[string]struct{}{}
	for _, s := range have {
		set[s] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}

func (r *Router) overCap(ctx context.Context, channel string) bool {
	cap := r.capFor(channel)
	if cap <= 0 || r.Budget == nil {
		return false
	}
	used, err := r.Budget.UsedPct(ctx, channel)
	if err != nil {
		return false
	}
	return used >= cap
}

func (r *Router) capFor(channel string) float64 {
	switch channel {
	case "claude":
		return r.Caps.Claude.CapPct
	case "codex":
		return r.Caps.Codex.CapPct
	case "gemini_free", "gemini":
		return r.Caps.GeminiFree.CapPct
	case "gemini_paid":
		return r.Caps.GeminiPaid.CapPct
	}
	return 0
}

// parseChannelModel splits "channel:model" into a typed pair.
// Special "channel:interactive" sentinel is parsed too.
func parseChannelModel(s string) (ChannelModel, error) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		return ChannelModel{}, fmt.Errorf("invalid channel:model %q", s)
	}
	return ChannelModel{Channel: s[:idx], Model: s[idx+1:]}, nil
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/router/... -v`
Expected: all 6 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/router/
git commit -m "feat(router): rule eval + budget degradation + parallel + explain"
```

---

## Phase 8 — Brief writer (Task 16)

### Task 16: Brief and plan file I/O

**Files:**
- Create: `internal/brief/brief_test.go`
- Modify: `internal/brief/brief.go`

- [ ] **Step 1: Write failing test**

Create `internal/brief/brief_test.go`:
```go
package brief

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteBrief_WritesMarkdownToResearchDir(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs", "research"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := WriteBrief(WriteOpts{
		ProjectPath: root,
		SubDir:      "docs/research",
		Query:       "pgvector dim limits",
		Body:        "## Findings\nGemini blah.",
		Now:         time.Date(2026, 5, 27, 10, 30, 15, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p, "20260527-103015-pgvector-dim-limits-brief.md") {
		t.Errorf("path = %q, want suffix 20260527-103015-pgvector-dim-limits-brief.md", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "## Findings") {
		t.Errorf("brief body missing; got:\n%s", string(b))
	}
}

func TestLoadLatest_ReturnsNewestBrief(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"20260101-100000-old-brief.md",
		"20260527-091500-newer-brief.md",
		"20260527-103015-newest-brief.md",
		"unrelated.txt",
	}
	for _, f := range files {
		path := filepath.Join(dir, f)
		if err := os.WriteFile(path, []byte("body"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadLatest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "20260527-103015-newest-brief.md") {
		t.Errorf("got %q, want newest brief", got)
	}
}

func TestLoadLatest_NoBriefsReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadLatest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/brief/...`
Expected: build failure (`WriteBrief`, `WriteOpts`, `LoadLatest` undefined).

- [ ] **Step 3: Implement brief.go**

Overwrite `internal/brief/brief.go`:
```go
// Package brief writes research briefs and implementation plans into a
// project's configured directories and resolves the most recent brief.
package brief

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

// WriteOpts configures WriteBrief / WritePlan.
type WriteOpts struct {
	ProjectPath string    // absolute project root
	SubDir      string    // relative to ProjectPath; e.g. "docs/research" or "styx/research"
	Query       string    // used for the slug and as a header
	Body        string    // markdown body
	Kind        string    // "brief" or "plan"
	Now         time.Time // defaults to time.Now() when zero
}

// WriteBrief writes a research brief markdown file and returns its absolute path.
func WriteBrief(o WriteOpts) (string, error) {
	o.Kind = "brief"
	return writeMarkdown(o)
}

// WritePlan writes a plan markdown file and returns its absolute path.
func WritePlan(o WriteOpts) (string, error) {
	o.Kind = "plan"
	return writeMarkdown(o)
}

func writeMarkdown(o WriteOpts) (string, error) {
	if o.ProjectPath == "" {
		return "", errors.New("WriteOpts.ProjectPath is required")
	}
	if o.SubDir == "" {
		return "", errors.New("WriteOpts.SubDir is required")
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	dir := filepath.Join(o.ProjectPath, o.SubDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	stamp := o.Now.UTC().Format("20060102-150405")
	slug := slugify(o.Query)
	name := fmt.Sprintf("%s-%s-%s.md", stamp, slug, o.Kind)
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(o.Body), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", full, err)
	}
	return full, nil
}

// LoadLatest returns the absolute path of the most recent *.md file in dir
// whose name matches the timestamp-prefixed brief/plan format, or "" if none.
func LoadLatest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", dir, err)
	}
	var matches []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		matches = append(matches, name)
	}
	if len(matches) == 0 {
		return "", nil
	}
	sort.Strings(matches)
	return filepath.Join(dir, matches[len(matches)-1]), nil
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	if s == "" {
		s = "untitled"
	}
	return s
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/brief/... -v`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/brief/
git commit -m "feat(brief): write briefs/plans + LoadLatest by timestamp"
```

---

## Phase 9 — Cloud channels (Tasks 17–19)

The three cloud channels share a common pattern: shell out to a CLI (`claude`, `codex`, `gemini`) for the primary path, capture stdout, classify errors. The Gemini channel additionally falls back to a direct HTTP API call when the CLI is missing.

### Task 17: Claude channel

**Files:**
- Create: `internal/channel/claude/claude_test.go`
- Modify: `internal/channel/claude/claude.go`

- [ ] **Step 1: Write failing test**

Create `internal/channel/claude/claude_test.go`:
```go
package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

// fakeCLI writes a stub `claude` script to a tmp dir and returns its parent dir
// (suitable for prepending to PATH).
func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSend_OneShotCapturesStdout(t *testing.T) {
	dir := fakeCLI(t, `printf 'planned: %s\n' "$3"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "sonnet-4-6", Prompt: "do thing"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text == "" {
		t.Error("expected non-empty Text")
	}
}

func TestSend_NonZeroExitIsError(t *testing.T) {
	dir := fakeCLI(t, `echo "boom" >&2; exit 2`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "sonnet-4-6", Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSend_MissingBinaryIsClassified(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "sonnet-4-6", Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/channel/claude/...`
Expected: build failure (`New` undefined in claude package).

- [ ] **Step 3: Implement claude.go**

Overwrite `internal/channel/claude/claude.go`:
```go
// Package claude implements the Channel interface against the local `claude`
// CLI (Claude Code). It supports one-shot (-p) and interactive modes.
package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/ishaanbatra/styx/internal/channel"
)

// Channel is the Claude implementation.
type Channel struct {
	bin string // override-able for tests; "" means look up "claude" on PATH
}

// New returns a Claude channel that finds `claude` on PATH.
func New() *Channel { return &Channel{} }

// Name implements channel.Channel.
func (c *Channel) Name() string { return "claude" }

// BudgetState implements channel.Channel. Without a stable `claude --usage`
// surface, we report unknown (zero) and rely on the local sqlite tracker for
// budget enforcement.
func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

// Send implements channel.Channel.
func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return c.sendInteractive(ctx, req)
	}
	return c.sendOneShot(ctx, req)
}

func (c *Channel) sendOneShot(ctx context.Context, req channel.Request) (channel.Response, error) {
	args := []string{"-p", req.Prompt}
	if req.Model != "" {
		args = append([]string{"--model", req.Model}, args...)
	}
	cmd := exec.CommandContext(ctx, c.binary(), args...)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	out, err := cmd.Output()
	if err != nil {
		return channel.Response{}, classifyExecError(err)
	}
	text := strings.TrimRight(string(out), "\n")
	return channel.Response{
		Text:         text,
		EstTokensIn:  estimateTokens(req.Prompt + req.System),
		EstTokensOut: estimateTokens(text),
	}, nil
}

func (c *Channel) sendInteractive(ctx context.Context, req channel.Request) (channel.Response, error) {
	args := []string{}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	cmd := exec.CommandContext(ctx, c.binary(), args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	if err := cmd.Run(); err != nil {
		return channel.Response{}, classifyExecError(err)
	}
	return channel.Response{}, nil
}

func (c *Channel) binary() string {
	if c.bin != "" {
		return c.bin
	}
	return "claude"
}

func estimateTokens(s string) int { return len(s) / 4 }

func classifyExecError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("claude CLI not found on PATH: %w", err)}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		// SIGPIPE / 124 (timeout) → timeout kind
		if status, ok := ee.Sys().(syscall.WaitStatus); ok {
			if status.Signal() == syscall.SIGKILL || status.Signal() == syscall.SIGTERM {
				return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
			}
		}
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("claude exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
	}
	return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/channel/claude/... -v`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/claude/
git commit -m "feat(channel/claude): one-shot + interactive via `claude` CLI"
```

---

### Task 18: Codex channel

**Files:**
- Create: `internal/channel/codex/codex_test.go`
- Modify: `internal/channel/codex/codex.go`

- [ ] **Step 1: Write failing test**

Create `internal/channel/codex/codex_test.go`:
```go
package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func fakeCLI(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "codex")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSend_OneShotCapturesStdout(t *testing.T) {
	dir := fakeCLI(t, `echo "codex says hi"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "gpt-5", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "codex says hi" {
		t.Errorf("Text = %q, want codex says hi", resp.Text)
	}
}

func TestSend_MissingBinary(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	c := New()
	_, err := c.Send(context.Background(), channel.Request{Model: "gpt-5", Prompt: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/channel/codex/...`
Expected: build failure (`New` undefined in codex package).

- [ ] **Step 3: Implement codex.go**

Overwrite `internal/channel/codex/codex.go`:
```go
// Package codex implements the Channel interface against the local `codex`
// CLI (OpenAI Codex, signed-in via ChatGPT account).
package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/ishaanbatra/styx/internal/channel"
)

type Channel struct{}

func New() *Channel { return &Channel{} }

func (c *Channel) Name() string { return "codex" }

func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return c.sendInteractive(ctx, req)
	}
	return c.sendOneShot(ctx, req)
}

func (c *Channel) sendOneShot(ctx context.Context, req channel.Request) (channel.Response, error) {
	// Codex CLI invocation: `codex --model <model> exec "<prompt>"`.
	// If the actual CLI uses a different verb, this is the single spot to adjust.
	args := []string{}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, "exec", req.Prompt)
	cmd := exec.CommandContext(ctx, "codex", args...)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	out, err := cmd.Output()
	if err != nil {
		return channel.Response{}, classifyExecError(err)
	}
	text := strings.TrimRight(string(out), "\n")
	return channel.Response{
		Text:         text,
		EstTokensIn:  estimateTokens(req.Prompt + req.System),
		EstTokensOut: estimateTokens(text),
	}, nil
}

func (c *Channel) sendInteractive(ctx context.Context, req channel.Request) (channel.Response, error) {
	args := []string{}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	if err := cmd.Run(); err != nil {
		return channel.Response{}, classifyExecError(err)
	}
	return channel.Response{}, nil
}

func estimateTokens(s string) int { return len(s) / 4 }

func classifyExecError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("codex CLI not found on PATH: %w", err)}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok {
			if status.Signal() == syscall.SIGKILL || status.Signal() == syscall.SIGTERM {
				return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
			}
		}
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("codex exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))}
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
	}
	return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/channel/codex/... -v`
Expected: 2 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/codex/
git commit -m "feat(channel/codex): one-shot + interactive via `codex` CLI"
```

---

### Task 19: Gemini channel (CLI primary, HTTP fallback)

**Files:**
- Create: `internal/channel/gemini/gemini_test.go`
- Modify: `internal/channel/gemini/gemini.go`

- [ ] **Step 1: Write failing test**

Create `internal/channel/gemini/gemini_test.go`:
```go
package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func fakeCLI(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSend_PrefersGeminiCLIWhenPresent(t *testing.T) {
	dir := fakeCLI(t, "gemini", `echo "from cli"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "flash", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "from cli" {
		t.Errorf("Text = %q, want from cli", resp.Text)
	}
}

func TestSend_FallsBackToHTTPWhenCLIMissing(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"from api"}]}}]}`))
	}))
	defer srv.Close()
	c := NewWithConfig(Config{APIBaseURL: srv.URL, APIKey: "test-key"})
	resp, err := c.Send(context.Background(), channel.Request{Model: "flash", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "from api" {
		t.Errorf("Text = %q, want from api", resp.Text)
	}
}

func TestSend_HTTPRequestUsesAPIKey(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	var gotKey string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer srv.Close()
	c := NewWithConfig(Config{APIBaseURL: srv.URL, APIKey: "K123"})
	_, err := c.Send(context.Background(), channel.Request{Model: "flash", Prompt: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "K123" {
		t.Errorf("api key = %q, want K123", gotKey)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/channel/gemini/...`
Expected: build failure.

- [ ] **Step 3: Implement gemini.go**

Overwrite `internal/channel/gemini/gemini.go`:
```go
// Package gemini implements the Channel interface against Google's Gemini.
// Preferred path: `gemini-cli` (uses authenticated $20/mo subscription quota).
// Fallback path: direct HTTP to generativelanguage.googleapis.com using a
// Keychain-stored API key (free dev tier).
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
)

const defaultAPIBase = "https://generativelanguage.googleapis.com/v1beta/models"

// Config injects test-time overrides.
type Config struct {
	APIBaseURL string // default: generativelanguage.googleapis.com/v1beta/models
	APIKey     string // when "", looked up from Keychain as "gemini_api_key"
	CLIName    string // default: "gemini"
}

type Channel struct {
	cfg    Config
	client *http.Client
}

func New() *Channel { return NewWithConfig(Config{}) }

func NewWithConfig(cfg Config) *Channel {
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = defaultAPIBase
	}
	if cfg.CLIName == "" {
		cfg.CLIName = "gemini"
	}
	return &Channel{cfg: cfg, client: &http.Client{Timeout: 5 * time.Minute}}
}

func (c *Channel) Name() string { return "gemini" }

func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return channel.Response{}, errors.New("gemini channel does not support interactive mode")
	}
	if _, err := exec.LookPath(c.cfg.CLIName); err == nil {
		return c.sendViaCLI(ctx, req)
	}
	return c.sendViaHTTP(ctx, req)
}

func (c *Channel) sendViaCLI(ctx context.Context, req channel.Request) (channel.Response, error) {
	args := []string{"-p", req.Prompt}
	if req.Model != "" {
		args = append([]string{"--model", req.Model}, args...)
	}
	cmd := exec.CommandContext(ctx, c.cfg.CLIName, args...)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	out, err := cmd.Output()
	if err != nil {
		return channel.Response{}, classifyExecError(err)
	}
	text := strings.TrimRight(string(out), "\n")
	return channel.Response{
		Text:         text,
		EstTokensIn:  estimateTokens(req.Prompt + req.System),
		EstTokensOut: estimateTokens(text),
	}, nil
}

type httpRequestBody struct {
	Contents []httpPart `json:"contents"`
}

type httpPart struct {
	Parts []struct {
		Text string `json:"text"`
	} `json:"parts"`
}

type httpResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (c *Channel) sendViaHTTP(ctx context.Context, req channel.Request) (channel.Response, error) {
	apiKey := c.cfg.APIKey
	if apiKey == "" {
		k, err := config.Secret("gemini_api_key")
		if err != nil {
			fallback := os.Getenv("GEMINI_API_KEY")
			if fallback == "" {
				return channel.Response{}, &channel.ClassifiedError{
					Kind: channel.ErrKindOther,
					Err:  fmt.Errorf("no gemini api key (set via styx migrate-secrets or GEMINI_API_KEY)"),
				}
			}
			apiKey = fallback
		} else {
			apiKey = k
		}
	}
	model := req.Model
	if model == "" {
		model = "gemini-2.5-flash"
	} else if model == "flash" {
		model = "gemini-2.5-flash"
	} else if model == "pro" {
		model = "gemini-2.5-pro"
	}

	body, _ := json.Marshal(httpRequestBody{
		Contents: []httpPart{{Parts: []struct {
			Text string `json:"text"`
		}{{Text: req.Prompt}}}},
	})
	url := fmt.Sprintf("%s/%s:generateContent?key=%s", c.cfg.APIBaseURL, model, apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return channel.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return channel.Response{}, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return channel.Response{}, &channel.ClassifiedError{
			Kind: classifyStatus(resp.StatusCode),
			Err:  fmt.Errorf("gemini %d: %s", resp.StatusCode, string(raw)),
		}
	}
	var hr httpResponse
	if err := json.Unmarshal(raw, &hr); err != nil {
		return channel.Response{}, err
	}
	text := ""
	if len(hr.Candidates) > 0 && len(hr.Candidates[0].Content.Parts) > 0 {
		text = hr.Candidates[0].Content.Parts[0].Text
	}
	return channel.Response{
		Text:         text,
		EstTokensIn:  estimateTokens(req.Prompt + req.System),
		EstTokensOut: estimateTokens(text),
	}, nil
}

func estimateTokens(s string) int { return len(s) / 4 }

func classifyExecError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok {
			if status.Signal() == syscall.SIGKILL || status.Signal() == syscall.SIGTERM {
				return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
			}
		}
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("gemini exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))}
	}
	return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
}

func classifyStatus(code int) channel.ErrorKindLabel {
	switch {
	case code == http.StatusTooManyRequests:
		return channel.ErrKindRateLimit
	case code >= 500:
		return channel.ErrKindServer
	default:
		return channel.ErrKindOther
	}
}
```

- [ ] **Step 4: Run tests, verify they pass**

Run: `go test ./internal/channel/gemini/... -v`
Expected: 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/gemini/
git commit -m "feat(channel/gemini): gemini-cli primary path, HTTP API fallback"
```

---

## Phase 10 — cmd/styx (Tasks 20–27)

### Task 20: main.go dispatcher + default routing.toml generator + help

**Files:**
- Modify: `cmd/styx/main.go`
- Create: `cmd/styx/help.go`
- Create: `cmd/styx/dispatch.go`

- [ ] **Step 1: Write the main dispatcher**

Overwrite `cmd/styx/main.go`:
```go
// Styx is a personal multi-model dev orchestration CLI.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printHelp()
		return
	}
	verb := os.Args[1]
	args := os.Args[2:]

	if err := ensureFirstRun(); err != nil {
		fmt.Fprintf(os.Stderr, "styx: setup error: %v\n", err)
		os.Exit(1)
	}

	if err := dispatch(verb, args); err != nil {
		fmt.Fprintf(os.Stderr, "styx: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Write help.go**

Create `cmd/styx/help.go`:
```go
package main

import "fmt"

const helpText = `styx — multi-model dev orchestration

USAGE
  styx <verb> [args]

VERBS
  research <query>          Gemini draft + Codex critique -> brief
  deep-research <query>     Open Gemini + ChatGPT in browser; synthesis template
  plan <description>        Draft an implementation plan using the latest brief
  build [target]            Interactive Claude/Codex session in the project dir
  review                    Parallel multi-channel review of git diff main...HEAD
  grunt <prompt>            Quick Ollama pass-through (code gen)
  think <prompt>            Ollama reasoning mode, no code (prefix with "deep:" for Claude)
  explain <file...>         Explain code in given files
  summarize <file...>       Summarize a set of files
  critique <text|file>      Devil's-advocate critique (Codex)
  check                     Dashboard: git status, ollama, latest briefs/plans
  budget                    Per-channel usage summary
  route --explain <verb> "..." Show routing decision for a hypothetical request
  project ls|add|rm|rename  Manage project registry
  migrate-secrets           One-time: move env-var secrets to macOS Keychain
  help                      Show this menu

CONFIG
  ~/.config/styx/routing.toml      routes (you edit)
  ~/.config/styx/projects.toml     registry (auto-managed)
  ~/.config/styx/state/usage.db    usage log

SECRETS
  Stored in macOS Keychain under service "styx". Migrate from env vars with:
    styx migrate-secrets
`

func printHelp() {
	fmt.Print(helpText)
}
```

- [ ] **Step 3: Write dispatch.go**

Create `cmd/styx/dispatch.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/paths"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/channel/claude"
	"github.com/ishaanbatra/styx/internal/channel/codex"
	"github.com/ishaanbatra/styx/internal/channel/gemini"
	"github.com/ishaanbatra/styx/internal/channel/ollama"
)

// app bundles the long-lived dependencies shared by every verb.
type app struct {
	routing  config.Routing
	tracker  *budget.Tracker
	router   *router.Router
	channels map[string]channel.Channel
}

func loadApp() (*app, error) {
	r, err := config.LoadRouting()
	if err != nil {
		return nil, fmt.Errorf("load routing: %w", err)
	}
	t, err := budget.Default()
	if err != nil {
		return nil, fmt.Errorf("open budget tracker: %w", err)
	}
	rt := router.FromConfig(r, &budgetSource{t: t})
	return &app{
		routing:  r,
		tracker:  t,
		router:   rt,
		channels: defaultChannels(),
	}, nil
}

func defaultChannels() map[string]channel.Channel {
	return map[string]channel.Channel{
		"claude": claude.New(),
		"codex":  codex.New(),
		"gemini": gemini.New(),
		"ollama": ollama.New(),
	}
}

// budgetSource adapts *budget.Tracker to router.BudgetSource.
type budgetSource struct{ t *budget.Tracker }

func (b *budgetSource) UsedPct(ctx context.Context, ch string) (float64, error) {
	st, err := b.t.State(ctx, ch)
	if err != nil {
		return 0, err
	}
	return st.UsedPct, nil
}

func dispatch(verb string, args []string) error {
	switch verb {
	case "help", "-h", "--help":
		printHelp()
		return nil
	case "migrate-secrets":
		return cmdMigrateSecrets()
	case "project":
		return cmdProject(args)
	case "route":
		return cmdRoute(args)
	case "budget":
		return cmdBudget(args)
	case "check":
		return cmdCheck(args)
	case "deep-research":
		return cmdDeepResearch(args)
	}

	a, err := loadApp()
	if err != nil {
		return err
	}
	defer a.tracker.Close()

	switch verb {
	case "research":
		return cmdResearch(a, args)
	case "plan":
		return cmdPlan(a, args)
	case "build":
		return cmdBuild(a, args)
	case "review":
		return cmdReview(a, args)
	case "grunt", "think", "explain", "summarize", "critique":
		return cmdOneShot(a, verb, args)
	}
	return fmt.Errorf("unknown verb %q (run `styx help`)", verb)
}

// ensureFirstRun creates the config dir and seeds routing.toml on first run.
func ensureFirstRun() error {
	cfg, err := paths.ConfigDir()
	if err != nil {
		return err
	}
	if err := paths.EnsureDir(cfg); err != nil {
		return err
	}
	if err := paths.EnsureDir(filepath.Join(cfg, "state")); err != nil {
		return err
	}
	routingPath, err := paths.RoutingPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(routingPath); os.IsNotExist(err) {
		if err := os.WriteFile(routingPath, []byte(defaultRoutingTOML), 0o644); err != nil {
			return fmt.Errorf("seed default routing.toml: %w", err)
		}
		fmt.Fprintf(os.Stderr, "[styx] wrote default routing.toml to %s\n", routingPath)
	}
	return nil
}

// resolveTarget converts a "backend|student|teacher|<alias>" arg into a Project.
// Empty arg means the project for the current working directory.
func resolveTarget(arg string) (project.Project, error) {
	if arg == "" {
		return project.Current()
	}
	if p, err := project.Resolve(arg); err == nil {
		return p, nil
	}
	return project.Current()
}
```

- [ ] **Step 4: Add a default-routing constant file**

Create `cmd/styx/default_routing.go`:
```go
package main

const defaultRoutingTOML = ` + "`" + `# Styx routing rules.  Edit freely; first match wins.
# Use ` + "`" + `` + "`" + `styx route --explain <verb> "..."` + "`" + `` + "`" + ` to see why a route was chosen.

[budget]
claude.cap_pct       = 80
codex.cap_pct        = 80
gemini_free.cap_pct  = 70
gemini_paid.cap_pct  = 80

# ── research ──
[[rule]]
verb = "research"
use  = "gemini:flash"
fallback = ["gemini:pro", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "research.critic"
use  = "codex:gpt-5"
fallback = ["ollama:qwen2.5-coder:14b"]

# ── plan ──
[[rule]]
verb = "plan"
signals = ["complex"]
use  = "claude:opus-4-7"
fallback = ["claude:sonnet-4-6", "codex:gpt-5"]

[[rule]]
verb = "plan"
use  = "claude:sonnet-4-6"
fallback = ["codex:gpt-5", "ollama:qwen2.5-coder:14b"]

# ── build (interactive) ──
[[rule]]
verb = "build"
use  = "claude:interactive"
fallback = ["codex:interactive"]

# ── review (parallel) ──
[[rule]]
verb = "review"
parallel = ["claude:sonnet-4-6", "codex:gpt-5"]
synthesize_with = "claude:sonnet-4-6"

# ── grunt / think ──
[[rule]]
verb = "grunt"
signals = ["trivial"]
use  = "ollama:qwen2.5-coder:7b"

[[rule]]
verb = "grunt"
use  = "ollama:qwen2.5-coder:14b"

[[rule]]
verb = "think"
signals = ["deep"]
use  = "claude:sonnet-4-6"

[[rule]]
verb = "think"
use  = "ollama:qwen2.5-coder:14b"

# ── explain / summarize / critique ──
[[rule]]
verb = "explain"
signals = ["large_context"]
use  = "gemini:pro"
fallback = ["claude:sonnet-4-6"]

[[rule]]
verb = "explain"
use  = "ollama:qwen2.5-coder:14b"

[[rule]]
verb = "summarize"
use  = "gemini:pro"
fallback = ["claude:sonnet-4-6", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "critique"
use  = "codex:gpt-5"
fallback = ["claude:sonnet-4-6", "ollama:qwen2.5-coder:14b"]
` + "`" + `
```

- [ ] **Step 5: Stub the verb handlers so build succeeds**

Create `cmd/styx/grunt.go`, `think.go`, `explain.go`, `summarize.go`, `critique.go`, `research.go`, `deep_research.go`, `plan.go`, `build.go`, `review.go`, `check.go`, `budget.go`, `route.go`, `project.go`, `migrate_secrets.go` — each containing the stubs below (one func per file; tasks 21-27 fill these in):

Create `cmd/styx/grunt.go`:
```go
package main

import "errors"

func cmdOneShot(a *app, verb string, args []string) error {
	return errors.New("not implemented yet")
}
```

Create `cmd/styx/research.go`:
```go
package main

import "errors"

func cmdResearch(a *app, args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/deep_research.go`:
```go
package main

import "errors"

func cmdDeepResearch(args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/plan.go`:
```go
package main

import "errors"

func cmdPlan(a *app, args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/build.go`:
```go
package main

import "errors"

func cmdBuild(a *app, args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/review.go`:
```go
package main

import "errors"

func cmdReview(a *app, args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/check.go`:
```go
package main

import "errors"

func cmdCheck(args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/budget.go`:
```go
package main

import "errors"

func cmdBudget(args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/route.go`:
```go
package main

import "errors"

func cmdRoute(args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/project.go`:
```go
package main

import "errors"

func cmdProject(args []string) error { return errors.New("not implemented yet") }
```

Create `cmd/styx/migrate_secrets.go`:
```go
package main

import "errors"

func cmdMigrateSecrets() error { return errors.New("not implemented yet") }
```

- [ ] **Step 6: Build and run help**

Run:
```bash
cd /Users/ishaanbatra/Documents/GitHub/styx
go build -o ./bin/styx ./cmd/styx
./bin/styx help
```
Expected: prints the help text from Step 2.

- [ ] **Step 7: First-run seeds default routing.toml**

Run:
```bash
XDG_CONFIG_HOME=$(mktemp -d) ./bin/styx help 2>&1 | head -3
```
Expected: stderr contains `[styx] wrote default routing.toml to ...`

- [ ] **Step 8: Commit**

```bash
git add cmd/styx/
git commit -m "feat(cmd): dispatcher, help, first-run config seed, verb stubs"
```

---

### Task 21: Simple one-shot verbs (grunt, think, explain, summarize, critique)

**Files:**
- Modify: `cmd/styx/grunt.go`

- [ ] **Step 1: Implement the shared one-shot pipeline**

Overwrite `cmd/styx/grunt.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

// cmdOneShot handles every verb whose pipeline is:
//   1. Read args + maybe file contents
//   2. Resolve current project (best-effort; ok if not in repo)
//   3. Route -> channel -> print to stdout
//   4. Record usage
func cmdOneShot(a *app, verb string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx %s <prompt|file...>", verb)
	}
	prompt, attachments, err := loadPromptAndAttachments(verb, args)
	if err != nil {
		return err
	}

	proj, _ := project.Current() // ok if not in a repo
	sigs := signals.Extract(verb, args, proj)

	// Large-context signal for explain/summarize: rough estimate by combined attachment size.
	if verb == "explain" || verb == "summarize" {
		if totalAttachmentBytes(attachments) > 200_000 {
			sigs = appendUnique(sigs, "large_context")
		}
	}

	req := router.Request{Verb: verb, Args: args, Signals: sigs}
	resp, picked, err := sendWithFallback(a, context.Background(), req, channel.Request{
		Prompt:      prompt,
		Attachments: attachments,
	})
	if err != nil {
		return err
	}
	fmt.Print(resp.Text)
	if !strings.HasSuffix(resp.Text, "\n") {
		fmt.Println()
	}
	fmt.Fprintf(os.Stderr, "[styx] channel=%s:%s\n", picked.Channel, picked.Model)
	return nil
}

// sendWithFallback walks the router's primary + fallback chain, recording
// usage at each attempt. Returns the successful response + the channel that produced it.
func sendWithFallback(a *app, ctx context.Context, req router.Request, cr channel.Request) (channel.Response, router.ChannelModel, error) {
	dec, err := a.router.Route(ctx, req)
	if err != nil {
		return channel.Response{}, router.ChannelModel{}, err
	}
	attempts := []router.ChannelModel{{Channel: dec.Channel, Model: dec.Model}}
	attempts = append(attempts, dec.Fallback...)
	var lastErr error
	for _, t := range attempts {
		ch, ok := a.channels[t.Channel]
		if !ok {
			lastErr = fmt.Errorf("unknown channel %q in routing", t.Channel)
			continue
		}
		cr.Model = t.Model
		resp, err := ch.Send(ctx, cr)
		_ = a.tracker.Record(ctx, budget.Event{
			Channel:   t.Channel,
			Verb:      req.Verb,
			TokensIn:  resp.EstTokensIn,
			TokensOut: resp.EstTokensOut,
			Success:   err == nil,
			ErrorKind: errorKindOf(err),
		})
		if err == nil {
			return resp, t, nil
		}
		fmt.Fprintf(os.Stderr, "[styx] %s failed (%v); falling back\n", t.Channel, err)
		lastErr = err
	}
	return channel.Response{}, router.ChannelModel{}, fmt.Errorf("all channels failed; last err: %w", lastErr)
}

func errorKindOf(err error) string {
	if err == nil {
		return ""
	}
	if ce, ok := err.(*channel.ClassifiedError); ok {
		return string(ce.Kind)
	}
	return "other"
}

func loadPromptAndAttachments(verb string, args []string) (string, []channel.Attachment, error) {
	// For "explain" and "summarize", treat args as file paths that exist.
	// Otherwise treat the joined args as a prompt.
	if verb == "explain" || verb == "summarize" {
		var atts []channel.Attachment
		for _, p := range args {
			if _, err := os.Stat(p); err != nil {
				return "", nil, fmt.Errorf("file not found: %s", p)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", nil, err
			}
			atts = append(atts, channel.Attachment{Path: p})
			_ = b // attachments carry path; channels inline content
		}
		prompt := promptForVerb(verb, args)
		return prompt, atts, nil
	}
	return strings.Join(args, " "), nil, nil
}

func promptForVerb(verb string, args []string) string {
	switch verb {
	case "explain":
		return "Explain the following code clearly. Cover: what it does, why it exists, key control flow, and any subtle behaviors. Files: " + strings.Join(args, ", ")
	case "summarize":
		return "Summarize the following files. Identify their purpose, key responsibilities, and how they relate. Files: " + strings.Join(args, ", ")
	case "critique":
		return "Act as a skeptical senior engineer. Argue against the following. Find holes, untested assumptions, missing context, weak evidence, edge cases that aren't addressed. Be specific.\n\n" + strings.Join(args, " ")
	case "think":
		return "Think step by step through the problem below. Do not write code unless explicitly asked. Focus on tradeoffs, hidden assumptions, edge cases, and design implications.\n\nProblem: " + strings.Join(args, " ")
	}
	return strings.Join(args, " ")
}

func totalAttachmentBytes(atts []channel.Attachment) int {
	total := 0
	for _, a := range atts {
		if fi, err := os.Stat(a.Path); err == nil {
			total += int(fi.Size())
		}
	}
	return total
}

func appendUnique(ss []string, s string) []string {
	for _, x := range ss {
		if x == s {
			return ss
		}
	}
	return append(ss, s)
}
```

- [ ] **Step 2: Build**

Run:
```bash
cd /Users/ishaanbatra/Documents/GitHub/styx
go build -o ./bin/styx ./cmd/styx
```
Expected: success.

- [ ] **Step 3: Smoke test grunt (requires Ollama running, qwen2.5-coder:14b pulled)**

Run:
```bash
./bin/styx grunt "Reply with the word: pong"
```
Expected: prints `pong` (or similar single-word reply) followed by `[styx] channel=ollama:qwen2.5-coder:14b`.

- [ ] **Step 4: Smoke test think with trivial input**

Run:
```bash
./bin/styx think "What is 2+2?"
```
Expected: ollama reply + stderr channel tag.

- [ ] **Step 5: Commit**

```bash
git add cmd/styx/grunt.go
git commit -m "feat(cmd): one-shot pipeline shared by grunt/think/explain/summarize/critique"
```

---

### Task 22: research verb (compound: draft + critique)

**Files:**
- Modify: `cmd/styx/research.go`

- [ ] **Step 1: Implement cmdResearch**

Overwrite `cmd/styx/research.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
)

func cmdResearch(a *app, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx research <query>")
	}
	query := strings.Join(args, " ")
	proj, err := project.Current()
	if err != nil {
		return fmt.Errorf("identify current project: %w", err)
	}
	ctx := context.Background()

	// 1. Draft (Gemini by default)
	draftPrompt := "You are a senior technical researcher. Investigate the following thoroughly. Cover: current best practices, common pitfalls, recommended libraries/approaches, real-world tradeoffs, and concrete code patterns where applicable. Be specific and cite reasoning, not just assertions.\n\nQuery: " + query
	draftResp, draftPicked, draftErr := sendWithFallback(a, ctx,
		router.Request{Verb: "research", Args: args},
		channel.Request{Prompt: draftPrompt})
	draftText := ""
	if draftErr != nil {
		fmt.Fprintf(os.Stderr, "[styx] research draft failed (%v); critic will work from raw query\n", draftErr)
	} else {
		draftText = draftResp.Text
	}

	// 2. Critic
	var criticPrompt string
	if draftText != "" {
		criticPrompt = "You are a skeptical senior engineer. Critically review the research below. Argue against it: find holes, untested assumptions, missing context, weak evidence, edge cases that aren't addressed. Be specific. Do not rewrite the research — argue with it.\n\nRESEARCH TO CRITIQUE:\n" + draftText
	} else {
		criticPrompt = "You are a skeptical senior engineer. External research was unavailable. Analyze this query: surface key questions, hidden assumptions, likely failure modes, and what should be investigated before acting.\n\nQUERY:\n" + query
	}
	criticResp, criticPicked, criticErr := sendWithFallback(a, ctx,
		router.Request{Verb: "research.critic", Args: args},
		channel.Request{Prompt: criticPrompt})
	if criticErr != nil {
		return fmt.Errorf("research critic failed: %w", criticErr)
	}

	// 3. Compose brief
	subDir := proj.ResearchDir
	if subDir == "" {
		subDir = "styx/research"
	}
	body := composeBrief(query, draftText, criticResp.Text, draftPicked, criticPicked)
	out, err := brief.WriteBrief(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      subDir,
		Query:       query,
		Body:        body,
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(proj.Path, out)
	fmt.Printf("✓ Brief saved: %s\n", rel)
	fmt.Printf("  Draft channel:  %s:%s\n", draftPicked.Channel, draftPicked.Model)
	fmt.Printf("  Critic channel: %s:%s\n", criticPicked.Channel, criticPicked.Model)
	return nil
}

func composeBrief(query, draft, critique string, draftCM, critCM router.ChannelModel) string {
	var b strings.Builder
	b.WriteString("# Research Brief\n\n")
	fmt.Fprintf(&b, "**Query**: %s\n", query)
	fmt.Fprintf(&b, "**Date**: %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "**Researcher**: %s:%s\n", draftCM.Channel, draftCM.Model)
	fmt.Fprintf(&b, "**Reviewer**:  %s:%s\n\n", critCM.Channel, critCM.Model)
	b.WriteString("---\n\n## Research\n\n")
	if draft != "" {
		b.WriteString(draft)
	} else {
		b.WriteString("_External research failed; critique below is based on the raw query._\n")
	}
	b.WriteString("\n\n---\n\n## Critical Review\n\n")
	b.WriteString(critique)
	b.WriteString("\n")
	return b.String()
}
```

- [ ] **Step 2: Build**

Run: `go build -o ./bin/styx ./cmd/styx`
Expected: success.

- [ ] **Step 3: Smoke test in this repo**

Run from `/Users/ishaanbatra/Documents/GitHub/styx`:
```bash
./bin/styx research "what is pgvector"
```
Expected: prints `✓ Brief saved: styx/research/<ts>-what-is-pgvector-brief.md`; file exists with `# Research Brief` header and both sections populated.

- [ ] **Step 4: Commit**

```bash
git add cmd/styx/research.go
git commit -m "feat(cmd): research verb (compound draft + critique -> brief)"
```

---

### Task 23: deep-research verb (browser launcher + template)

**Files:**
- Modify: `cmd/styx/deep_research.go`

- [ ] **Step 1: Implement cmdDeepResearch**

Overwrite `cmd/styx/deep_research.go`:
```go
package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/project"
)

func cmdDeepResearch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx deep-research <query>")
	}
	query := strings.Join(args, " ")
	proj, err := project.Current()
	if err != nil {
		return err
	}
	encoded := url.QueryEscape(query)
	_ = exec.Command("open", "https://gemini.google.com/app?q="+encoded).Run()
	_ = exec.Command("open", "https://chat.openai.com/?q="+encoded).Run()

	subDir := proj.ResearchDir
	if subDir == "" {
		subDir = "styx/research"
	}
	body := fmt.Sprintf(`# Deep Research Brief

**Query**: %s
**Date**:  %s
**Mode**:  human-in-the-loop (Gemini Deep Research + ChatGPT)

---

## Gemini Deep Research Findings

<!-- Paste Gemini's output here. Trim to what's actually useful. -->

---

## ChatGPT Second Opinion

<!-- Paste ChatGPT's response. Note where it disagrees with Gemini. -->

---

## Your Synthesis & Decision

<!-- What's the path forward? Where did the two sources agree? Where did they diverge? Which side did you pick, and why? -->
`, query, time.Now().Format("2006-01-02 15:04:05"))

	out, err := brief.WriteBrief(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      subDir,
		Query:       query + " deep",
		Body:        body,
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(proj.Path, out)
	fmt.Printf("✓ Template: %s\n", rel)
	fmt.Println("✓ Opened Gemini and ChatGPT in browser")
	fmt.Println()
	fmt.Println(`Fill in the brief, then: styx plan "<description>"`)
	return nil
}
```

- [ ] **Step 2: Build**

Run: `go build -o ./bin/styx ./cmd/styx`
Expected: success.

- [ ] **Step 3: Smoke test (will open browser tabs)**

Run: `./bin/styx deep-research "test query"` (Ctrl-C the browser tabs after opened).
Expected: template file written under the project's research dir.

- [ ] **Step 4: Commit**

```bash
git add cmd/styx/deep_research.go
git commit -m "feat(cmd): deep-research opens Gemini + ChatGPT, writes synthesis template"
```

---

### Task 24: plan + build verbs

**Files:**
- Modify: `cmd/styx/plan.go`
- Modify: `cmd/styx/build.go`

- [ ] **Step 1: Implement cmdPlan**

Overwrite `cmd/styx/plan.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func cmdPlan(a *app, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx plan <description>")
	}
	desc := strings.Join(args, " ")
	proj, err := project.Current()
	if err != nil {
		return err
	}
	subDirResearch := proj.ResearchDir
	if subDirResearch == "" {
		subDirResearch = "styx/research"
	}
	briefPath, err := brief.LoadLatest(filepath.Join(proj.Path, subDirResearch))
	if err != nil {
		return err
	}
	var briefBody string
	if briefPath != "" {
		b, err := os.ReadFile(briefPath)
		if err != nil {
			return err
		}
		briefBody = string(b)
	}

	prompt := fmt.Sprintf(`Read the research brief below, then create a detailed implementation plan for: %s

The plan MUST include:
1. Files to modify (explicit paths, with reason for each)
2. Data models (schemas, types, API shapes)
3. Edge cases and failure modes (what can go wrong, how each is handled)
4. Testing strategy (unit, integration, what's mocked vs real)

If the brief is empty, proceed with the description alone but note that assumption explicitly.

---
RESEARCH BRIEF:
%s
---
`, desc, briefBody)

	sigs := signals.Extract("plan", args, proj)
	resp, picked, err := sendWithFallback(a, context.Background(),
		router.Request{Verb: "plan", Args: args, Signals: sigs},
		channel.Request{Prompt: prompt, WorkingDir: proj.Path})
	if err != nil {
		return err
	}

	subDirPlans := proj.PlansDir
	if subDirPlans == "" {
		subDirPlans = "styx/plans"
	}
	out, err := brief.WritePlan(brief.WriteOpts{
		ProjectPath: proj.Path,
		SubDir:      subDirPlans,
		Query:       desc,
		Body:        resp.Text,
		Now:         time.Now(),
	})
	if err != nil {
		return err
	}
	rel, _ := filepath.Rel(proj.Path, out)
	fmt.Printf("✓ Plan saved: %s\n", rel)
	fmt.Fprintf(os.Stderr, "[styx] channel=%s:%s\n", picked.Channel, picked.Model)
	return nil
}
```

- [ ] **Step 2: Implement cmdBuild**

Overwrite `cmd/styx/build.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func cmdBuild(a *app, args []string) error {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	proj, err := resolveTarget(target)
	if err != nil {
		return err
	}

	sigs := signals.Extract("build", args, proj)
	dec, err := a.router.Route(context.Background(), router.Request{Verb: "build", Args: args, Signals: sigs})
	if err != nil {
		return err
	}
	ch, ok := a.channels[dec.Channel]
	if !ok {
		return fmt.Errorf("unknown channel %q for build", dec.Channel)
	}
	fmt.Fprintf(os.Stderr, "[styx] -> %s (%s:%s)\n", proj.Path, dec.Channel, dec.Model)
	_, err = ch.Send(context.Background(), channel.Request{
		Model:       dec.Model,
		Interactive: true,
		WorkingDir:  proj.Path,
	})
	return err
}
```

- [ ] **Step 3: Build**

Run: `go build -o ./bin/styx ./cmd/styx`
Expected: success.

- [ ] **Step 4: Smoke test plan**

From this repo:
```bash
./bin/styx plan "add a stub /health endpoint that returns OK"
```
Expected: file appears at `styx/plans/<ts>-plan.md`.

- [ ] **Step 5: Smoke test build (interactive — Ctrl-C to exit)**

Run: `./bin/styx build` (no arg → current project)
Expected: launches `claude` (or codex on fallback) in `cwd`.

- [ ] **Step 6: Commit**

```bash
git add cmd/styx/plan.go cmd/styx/build.go
git commit -m "feat(cmd): plan loads latest brief, build launches interactive channel"
```

---

### Task 25: review verb (parallel + synthesize)

**Files:**
- Modify: `cmd/styx/review.go`

- [ ] **Step 1: Implement cmdReview**

Overwrite `cmd/styx/review.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/ishaanbatra/styx/internal/budget"
	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
)

func cmdReview(a *app, args []string) error {
	proj, err := project.Current()
	if err != nil {
		return err
	}
	diff, err := branchDiff(proj.Path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		return fmt.Errorf("no diff between current branch and main; nothing to review")
	}

	ctx := context.Background()
	dec, err := a.router.Route(ctx, router.Request{Verb: "review", Args: args})
	if err != nil {
		return err
	}
	if !dec.Parallel {
		return fmt.Errorf("review verb requires a parallel rule in routing.toml (got Channel=%s)", dec.Channel)
	}

	type result struct {
		Target router.ChannelModel
		Text   string
		Err    error
	}
	results := make([]result, len(dec.ParallelTargets))
	var wg sync.WaitGroup
	for i, t := range dec.ParallelTargets {
		i, t := i, t
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, ok := a.channels[t.Channel]
			if !ok {
				results[i] = result{Target: t, Err: fmt.Errorf("unknown channel %s", t.Channel)}
				return
			}
			prompt := fmt.Sprintf("You are reviewing a git diff. Identify bugs, security issues, regressions, missing tests, and architectural concerns. Be specific (file:line). Group findings as BLOCKING / IMPORTANT / NIT.\n\n--- DIFF ---\n%s\n", diff)
			resp, err := ch.Send(ctx, channel.Request{Model: t.Model, Prompt: prompt})
			_ = a.tracker.Record(ctx, budget.Event{
				Channel:   t.Channel,
				Verb:      "review",
				TokensIn:  resp.EstTokensIn,
				TokensOut: resp.EstTokensOut,
				Success:   err == nil,
				ErrorKind: errorKindOf(err),
			})
			results[i] = result{Target: t, Text: resp.Text, Err: err}
		}()
	}
	wg.Wait()

	var b strings.Builder
	for _, r := range results {
		if r.Err != nil {
			fmt.Fprintf(&b, "## %s:%s (FAILED)\n%v\n\n", r.Target.Channel, r.Target.Model, r.Err)
			continue
		}
		fmt.Fprintf(&b, "## %s:%s\n\n%s\n\n", r.Target.Channel, r.Target.Model, r.Text)
	}

	// Synthesize
	synth, ok := a.channels[dec.SynthesizeWith.Channel]
	if !ok {
		return fmt.Errorf("synthesize channel %q not registered", dec.SynthesizeWith.Channel)
	}
	synthResp, err := synth.Send(ctx, channel.Request{
		Model:  dec.SynthesizeWith.Model,
		Prompt: "Merge the following independent reviews into a single deduplicated report grouped by severity (BLOCKING / IMPORTANT / NIT). Keep specific file:line citations.\n\n" + b.String(),
	})
	if err != nil {
		return err
	}
	fmt.Println(synthResp.Text)
	fmt.Fprintf(os.Stderr, "[styx] synthesized by %s:%s\n", dec.SynthesizeWith.Channel, dec.SynthesizeWith.Model)
	return nil
}

func branchDiff(projectPath string) (string, error) {
	cmd := exec.Command("git", "diff", "main...HEAD")
	cmd.Dir = projectPath
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git diff main...HEAD: %w", err)
	}
	return string(out), nil
}
```

- [ ] **Step 2: Build**

Run: `go build -o ./bin/styx ./cmd/styx`
Expected: success.

- [ ] **Step 3: Manual smoke (in a branch with diff)**

Run from a repo with a non-main branch that has changes:
```bash
./bin/styx review
```
Expected: two parallel reviews complete, then a synthesized output.

- [ ] **Step 4: Commit**

```bash
git add cmd/styx/review.go
git commit -m "feat(cmd): review verb runs parallel channels and synthesizes by severity"
```

---

### Task 26: check + budget + route + project commands

**Files:**
- Modify: `cmd/styx/check.go`
- Modify: `cmd/styx/budget.go`
- Modify: `cmd/styx/route.go`
- Modify: `cmd/styx/project.go`

- [ ] **Step 1: Implement cmdCheck**

Overwrite `cmd/styx/check.go`:
```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/brief"
	"github.com/ishaanbatra/styx/internal/project"
)

func cmdCheck(args []string) error {
	projs, err := project.List()
	if err != nil {
		return err
	}
	for _, p := range projs {
		fmt.Printf("── %s ──\n", p.Name)
		if _, err := os.Stat(filepath.Join(p.Path, ".git")); err == nil {
			branchCmd := exec.Command("git", "branch", "--show-current")
			branchCmd.Dir = p.Path
			branch, _ := branchCmd.Output()
			fmt.Printf("  branch: %s", branch)
			statusCmd := exec.Command("git", "status", "--short")
			statusCmd.Dir = p.Path
			st, _ := statusCmd.Output()
			s := strings.TrimSpace(string(st))
			if s == "" {
				fmt.Println("  status: clean")
			} else {
				fmt.Println("  status:")
				for _, line := range strings.Split(s, "\n") {
					fmt.Println("    " + line)
				}
			}
		} else {
			fmt.Printf("  (not a git repo: %s)\n", p.Path)
		}
		researchDir := p.ResearchDir
		if researchDir == "" {
			researchDir = "styx/research"
		}
		latest, _ := brief.LoadLatest(filepath.Join(p.Path, researchDir))
		if latest != "" {
			rel, _ := filepath.Rel(p.Path, latest)
			fmt.Printf("  latest brief: %s\n", rel)
		}
		fmt.Println()
	}
	return nil
}
```

- [ ] **Step 2: Implement cmdBudget**

Overwrite `cmd/styx/budget.go`:
```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/ishaanbatra/styx/internal/budget"
)

func cmdBudget(args []string) error {
	tr, err := budget.Default()
	if err != nil {
		return err
	}
	defer tr.Close()
	ctx := context.Background()
	for _, ch := range []string{"claude", "codex", "gemini", "gemini_paid", "gemini_free", "ollama"} {
		st, err := tr.State(ctx, ch)
		if err != nil {
			fmt.Printf("%-12s  error: %v\n", ch, err)
			continue
		}
		cooldown := ""
		if !st.CooldownUntil.IsZero() {
			cooldown = fmt.Sprintf(" (cooldown until %s)", st.CooldownUntil.Format(time.RFC3339))
		}
		fmt.Printf("%-12s  used %5.1f%%  window=%s%s\n", ch, st.UsedPct, st.Window, cooldown)
	}
	return nil
}
```

- [ ] **Step 3: Implement cmdRoute (--explain only)**

Overwrite `cmd/styx/route.go`:
```go
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/ishaanbatra/styx/internal/project"
	"github.com/ishaanbatra/styx/internal/router"
	"github.com/ishaanbatra/styx/internal/signals"
)

func cmdRoute(args []string) error {
	if len(args) < 2 || args[0] != "--explain" {
		return fmt.Errorf("usage: styx route --explain <verb> \"<text>\"")
	}
	verb := args[1]
	text := strings.Join(args[2:], " ")
	a, err := loadApp()
	if err != nil {
		return err
	}
	defer a.tracker.Close()
	proj, _ := project.Current()
	sigs := signals.Extract(verb, []string{text}, proj)
	fmt.Print(a.router.Explain(context.Background(), router.Request{Verb: verb, Args: []string{text}, Signals: sigs}))
	return nil
}
```

- [ ] **Step 4: Implement cmdProject**

Overwrite `cmd/styx/project.go`:
```go
package main

import (
	"fmt"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/project"
)

func cmdProject(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: styx project ls|add|rm|rename")
	}
	switch args[0] {
	case "ls":
		projs, err := project.List()
		if err != nil {
			return err
		}
		for _, p := range projs {
			fmt.Printf("%-20s %s\n", p.Name, p.Path)
		}
		return nil
	case "add":
		if len(args) < 3 {
			return fmt.Errorf("usage: styx project add <name> <path>")
		}
		return project.Register(config.Project{Name: args[1], Path: args[2], Language: project.SniffLanguage(args[2])})
	case "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: styx project rm <name>")
		}
		return project.Forget(args[1])
	case "rename":
		if len(args) < 3 {
			return fmt.Errorf("usage: styx project rename <old> <new>")
		}
		p, err := project.Resolve(args[1])
		if err != nil {
			return err
		}
		if err := project.Forget(args[1]); err != nil {
			return err
		}
		p.Name = args[2]
		return project.Register(p)
	}
	return fmt.Errorf("unknown project subcommand %q", args[0])
}
```

- [ ] **Step 5: Build**

Run: `go build -o ./bin/styx ./cmd/styx`
Expected: success.

- [ ] **Step 6: Smoke each new admin verb**

Run:
```bash
./bin/styx project ls
./bin/styx budget
./bin/styx route --explain plan "refactor the auth middleware"
./bin/styx check
```
Expected: each prints sensible output without errors. `route --explain` should show `claude:opus-4-7` (because "refactor" triggers `complex`).

- [ ] **Step 7: Commit**

```bash
git add cmd/styx/check.go cmd/styx/budget.go cmd/styx/route.go cmd/styx/project.go
git commit -m "feat(cmd): check/budget/route/project administrative subcommands"
```

---

### Task 27: migrate-secrets command

**Files:**
- Modify: `cmd/styx/migrate_secrets.go`

- [ ] **Step 1: Implement cmdMigrateSecrets**

Overwrite `cmd/styx/migrate_secrets.go`:
```go
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
)

var secretShapedRE = regexp.MustCompile(`^export\s+([A-Z][A-Z0-9_]*(?:_API_KEY|_TOKEN|_SECRET))="?([^"]+)"?\s*$`)

func cmdMigrateSecrets() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	files := []string{".zshrc", ".bashrc", ".bash_profile", ".zprofile"}
	moved := 0
	for _, f := range files {
		path := filepath.Join(home, f)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		n, err := migrateOne(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s: %v\n", path, err)
			continue
		}
		moved += n
	}
	fmt.Printf("Migrated %d secret(s) to the macOS Keychain (service=styx).\n", moved)
	if moved > 0 {
		fmt.Println("Open a new shell so the commented-out exports take effect.")
	}
	return nil
}

func migrateOne(path string) (int, error) {
	in, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	var out strings.Builder
	moved := 0
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		m := secretShapedRE.FindStringSubmatch(line)
		if m == nil {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		name := strings.ToLower(m[1])
		value := m[2]
		fmt.Printf("%s -> %s — move to Keychain? [Y/n] ", path, name)
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans == "" || ans == "y" || ans == "yes" {
			if err := config.SetSecret(name, value); err != nil {
				return moved, err
			}
			out.WriteString("# moved to Keychain by styx migrate-secrets\n# " + line + "\n")
			moved++
		} else {
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return moved, err
	}
	if moved == 0 {
		return 0, nil
	}
	return moved, os.WriteFile(path, []byte(out.String()), 0o644)
}
```

- [ ] **Step 2: Build**

Run: `go build -o ./bin/styx ./cmd/styx`
Expected: success.

- [ ] **Step 3: Manual run**

Run: `./bin/styx migrate-secrets`
Expected: prompts for each secret-shaped env var; on accept, writes to Keychain and comments the line.

- [ ] **Step 4: Verify Keychain entry**

Run: `security find-generic-password -s styx -a gemini_api_key -w | head -c 8`
Expected: prints the first 8 characters of the API key (i.e., it's now in Keychain).

- [ ] **Step 5: Commit**

```bash
git add cmd/styx/migrate_secrets.go
git commit -m "feat(cmd): migrate-secrets moves env-var secrets to macOS Keychain"
```

---

## Phase 11 — Install + final smoke (Task 28)

### Task 28: Install + manual end-to-end smoke

**Files:**
- Create: `install.sh`
- Modify: `README.md`

- [ ] **Step 1: Write install.sh**

Create `install.sh`:
```bash
#!/usr/bin/env bash
# Build and install styx to ~/bin/styx, backing up any prior install.
set -euo pipefail

cd "$(dirname "$0")"

BIN_DIR="$HOME/bin"
BIN="$BIN_DIR/styx"

go build -o ./bin/styx ./cmd/styx

mkdir -p "$BIN_DIR"
if [ -e "$BIN" ] && [ ! -L "$BIN" ]; then
  mv "$BIN" "$BIN.old.bak"
  echo "Backed up existing styx -> $BIN.old.bak"
fi
cp ./bin/styx "$BIN"
chmod 755 "$BIN"
echo "Installed -> $BIN"

case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "NOTE: $BIN_DIR is not in PATH. Add: export PATH=\"\$HOME/bin:\$PATH\"";;
esac
```

- [ ] **Step 2: Make it executable**

Run:
```bash
chmod +x install.sh
```

- [ ] **Step 3: Update README**

Overwrite `README.md`:
```markdown
# Styx

Personal multi-model dev orchestration CLI. Routes work between Claude, Codex,
Gemini-CLI, and Ollama based on a hand-curated rules table.

See `docs/superpowers/specs/2026-05-26-styx-v2-design.md` for design.

## Install (one shot)

    ./install.sh        # builds + drops binary at ~/bin/styx (backs up any existing one)

Then migrate any plaintext secrets out of your shell rc:

    styx migrate-secrets

## Build manually

    make build       # produces ./bin/styx
    make test        # runs all tests
    make install     # same as install.sh

## Verbs

| Verb           | Purpose                                                   |
|----------------|-----------------------------------------------------------|
| research <q>   | Gemini draft + Codex critique → brief in research_dir     |
| deep-research  | Open Gemini + ChatGPT in browser, write synthesis template |
| plan <desc>    | Use latest brief to draft a detailed implementation plan  |
| build [target] | Launch interactive Claude (or Codex on fallback) in repo  |
| review         | Parallel multi-channel review of `git diff main...HEAD`   |
| grunt <prompt> | Local Ollama pass-through                                 |
| think <prompt> | Local Ollama reasoning mode (`deep:` prefix → Claude)     |
| explain ...    | Explain code files                                        |
| summarize ...  | Summarize a set of files                                  |
| critique ...   | Devil's-advocate critique (Codex)                         |
| check          | Dashboard: git status, Ollama, latest briefs/plans        |
| budget         | Per-channel usage summary                                 |
| route --explain <verb> "..." | Why did styx pick that channel?             |
| project ls/add/rm/rename     | Manage project registry                      |
| migrate-secrets              | Move env-var secrets to macOS Keychain       |

## Configuration

- `~/.config/styx/routing.toml` — routing rules + budget caps (you edit this)
- `~/.config/styx/projects.toml` — registered projects (auto-managed)
- `~/.config/styx/state/usage.db` — append-only sqlite usage log
- Secrets live in macOS Keychain under service `styx`.
```

- [ ] **Step 4: Full-tree build + test**

Run from the repo root:
```bash
go vet ./...
go test ./...
go build -o ./bin/styx ./cmd/styx
```
Expected: vet clean; all unit tests PASS; build succeeds.

- [ ] **Step 5: Install**

Run: `./install.sh`
Expected: prints `Installed -> /Users/ishaanbatra/bin/styx` and (if applicable) `Backed up existing styx -> ...old.bak`.

- [ ] **Step 6: End-to-end smoke checklist**

Verify each manually. Each line should succeed (or fail in the documented manner):

```bash
# 1. Help text
styx help

# 2. First-run config seeded
ls ~/.config/styx/routing.toml ~/.config/styx/projects.toml

# 3. Project auto-registration (from one of your existing repos)
cd ~/Documents/GitHub/ai-ta-backend
styx project ls                     # should now include hoot-backend (or "ai-ta-backend" if not yet renamed)

# 4. Routing explain
styx route --explain plan "refactor the embedding layer"
# Expected: claude:opus-4-7 because "refactor" → complex signal

# 5. Local ollama grunt (Ollama must be running)
styx grunt "Reply with the single word: pong"

# 6. Cloud-routed plan (requires claude CLI auth)
styx plan "test plan request — just describe what styx is"
ls ~/Documents/GitHub/ai-ta-backend/docs/plans/   # new plan file present

# 7. Budget summary
styx budget                          # claude shows non-zero used after step 6

# 8. Secrets migration (only if not already done)
styx migrate-secrets
security find-generic-password -s styx -a gemini_api_key -w >/dev/null && echo "OK: gemini key in Keychain"

# 9. Verify old script is backed up
ls ~/bin/styx.old.bak                # or `not present` if there was none originally
```

- [ ] **Step 7: Commit installer + README**

```bash
git add install.sh README.md
git commit -m "feat: install.sh + README with verb table and config docs"
```

- [ ] **Step 8: Tag v0.1.0**

```bash
git tag v0.1.0
git log --oneline | head -30
```
Expected: `v0.1.0` tag at latest commit; commit log shows ~28 atomic feature commits.

---

## Self-review (executed when the plan was written)

### Spec coverage check

| Spec section | Covered by task(s) |
|---|---|
| §2 goal 1 (global, not Hoot-scoped) | T11 (project discovery + auto-register) |
| §2 goal 2 (leverage every channel) | T13, T17, T18, T19 |
| §2 goal 3 (curate-by-rules-table) | T6 (loader), T15 (router), T26 (route --explain) |
| §2 goal 4 (never block on capped channel) | T15 (fallback), T21 (sendWithFallback), T9 (cooldown) |
| §2 goal 5 (zero setup in new projects) | T11 (auto-register), T20 (first-run routing.toml) |
| §2 goal 6 (preserve Hoot workflow) | T11 (per-project research/plans dirs), T22 (research uses ResearchDir) |
| §3 non-goal: NL entrypoint | not implemented (correct) |
| §3 non-goal: persistent memory | not implemented (correct) |
| §3 non-goal: daemon | not implemented (correct) |
| §6.1 dispatcher | T20 |
| §6.1 compound verbs | T22 (research issues research + research.critic) |
| §6.2 Router / Decision / Explain | T15 |
| §6.3 Channel interface + 4 impls | T12, T13, T17, T18, T19 |
| §6.4 Project + walk-up + auto-register | T11 |
| §6.5 Budget tracker (sqlite) | T8 + T9 |
| §6.6 brief writer with per-project dirs | T16 + T22 + T24 |
| §6.7 Config + Keychain | T6, T7, T5 |
| §6.8 Signal extractor | T14 |
| §7.1 file layout (XDG) | T4 |
| §7.2 default routing.toml | T20 (defaultRoutingTOML) |
| §7.3 projects.toml entries | T7 (atomic save) + T11 (auto-write) |
| §7.4 per-repo `.styx.toml` override | **GAP** — see "Open follow-ups" below |
| §8 migrate-secrets | T27 |
| §9 worked example | covered by T21+T24 path |
| §10 error handling table | T15 (default rule), T13/T17/T18/T19 (ClassifiedError), T9 (circuit breaker), T21 (fallback walk) |
| §11 testing (unit + contract + e2e golden) | unit + contract covered; e2e golden tests deferred to a follow-up task |
| §12 in-place migration | T28 install.sh moves old → .bak |

### Open follow-ups (not blocking v0.1.0)

1. **`.styx.toml` per-repo override** (spec §7.4). Architecture supports it (per-project config), but it's not yet wired in. Add a follow-up task when the user wants a repo-blocks-cloud capability.
2. **Golden-file e2e scenarios in `testdata/scenarios/`** (spec §11). The dispatcher + handlers are testable through fake channels; defer until the v0.1.0 surface is settled.
3. **gemini-cli auth flow** (spec §13 open question). The current implementation prefers `gemini-cli` if on PATH; whether `gemini-cli` uses your $20 sub depends on its own auth state. Verify manually after install: `gemini auth login` or equivalent.

### Placeholder scan
- No "TBD" or "implement later" in any step.
- No "add appropriate error handling" hand-waves — every error path has explicit code.
- Every step that changes code shows the code.

### Type consistency
- `Project` defined in T7 (`config.Project`); aliased in T11 (`project.Project = config.Project`); used unchanged in T14, T16, T22-T26.
- `Channel` / `Request` / `Response` / `Budget` / `ClassifiedError` defined in T12; consumed unchanged in T13, T17, T18, T19, T21-T25.
- `Decision` / `Request` / `ChannelModel` defined in T15; consumed unchanged in T21-T25.
- `Tracker` / `Event` / `State` defined in T8; consumed unchanged in T9, T20, T21, T25, T26.
- `Routing` / `Rule` / `BudgetCaps` / `ChannelCap` defined in T6; consumed unchanged in T15, T20.

No naming drift found.

---

## How to execute this plan

See top of file for the recommended sub-skill: **superpowers:subagent-driven-development**.

Each task is independently committable. The phases create natural checkpoint commits where you can stop and pick up later:

- **End of Phase 4** (Task 9): foundation + config + budget compile and test.
- **End of Phase 6** (Task 13): Ollama works; you can already call `ollama.Send` from a script.
- **End of Phase 7** (Task 15): the router is testable in isolation.
- **End of Phase 9** (Task 19): all four channels work; the router has real channels behind it.
- **End of Phase 10** (Task 27): every verb works; `styx help` shows the full menu.
- **End of Phase 11** (Task 28): installed, smoked, tagged v0.1.0.

