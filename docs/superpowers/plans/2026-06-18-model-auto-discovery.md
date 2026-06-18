# Model Auto-Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop pinning model versions (defer to each CLI's latest) and add a user-controlled, pass-through reasoning-effort field per routing rule.

**Architecture:** Two axes. (1) *Version → latest*: codex sends no `--model`; claude routes by alias. A one-time migration in a new `internal/modelsync` package de-pins legacy `routing.toml` tokens, runs from `styx doctor` and a staleness check in `loadApp()`. (2) *Effort → user-controlled*: a new optional `effort` field on each routing rule flows `Rule.Effort → Decision.Effort → channel.Request.Effort` into the codex (`-c model_reasoning_effort=`) and claude (`--effort`) adapters.

**Tech Stack:** Go, `modernc.org/sqlite` (memory), `github.com/BurntSushi/toml` (config), table-driven `t.Run` tests, fakes over mocks.

## Global Constraints

- Pure Go, no cgo; SQLite via `modernc.org/sqlite` only — do not add drivers.
- Channels are CLIs/HTTP, never SDKs. Never call provider HTTP APIs for claude/codex/agy.
- File writes are atomic (tmp + rename); use `internal/paths` helpers for locations.
- Never swallow errors with `x, _ :=`; wrap with `fmt.Errorf("...: %w", err)`.
- Every subprocess/HTTP call runs under a `context` with timeout.
- Effort is a **pass-through string** — never validate it against an enum.
- Drift contract: after editing a `.go` file, update `docs/ARCHITECTURE.md` and bump its `last_verified` in the same commit. Editing `default_routing.go` requires keeping the live `routing.toml` upgrade path in mind.
- `go vet ./... && gofmt -w .` before every commit.

---

### Task 1: `effort` + `[models]` config fields

**Files:**
- Modify: `internal/config/routing.go`
- Test: `internal/config/routing_test.go`

**Interfaces:**
- Produces: `config.Rule.Effort string`; `config.ModelsConfig{RefreshIntervalHours int}`; `config.Routing.Models ModelsConfig` (defaulted to 24).

- [ ] **Step 1: Write the failing test**

Add to `internal/config/routing_test.go`:

```go
func TestLoadRouting_EffortAndModels(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(path, []byte(`
[[rule]]
verb = "research.critic"
use  = "codex"
effort = "high"

[models]
refresh_interval_hours = 12
`), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := config.LoadRoutingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rules[0].Effort != "high" {
		t.Errorf("Effort = %q, want high", r.Rules[0].Effort)
	}
	if r.Models.RefreshIntervalHours != 12 {
		t.Errorf("RefreshIntervalHours = %d, want 12", r.Models.RefreshIntervalHours)
	}
}

func TestLoadRouting_ModelsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(path, []byte("[[rule]]\nverb=\"plan\"\nuse=\"claude:opus\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := config.LoadRoutingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Models.RefreshIntervalHours != 24 {
		t.Errorf("default RefreshIntervalHours = %d, want 24", r.Models.RefreshIntervalHours)
	}
}
```

Ensure the test file imports `os`, `path/filepath`, `testing`, and the `config` package.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadRouting_Effort -v`
Expected: FAIL (compile error: `Effort`/`Models` undefined).

- [ ] **Step 3: Implement**

In `internal/config/routing.go`, add `Effort` to `Rule`:

```go
type Rule struct {
	Verb           string   `toml:"verb"`
	Signals        []string `toml:"signals"`
	Use            string   `toml:"use"`
	Parallel       []string `toml:"parallel"`
	SynthesizeWith string   `toml:"synthesize_with"`
	Fallback       []string `toml:"fallback"`
	Effort         string   `toml:"effort"` // optional reasoning-effort, pass-through to the CLI
}
```

Add the config type and field to `Routing`:

```go
// ModelsConfig controls model auto-discovery / staleness refresh.
type ModelsConfig struct {
	RefreshIntervalHours int `toml:"refresh_interval_hours"`
}
```

Add `Models ModelsConfig \`toml:"models"\`` to the `Routing` struct. Then extend the existing defaults application (where `applyBrainDefaults` is called inside `LoadRoutingFile`) with:

```go
func applyModelsDefaults(r *Routing) {
	if r.Models.RefreshIntervalHours == 0 {
		r.Models.RefreshIntervalHours = 24
	}
}
```

Call `applyModelsDefaults(&r)` next to the existing `applyBrainDefaults(&r)` call in `LoadRoutingFile`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestLoadRouting -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/config/ && go vet ./internal/config/
git add internal/config/
git commit -m "feat(config): per-rule effort field + [models] refresh interval"
```

---

### Task 2: router carries effort + bare-channel parsing

**Files:**
- Modify: `internal/router/router.go`
- Test: `internal/router/router_test.go`

**Interfaces:**
- Consumes: `config.Rule.Effort` (Task 1).
- Produces: `router.Decision.Effort string`; `parseChannelModel("codex")` → `{Channel:"codex", Model:""}` (no error).

- [ ] **Step 1: Write the failing test**

Add to `internal/router/router_test.go`:

```go
func TestParseChannelModel_BareChannel(t *testing.T) {
	cm, err := parseChannelModel("codex")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cm.Channel != "codex" || cm.Model != "" {
		t.Errorf("got %+v, want {codex }", cm)
	}
}
```

Also add a routing test that asserts `Decision.Effort` is populated. Follow the existing router_test setup for building a `Router` from rules; add a rule `{Verb: "plan", Use: "codex", Effort: "high"}` and assert `dec.Effort == "high"` after `Route` with `Request{Verb: "plan"}`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/router/ -run TestParseChannelModel_BareChannel -v`
Expected: FAIL ("invalid channel:model").

- [ ] **Step 3: Implement**

In `internal/router/router.go`, make `parseChannelModel` tolerate a missing colon:

```go
func parseChannelModel(s string) (ChannelModel, error) {
	idx := strings.Index(s, ":")
	if idx < 0 {
		if s == "" {
			return ChannelModel{}, fmt.Errorf("empty channel:model")
		}
		return ChannelModel{Channel: s, Model: ""}, nil
	}
	return ChannelModel{Channel: s[:idx], Model: s[idx+1:]}, nil
}
```

Add `Effort string` to the `Decision` struct. In `Route`, set `Effort: rule.Effort` on every `Decision` value returned (both the parallel-rule branch and the single-`use` branch). The matched `rule` is already in scope in both branches.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/router/ -v`
Expected: PASS (all router tests).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/router/ && go vet ./internal/router/
git add internal/router/
git commit -m "feat(router): Decision.Effort + bare-channel (deferred-model) parsing"
```

---

### Task 3: `channel.Request.Effort` + codex adapter

**Files:**
- Modify: `internal/channel/channel.go`
- Modify: `internal/channel/codex/codex.go`
- Test: `internal/channel/codex/codex_test.go`

**Interfaces:**
- Produces: `channel.Request.Effort string`. codex `sendOneShot` appends `-c model_reasoning_effort=<effort>` when `Effort != ""`; never appends `--model` (already gated on `Model != ""`, and routing now passes `Model == ""`).

- [ ] **Step 1: Write the failing test**

Codex's adapter shells out to a real `codex` binary, so test the argv it builds. If `codex_test.go` already factors arg-building, assert on it; otherwise add a small pure helper. Add this helper to `codex.go` and test it:

```go
// codexArgs builds the exec argv (excluding the binary name) for req.
func codexArgs(req channel.Request) []string {
	args := []string{}
	if req.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+req.Effort)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, "exec")
	if req.Write {
		args = append(args, "--sandbox", "workspace-write")
	}
	args = append(args, req.Prompt)
	return args
}
```

Test in `codex_test.go`:

```go
func TestCodexArgs_EffortNoModel(t *testing.T) {
	got := codexArgs(channel.Request{Prompt: "hi", Effort: "high"})
	want := []string{"-c", "model_reasoning_effort=high", "exec", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}

func TestCodexArgs_NoModelByDefault(t *testing.T) {
	got := codexArgs(channel.Request{Prompt: "hi"})
	for _, a := range got {
		if a == "--model" {
			t.Errorf("unexpected --model in %v", got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/channel/codex/ -run TestCodexArgs -v`
Expected: FAIL (compile error: `Effort` / `codexArgs` undefined).

- [ ] **Step 3: Implement**

In `internal/channel/channel.go`, add to `Request`:

```go
	Effort string // optional reasoning-effort, pass-through to the channel CLI
```

In `internal/channel/codex/codex.go`, add the `codexArgs` helper above and refactor `sendOneShot` to use it:

```go
func (c *Channel) sendOneShot(ctx context.Context, req channel.Request) (channel.Response, error) {
	cmd := exec.CommandContext(ctx, "codex", codexArgs(req)...)
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
```

Add `"reflect"` to the codex test imports.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/channel/... -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/channel/ && go vet ./internal/channel/...
git add internal/channel/
git commit -m "feat(channel): Request.Effort; codex passes model_reasoning_effort, defers --model"
```

---

### Task 4: claude adapter honors effort

**Files:**
- Modify: `internal/channel/claude/claude.go`
- Test: `internal/channel/claude/claude_test.go`

**Interfaces:**
- Consumes: `channel.Request.Effort` (Task 3).
- Produces: claude `sendOneShot` appends `--effort <effort>` when `Effort != ""`.

- [ ] **Step 1: Write the failing test**

Mirror Task 3's argv approach. Add a `claudeArgs(req channel.Request) []string` helper to `claude.go` and test it:

```go
func TestClaudeArgs_Effort(t *testing.T) {
	got := claudeArgs(channel.Request{Prompt: "hi", Model: "opus", Effort: "ultracode"})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--effort ultracode") {
		t.Errorf("missing --effort ultracode in %v", got)
	}
	if !strings.Contains(joined, "--model opus") {
		t.Errorf("missing --model opus in %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/channel/claude/ -run TestClaudeArgs -v`
Expected: FAIL (compile error: `claudeArgs` undefined).

- [ ] **Step 3: Implement**

In `internal/channel/claude/claude.go`, extract arg-building into `claudeArgs` and call it from `sendOneShot`:

```go
func claudeArgs(req channel.Request) []string {
	args := []string{"-p", req.Prompt}
	if req.Write {
		args = append([]string{"--dangerously-skip-permissions"}, args...)
	}
	if req.Model != "" {
		args = append([]string{"--model", req.Model}, args...)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	return args
}
```

Replace the inline arg construction in `sendOneShot` with `cmd := exec.CommandContext(ctx, c.binary(), claudeArgs(req)...)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/channel/claude/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/channel/claude/ && go vet ./internal/channel/claude/
git add internal/channel/claude/
git commit -m "feat(channel): claude passes --effort when set"
```

---

### Task 5: thread effort through the research pipeline

**Files:**
- Modify: `cmd/styx/research.go`
- Test: `cmd/styx/research_test.go` (create if absent)

**Interfaces:**
- Consumes: `router.Decision.Effort` (Task 2), `channel.Request.Effort` (Task 3).
- Produces: `channelAdapter.effort string`, set into `channel.Request.Effort` on `Send`.

- [ ] **Step 1: Write the failing test**

Add a focused test of the adapter wiring. Use a fake `channel.Channel` that records the last `Request`:

```go
type recordingChannel struct{ last channel.Request }

func (r *recordingChannel) Name() string { return "rec" }
func (r *recordingChannel) BudgetState(context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}
func (r *recordingChannel) Send(_ context.Context, req channel.Request) (channel.Response, error) {
	r.last = req
	return channel.Response{Text: "ok"}, nil
}

func TestChannelAdapter_PassesEffort(t *testing.T) {
	rec := &recordingChannel{}
	a := &channelAdapter{ch: rec, model: "", effort: "high"}
	if _, err := a.Send(context.Background(), "draft this"); err != nil {
		t.Fatal(err)
	}
	if rec.last.Effort != "high" {
		t.Errorf("Effort = %q, want high", rec.last.Effort)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestChannelAdapter_PassesEffort -v`
Expected: FAIL (compile error: `effort` field undefined).

- [ ] **Step 3: Implement**

In `cmd/styx/research.go`, add `effort string` to `channelAdapter` and set `Effort` in `Send`:

```go
type channelAdapter struct {
	ch          channel.Channel
	model       string
	effort      string
	projectPath string
}

func (a *channelAdapter) Send(ctx context.Context, prompt string) (string, error) {
	resp, err := a.ch.Send(ctx, channel.Request{
		Model:      a.model,
		Effort:     a.effort,
		Prompt:     prompt,
		WorkingDir: a.projectPath,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}
```

Then set `effort` where the adapters are constructed (the lines that build `drafter` and `critic`):

```go
	drafter := &channelAdapter{ch: rawChannel(drafterCh), model: drafterDec.Model, effort: drafterDec.Effort, projectPath: proj.Path}
	critic := &channelAdapter{ch: rawChannel(criticCh), model: criticDec.Model, effort: criticDec.Effort}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/styx/ -run TestChannelAdapter_PassesEffort -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w cmd/styx/ && go vet ./cmd/styx/
git add cmd/styx/research.go cmd/styx/research_test.go
git commit -m "feat(research): carry rule effort into drafter/critic dispatches"
```

---

### Task 6: `paths.ModelsCachePath()`

**Files:**
- Modify: `internal/paths/paths.go`
- Test: `internal/paths/paths_test.go`

**Interfaces:**
- Produces: `paths.ModelsCachePath() (string, error)` → `<ConfigDir>/models.json`.

- [ ] **Step 1: Write the failing test**

```go
func TestModelsCachePath(t *testing.T) {
	p, err := ModelsCachePath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "models.json" {
		t.Errorf("base = %q, want models.json", filepath.Base(p))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/paths/ -run TestModelsCachePath -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

```go
// ModelsCachePath is where the model-discovery cache (models.json) lives.
func ModelsCachePath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "models.json"), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/paths/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/paths/ && go vet ./internal/paths/
git add internal/paths/
git commit -m "feat(paths): ModelsCachePath helper"
```

---

### Task 7: `internal/modelsync` discoverers

**Files:**
- Create: `internal/modelsync/modelsync.go`
- Create: `internal/modelsync/discoverers.go`
- Test: `internal/modelsync/discoverers_test.go`

**Interfaces:**
- Produces: `Result{Current, Available []string, Source}`; `Discoverer` interface; `ClaudeDiscoverer{}`; `CodexDiscoverer{ConfigPath string}` (empty = `~/.codex/config.toml`).

- [ ] **Step 1: Write the failing test**

```go
package modelsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeDiscoverer(t *testing.T) {
	r, err := ClaudeDiscoverer{}.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"opus", "sonnet", "haiku", "fable"}
	if len(r.Available) != len(want) {
		t.Fatalf("Available = %v, want %v", r.Available, want)
	}
	if r.Source != "claude-alias" {
		t.Errorf("Source = %q", r.Source)
	}
}

func TestCodexDiscoverer(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte("model = \"gpt-5.5\"\nmodel_reasoning_effort = \"medium\"\n"), 0o644)

	r, err := CodexDiscoverer{ConfigPath: cfg}.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r.Current != "gpt-5.5" || r.Source != "codex-config" {
		t.Errorf("got %+v", r)
	}
}

func TestCodexDiscoverer_MissingFile(t *testing.T) {
	_, err := CodexDiscoverer{ConfigPath: filepath.Join(t.TempDir(), "none.toml")}.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestCodexDiscoverer_NoModelLine(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	os.WriteFile(cfg, []byte("model_reasoning_effort = \"medium\"\n"), 0o644)
	_, err := CodexDiscoverer{ConfigPath: cfg}.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error when no model line present")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/modelsync/ -v`
Expected: FAIL (package/types undefined).

- [ ] **Step 3: Implement**

`internal/modelsync/modelsync.go`:

```go
// Package modelsync keeps styx's routing on each CLI's current models without
// hand-pinning versions: it discovers the model a channel uses now, migrates
// legacy version-pinned routing tokens to the defer-to-latest form, and caches
// the result. Run from `styx doctor` and a staleness check in loadApp().
package modelsync

import "context"

// Result is one channel's discovered model state.
type Result struct {
	Current   string   `json:"current,omitempty"`   // preferred id now ("" if alias-only)
	Available []string `json:"available,omitempty"` // valid ids when enumerable
	Source    string   `json:"source"`              // "codex-config" | "claude-alias"
}

// Discoverer reports the models a channel currently accepts.
type Discoverer interface {
	Channel() string
	Discover(ctx context.Context) (Result, error)
}
```

`internal/modelsync/discoverers.go`:

```go
package modelsync

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ClaudeDiscoverer reports claude's stable class aliases (always latest).
type ClaudeDiscoverer struct{}

func (ClaudeDiscoverer) Channel() string { return "claude" }

func (ClaudeDiscoverer) Discover(context.Context) (Result, error) {
	return Result{
		Available: []string{"opus", "sonnet", "haiku", "fable"},
		Source:    "claude-alias",
	}, nil
}

// CodexDiscoverer reads codex's own current default model from its config.
type CodexDiscoverer struct {
	ConfigPath string // "" => ~/.codex/config.toml
}

func (CodexDiscoverer) Channel() string { return "codex" }

func (d CodexDiscoverer) Discover(context.Context) (Result, error) {
	path := d.ConfigPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Result{}, fmt.Errorf("locate home for codex config: %w", err)
		}
		path = filepath.Join(home, ".codex", "config.toml")
	}
	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open codex config: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") { // left the top-level table
			break
		}
		if v, ok := topLevelModel(line); ok {
			return Result{Current: v, Source: "codex-config"}, nil
		}
	}
	if err := sc.Err(); err != nil {
		return Result{}, fmt.Errorf("read codex config: %w", err)
	}
	return Result{}, fmt.Errorf("no top-level model in %s", path)
}

// topLevelModel parses a `model = "x"` line, returning ("x", true) on match.
func topLevelModel(line string) (string, bool) {
	if !strings.HasPrefix(line, "model") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, "model"))
	if !strings.HasPrefix(rest, "=") {
		return "", false // avoid matching model_reasoning_effort etc.
	}
	val := strings.TrimSpace(strings.TrimPrefix(rest, "="))
	val = strings.Trim(val, "\"'")
	if val == "" {
		return "", false
	}
	return val, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/modelsync/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/modelsync/ && go vet ./internal/modelsync/
git add internal/modelsync/
git commit -m "feat(modelsync): codex-config + claude-alias discoverers"
```

---

### Task 8: model cache (read/write/staleness)

**Files:**
- Create: `internal/modelsync/cache.go`
- Test: `internal/modelsync/cache_test.go`

**Interfaces:**
- Produces: `Cache{RefreshedAt time.Time, Channels map[string]Result}`; `LoadCache(path string) (*Cache, error)` (empty cache if file missing); `(*Cache).Save(path string) error` (atomic); `(*Cache).IsStale(now time.Time, interval time.Duration) bool`.

- [ ] **Step 1: Write the failing test**

```go
func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	c := &Cache{RefreshedAt: now, Channels: map[string]Result{
		"codex": {Current: "gpt-5.5", Source: "codex-config"},
	}}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Channels["codex"].Current != "gpt-5.5" {
		t.Errorf("round-trip lost data: %+v", got)
	}
}

func TestLoadCache_Missing(t *testing.T) {
	c, err := LoadCache(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if c == nil || len(c.Channels) != 0 {
		t.Errorf("want empty cache, got %+v", c)
	}
}

func TestIsStale(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	c := &Cache{RefreshedAt: base}
	if c.IsStale(base.Add(23*time.Hour), 24*time.Hour) {
		t.Error("23h < 24h should not be stale")
	}
	if !c.IsStale(base.Add(25*time.Hour), 24*time.Hour) {
		t.Error("25h > 24h should be stale")
	}
	empty := &Cache{}
	if !empty.IsStale(base, 24*time.Hour) {
		t.Error("zero RefreshedAt should be stale")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/modelsync/ -run 'TestCache|TestLoadCache|TestIsStale' -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`internal/modelsync/cache.go`:

```go
package modelsync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Cache is the persisted result of the last model refresh.
type Cache struct {
	RefreshedAt time.Time         `json:"refreshed_at"`
	Channels    map[string]Result `json:"channels"`
}

// LoadCache reads the cache; a missing file yields an empty (stale) cache.
func LoadCache(path string) (*Cache, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Cache{Channels: map[string]Result{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read model cache: %w", err)
	}
	var c Cache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse model cache: %w", err)
	}
	if c.Channels == nil {
		c.Channels = map[string]Result{}
	}
	return &c, nil
}

// Save writes the cache atomically (tmp + rename).
func (c *Cache) Save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal model cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write model cache tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename model cache: %w", err)
	}
	_ = filepath.Dir(path) // path dir is ensured by callers via paths.EnsureDir
	return nil
}

// IsStale reports whether the cache is older than interval as of now.
func (c *Cache) IsStale(now time.Time, interval time.Duration) bool {
	if c.RefreshedAt.IsZero() {
		return true
	}
	return now.Sub(c.RefreshedAt) > interval
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/modelsync/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/modelsync/ && go vet ./internal/modelsync/
git add internal/modelsync/
git commit -m "feat(modelsync): models.json cache with atomic write + staleness"
```

---

### Task 9: one-time de-pin migration

**Files:**
- Create: `internal/modelsync/migrate.go`
- Test: `internal/modelsync/migrate_test.go`

**Interfaces:**
- Consumes: `Result` (claude `Available`), per-channel discovery output.
- Produces: `MigrateText(src string, claudeAliases []string) (out string, changes []Change)`; `Change{Old, New string}`. Pure string transform — no IO.

- [ ] **Step 1: Write the failing test**

```go
func TestMigrateText(t *testing.T) {
	src := `# keep this comment
[[rule]]
verb = "research.critic"
use  = "codex:gpt-5.5"
fallback = ["claude:opus-4-7", "ollama:qwen2.5-coder:14b"]

[[rule]]
verb = "build"
use  = "claude:interactive"
parallel = ["claude:sonnet-4-6", "codex:gpt-5.5"]
`
	out, changes := MigrateText(src, []string{"opus", "sonnet", "haiku", "fable"})

	if !strings.Contains(out, `use  = "codex"`) {
		t.Errorf("codex not de-pinned:\n%s", out)
	}
	if !strings.Contains(out, `"claude:opus"`) || !strings.Contains(out, `"claude:sonnet"`) {
		t.Errorf("claude not de-pinned to alias:\n%s", out)
	}
	if !strings.Contains(out, "# keep this comment") {
		t.Error("comment not preserved")
	}
	if !strings.Contains(out, `"claude:interactive"`) {
		t.Error("claude:interactive must be left untouched")
	}
	if !strings.Contains(out, "ollama:qwen2.5-coder:14b") {
		t.Error("ollama token must be left untouched")
	}
	if len(changes) == 0 {
		t.Error("expected recorded changes")
	}

	// Idempotent: a second pass makes no change.
	out2, changes2 := MigrateText(out, []string{"opus", "sonnet", "haiku", "fable"})
	if out2 != out || len(changes2) != 0 {
		t.Errorf("migration not idempotent: %d changes", len(changes2))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/modelsync/ -run TestMigrateText -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`internal/modelsync/migrate.go`:

```go
package modelsync

import (
	"regexp"
	"strings"
)

// Change is one token rewrite the migration applied.
type Change struct{ Old, New string }

// tokenRe matches a channel:model token inside quotes or bare in TOML values.
var tokenRe = regexp.MustCompile(`(codex|claude):[A-Za-z0-9._-]+`)

// MigrateText de-pins legacy version-pinned routing tokens:
//   - codex:<version>  -> codex            (defer to codex's own default)
//   - claude:<version> -> claude:<alias>   (prefix-matched class alias)
// codex:interactive, claude:interactive, claude:<alias>, and any non-codex/
// non-claude token are left untouched. Pure transform; idempotent.
func MigrateText(src string, claudeAliases []string) (string, []Change) {
	aliasSet := map[string]bool{}
	for _, a := range claudeAliases {
		aliasSet[a] = true
	}
	var changes []Change
	out := tokenRe.ReplaceAllStringFunc(src, func(tok string) string {
		idx := strings.Index(tok, ":")
		ch, model := tok[:idx], tok[idx+1:]
		if model == "interactive" {
			return tok
		}
		switch ch {
		case "codex":
			changes = append(changes, Change{Old: tok, New: "codex"})
			return "codex"
		case "claude":
			if aliasSet[model] {
				return tok // already an alias
			}
			alias := classAlias(model, claudeAliases)
			if alias == "" {
				return tok // unknown; leave + (caller may warn)
			}
			newTok := "claude:" + alias
			changes = append(changes, Change{Old: tok, New: newTok})
			return newTok
		}
		return tok
	})
	return out, changes
}

// classAlias returns the alias whose name prefixes the pinned model
// (e.g. "opus-4-7" -> "opus"), or "" if none match.
func classAlias(model string, aliases []string) string {
	for _, a := range aliases {
		if strings.HasPrefix(model, a) {
			return a
		}
	}
	return ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/modelsync/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/modelsync/ && go vet ./internal/modelsync/
git add internal/modelsync/
git commit -m "feat(modelsync): idempotent de-pin migration (pure text transform)"
```

---

### Task 10: `Refresh` orchestrator + record correction

**Files:**
- Create: `internal/modelsync/refresh.go`
- Test: `internal/modelsync/refresh_test.go`

**Interfaces:**
- Consumes: discoverers (Task 7), `Cache` (Task 8), `MigrateText` (Task 9), `memory.Store.Add` + `memory.Embedder`.
- Produces:
  ```go
  type Options struct {
      RoutingPath string
      CachePath   string
      Now         time.Time
      Discoverers []Discoverer
      Store       *memory.Store // global store for corrections; may be nil
      Embed       memory.Embedder // may be nil (store w/o embedding)
      Log         func(format string, args ...any) // may be nil
  }
  func Refresh(ctx context.Context, opts Options) error
  ```

- [ ] **Step 1: Write the failing test**

```go
func TestRefresh_MigratesAndCaches(t *testing.T) {
	dir := t.TempDir()
	routing := filepath.Join(dir, "routing.toml")
	os.WriteFile(routing, []byte("[[rule]]\nverb=\"x\"\nuse=\"codex:gpt-5.5\"\n"), 0o644)
	cache := filepath.Join(dir, "models.json")

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	err := Refresh(context.Background(), Options{
		RoutingPath: routing,
		CachePath:   cache,
		Now:         now,
		Discoverers: []Discoverer{ClaudeDiscoverer{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(routing)
	if !strings.Contains(string(got), `use="codex"`) && !strings.Contains(string(got), `use = "codex"`) {
		t.Errorf("routing not migrated:\n%s", got)
	}
	c, _ := LoadCache(cache)
	if c.RefreshedAt.IsZero() {
		t.Error("cache timestamp not written")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/modelsync/ -run TestRefresh -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

`internal/modelsync/refresh.go`:

```go
package modelsync

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ishaanbatra/styx/internal/memory"
)

// Options configures a Refresh run. All paths are explicit for testability.
type Options struct {
	RoutingPath string
	CachePath   string
	Now         time.Time
	Discoverers []Discoverer
	Store       *memory.Store
	Embed       memory.Embedder
	Log         func(format string, args ...any)
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		o.Log(format, args...)
	}
}

// Refresh discovers each channel's current model, migrates legacy version-pinned
// routing tokens, records corrections, and writes the cache. Best-effort: a
// failing discoverer is skipped, not fatal.
func Refresh(ctx context.Context, opts Options) error {
	results := map[string]Result{}
	var claudeAliases []string
	for _, d := range opts.Discoverers {
		r, err := d.Discover(ctx)
		if err != nil {
			opts.logf("model discovery: %s skipped: %v", d.Channel(), err)
			continue
		}
		results[d.Channel()] = r
		if d.Channel() == "claude" {
			claudeAliases = r.Available
		}
	}

	// Migrate routing (needs claude aliases to de-pin claude tokens).
	if claudeAliases != nil {
		src, err := os.ReadFile(opts.RoutingPath)
		if err != nil {
			return fmt.Errorf("read routing for migration: %w", err)
		}
		out, changes := MigrateText(string(src), claudeAliases)
		if len(changes) > 0 {
			tmp := opts.RoutingPath + ".tmp"
			if err := os.WriteFile(tmp, []byte(out), 0o644); err != nil {
				return fmt.Errorf("write routing tmp: %w", err)
			}
			if err := os.Rename(tmp, opts.RoutingPath); err != nil {
				return fmt.Errorf("rename routing: %w", err)
			}
			for _, c := range changes {
				opts.logf("routing: de-pinned %s -> %s (defer to latest)", c.Old, c.New)
				recordCorrection(ctx, opts, c)
			}
		}
	}

	cache := &Cache{RefreshedAt: opts.Now, Channels: results}
	if err := cache.Save(opts.CachePath); err != nil {
		return fmt.Errorf("save model cache: %w", err)
	}
	return nil
}

func recordCorrection(ctx context.Context, opts Options, c Change) {
	if opts.Store == nil {
		return
	}
	text := fmt.Sprintf("routing: de-pinned %s -> %s (defer to latest)", c.Old, c.New)
	var vec []float32
	if opts.Embed != nil {
		if v, err := opts.Embed.Embed(ctx, text); err == nil {
			vec = v
		} else {
			opts.logf("embed routing correction: %v", err)
		}
	}
	_, err := opts.Store.Add(ctx, memory.Item{
		Kind:       memory.KindRoutingPreference,
		Text:       text,
		Source:     "modelsync",
		Project:    "", // global
		Confidence: 0.9,
		Embedding:  vec,
	})
	if err != nil {
		opts.logf("record routing correction: %v", err)
	}
}
```

> Note: verify `memory.Embedder` is the exported interface name (it is used by `memory.Recall`). If the interface lives under a different name, adjust the `Embed` field type accordingly.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/modelsync/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/modelsync/ && go vet ./internal/modelsync/
git add internal/modelsync/
git commit -m "feat(modelsync): Refresh orchestrator + routing-correction memory"
```

---

### Task 11: re-author seeded `default_routing.go`

**Files:**
- Modify: `cmd/styx/default_routing.go`
- Test: `cmd/styx/dispatch_test.go` (or wherever default routing is parsed in tests)

**Interfaces:**
- Consumes: `config.LoadRoutingFile` (parses the seeded TOML), `MigrateText` invariants.
- Produces: a seeded `routing.toml` already in defer-to-latest form (no version pins), with a `[models]` block and at least one `effort` example.

- [ ] **Step 1: Write the failing test**

```go
func TestDefaultRouting_NoVersionPins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routing.toml")
	if err := os.WriteFile(path, []byte(defaultRoutingTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := config.LoadRoutingFile(path)
	if err != nil {
		t.Fatalf("seeded routing must parse: %v", err)
	}
	// No codex:<version> and no claude:<version> tokens remain.
	if strings.Contains(defaultRoutingTOML, "codex:gpt") {
		t.Error("seeded routing still pins a codex version")
	}
	if strings.Contains(defaultRoutingTOML, "claude:opus-4") || strings.Contains(defaultRoutingTOML, "claude:sonnet-4") {
		t.Error("seeded routing still pins a claude version")
	}
	if r.Models.RefreshIntervalHours == 0 {
		t.Error("seeded routing missing [models] (defaults not applied?)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestDefaultRouting_NoVersionPins -v`
Expected: FAIL (current seed pins versions).

- [ ] **Step 3: Implement**

Edit `defaultRoutingTOML` in `cmd/styx/default_routing.go`:
- Replace every `codex:gpt-5.5` with bare `codex`.
- Replace `claude:opus-4-7` → `claude:opus`, `claude:sonnet-4-6` → `claude:sonnet`.
- Add an example effort on the critic rule:

```toml
[[rule]]
verb = "research.critic"
use  = "codex"
effort = "high"
fallback = ["ollama:qwen2.5-coder:14b"]
```

- Append a `[models]` block before `[brain]`:

```toml
# ── model discovery ──
[models]
refresh_interval_hours = 24
```

Leave `claude:interactive` / `codex:interactive` untouched.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/styx/ -run TestDefaultRouting -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w cmd/styx/ && go vet ./cmd/styx/
git add cmd/styx/default_routing.go cmd/styx/*_test.go docs/ARCHITECTURE.md
git commit -m "feat(routing): seed defer-to-latest defaults + [models] + effort example"
```

(Include the `ARCHITECTURE.md` `last_verified` bump in this commit per the drift contract.)

---

### Task 12: wire `Refresh` into `styx doctor`

**Files:**
- Modify: `cmd/styx/doctor.go`
- Test: `cmd/styx/doctor_test.go`

**Interfaces:**
- Consumes: `modelsync.Refresh`, `modelsync.CodexDiscoverer`, `modelsync.ClaudeDiscoverer`, `paths.ModelsCachePath`, `paths.RoutingPath`.
- Produces: `cmdDoctor` runs a refresh and prints a one-line summary; a refresh error warns but does not fail the doctor run.

- [ ] **Step 1: Write the failing test**

Add a test that builds a temp routing + cache and calls a new extracted helper `runModelRefresh(routingPath, cachePath string, now time.Time) error` (so it's testable without real `~/.codex`):

```go
func TestRunModelRefresh_DePins(t *testing.T) {
	dir := t.TempDir()
	routing := filepath.Join(dir, "routing.toml")
	os.WriteFile(routing, []byte("[[rule]]\nverb=\"x\"\nuse=\"claude:opus-4-7\"\n"), 0o644)
	cache := filepath.Join(dir, "models.json")
	if err := runModelRefresh(routing, cache, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(routing)
	if !strings.Contains(string(got), "claude:opus") || strings.Contains(string(got), "opus-4-7") {
		t.Errorf("not de-pinned:\n%s", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestRunModelRefresh -v`
Expected: FAIL (undefined `runModelRefresh`).

- [ ] **Step 3: Implement**

In `cmd/styx/doctor.go`, add:

```go
func runModelRefresh(routingPath, cachePath string, now time.Time) error {
	return modelsync.Refresh(context.Background(), modelsync.Options{
		RoutingPath: routingPath,
		CachePath:   cachePath,
		Now:         now,
		Discoverers: []modelsync.Discoverer{
			modelsync.CodexDiscoverer{},
			modelsync.ClaudeDiscoverer{},
		},
		Log: func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) },
	})
}
```

In `cmdDoctor`, before `reportDoctor`, call it using real paths and warn-don't-fail:

```go
	if rp, err := paths.RoutingPath(); err == nil {
		if cp, err := paths.ModelsCachePath(); err == nil {
			if err := runModelRefresh(rp, cp, time.Now()); err != nil {
				fmt.Printf("! model refresh skipped: %v\n", err)
			} else {
				fmt.Println("ok models refreshed (defer-to-latest)")
			}
		}
	}
```

Add imports: `context`, `time`, `github.com/ishaanbatra/styx/internal/modelsync`, `github.com/ishaanbatra/styx/internal/paths`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/styx/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w cmd/styx/ && go vet ./cmd/styx/
git add cmd/styx/doctor.go cmd/styx/doctor_test.go docs/ARCHITECTURE.md
git commit -m "feat(doctor): run model refresh (de-pin migration) on every doctor"
```

---

### Task 13: staleness refresh in `loadApp()`

**Files:**
- Modify: `cmd/styx/dispatch.go`
- Test: `cmd/styx/dispatch_test.go`

**Interfaces:**
- Consumes: `modelsync.LoadCache`, `modelsync.Refresh`, `config.Routing.Models.RefreshIntervalHours`.
- Produces: `maybeRefreshModels(routingPath, cachePath string, intervalHours int, now time.Time) (refreshed bool, err error)` — refreshes only when the cache is stale.

- [ ] **Step 1: Write the failing test**

```go
func TestMaybeRefreshModels_OnlyWhenStale(t *testing.T) {
	dir := t.TempDir()
	routing := filepath.Join(dir, "routing.toml")
	os.WriteFile(routing, []byte("[[rule]]\nverb=\"x\"\nuse=\"codex\"\n"), 0o644)
	cache := filepath.Join(dir, "models.json")

	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// Fresh cache => no refresh.
	(&modelsync.Cache{RefreshedAt: now}).Save(cache)
	did, err := maybeRefreshModels(routing, cache, 24, now.Add(1*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if did {
		t.Error("should not refresh when cache is fresh")
	}
	// Stale cache => refresh.
	did, err = maybeRefreshModels(routing, cache, 24, now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Error("should refresh when cache is stale")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/styx/ -run TestMaybeRefreshModels -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

In `cmd/styx/dispatch.go`:

```go
func maybeRefreshModels(routingPath, cachePath string, intervalHours int, now time.Time) (bool, error) {
	c, err := modelsync.LoadCache(cachePath)
	if err != nil {
		return false, err
	}
	if !c.IsStale(now, time.Duration(intervalHours)*time.Hour) {
		return false, nil
	}
	err = modelsync.Refresh(context.Background(), modelsync.Options{
		RoutingPath: routingPath,
		CachePath:   cachePath,
		Now:         now,
		Discoverers: []modelsync.Discoverer{
			modelsync.CodexDiscoverer{},
			modelsync.ClaudeDiscoverer{},
		},
	})
	return err == nil, err
}
```

In `loadApp()`, after `config.LoadRouting()` succeeds, call it best-effort (warn, never fail):

```go
	if rp, perr := paths.RoutingPath(); perr == nil {
		if cp, perr := paths.ModelsCachePath(); perr == nil {
			if _, rerr := maybeRefreshModels(rp, cp, r.Models.RefreshIntervalHours, time.Now()); rerr != nil {
				fmt.Fprintf(os.Stderr, "[styx] model refresh skipped: %v\n", rerr)
			} else {
				// Reload routing so a migration applied this run takes effect now.
				if r2, rerr := config.LoadRouting(); rerr == nil {
					r = r2
				}
			}
		}
	}
```

Add imports as needed (`os`, `time`, `context`, `modelsync`, `paths`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/styx/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w cmd/styx/ && go vet ./cmd/styx/
git add cmd/styx/dispatch.go cmd/styx/dispatch_test.go docs/ARCHITECTURE.md
git commit -m "feat(dispatch): staleness-gated model refresh in loadApp"
```

---

### Task 14: docs + full-suite verification

**Files:**
- Modify: `docs/ARCHITECTURE.md`

**Interfaces:** none (documentation + verification).

- [ ] **Step 1: Document the new subsystem**

In `docs/ARCHITECTURE.md`, add an `internal/modelsync` subsection: its role (defer-to-latest discovery, idempotent de-pin migration, models.json cache, run from doctor + loadApp staleness), the codex/claude discoverers, and the effort axis (`Rule.Effort` → `Decision.Effort` → `Request.Effort` → codex/claude flags). Note agy/ollama are out of scope and why. Bump the `last_verified` date in the frontmatter to 2026-06-18.

- [ ] **Step 2: Build, vet, gofmt**

Run: `go build ./... && go vet ./... && gofmt -l .`
Expected: builds clean, vet clean, `gofmt -l` prints nothing.

- [ ] **Step 3: Full test suite**

Run: `go test ./...`
Expected: all packages PASS.

- [ ] **Step 4: Manual smoke (requires real CLIs)**

```bash
make install
styx doctor              # prints "ok models refreshed (defer-to-latest)"; routing.toml de-pinned
grep -n "codex" ~/.config/styx/routing.toml   # bare "codex", no version
styx "research: how to speed up embedding"     # critic dispatch uses codex with effort=high, no gpt-5 error
```

- [ ] **Step 5: Commit**

```bash
git add docs/ARCHITECTURE.md
git commit -m "docs(architecture): document internal/modelsync + effort axis"
```

---

## Self-Review

**Spec coverage:**
- Defer-to-latest (codex no `--model`, claude alias) → Tasks 3, 11.
- Effort axis (Rule→Decision→Request→adapters, pass-through) → Tasks 1–5.
- codex/claude discoverers → Task 7.
- models.json cache + staleness → Tasks 6, 8.
- One-time idempotent de-pin migration + record correction → Tasks 9, 10.
- doctor always refreshes → Task 12.
- loadApp staleness (covers verbs/one-shot/REPL) → Task 13.
- agy/ollama out of scope → respected (migration never touches them; Task 9 test asserts it).
- Drift contract (ARCHITECTURE, default_routing) → Tasks 11–14.

**Placeholder scan:** all code steps contain full code; no TBD/TODO.

**Type consistency:** `Result`, `Discoverer`, `Cache`, `Options`, `Change`, `MigrateText`, `Refresh`, `Rule.Effort`, `Decision.Effort`, `Request.Effort` are defined once and referenced consistently. `memory.Item`/`memory.KindRoutingPreference`/`memory.Embedder` match the existing memory package (Task 10 note flags the one name to re-verify at implementation time).
