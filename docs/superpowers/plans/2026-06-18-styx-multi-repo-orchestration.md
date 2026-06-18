# Styx Multi-Repo Orchestration + Run-From-Anywhere Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make styx conversational, brain-routed, and multi-repo — every verb runs from any directory via an explicit target, and a single REPL session binds a set of repos so one coordinated task can reason and act across all of them.

**Architecture:** A new `internal/target` resolver unifies the two divergent target resolvers and becomes the single seam every verb and the REPL route through (no silent cwd fallback). Projects gain a stable path-hash `ID` that re-keys all per-project on-disk state, with an idempotent migration. The budget sqlite hardens for concurrent processes (WAL + busy_timeout) and gains per-event `project`/`run_id` tags. The brain `Dispatch` gains `Project`/`ExtraRoots`, the prompt sees the registry, and both the channel and agent adapters translate extra roots to the providers' `--add-dir`. Finally `replSession` becomes multi-project: a lazily-bound set keyed by ID with a current-focus pointer, session-scoped recall across every bound repo's store, and one project-tagged audit stream.

**Tech Stack:** Go 1.22, `modernc.org/sqlite` (already a dep, pure-Go), ollama HTTP API, claude/codex/agy CLIs via subprocess. **No new Go module dependencies.**

**Spec:** `docs/superpowers/specs/2026-06-18-styx-multi-repo-orchestration-design.md`

**Module path:** `github.com/ishaanbatra/styx` (use this prefix for all internal imports, e.g. `github.com/ishaanbatra/styx/internal/target`).

## Global Constraints

These apply to **every** task. Copied verbatim from CLAUDE.md and the spec:

- **Branch base:** This work builds on the REPL orchestrator + model-auto-discovery code that lives on `origin/main` (48 commits ahead of the branch's local-main base). **Phase 0 is a hard prerequisite** — without it, Phases 4–5 have no `cmd/styx/repl.go`, `internal/agent`, `internal/audit`, or effort-aware channels to edit. Every file:line reference in this plan is against `origin/main`'s tree.
- **Drift contract (CLAUDE.md, the most important rule):** after editing code, update the owner doc **in the same commit** and bump its `last_verified` date. `docs/ARCHITECTURE.md` owns `cmd/styx/**`, `internal/**`, `testdata/**`. `README.md` owns the user-facing verb table. This plan ships each phase's doc updates as that phase's **final commit** — a deliberate batching, since one architecture-section edit spans several tasks. That is within the same phase (never deferred past it), which is a pragmatic reading of "same commit as code"; if you prefer the strict reading, fold each task's doc delta into that task's own commit instead. A PostToolUse hook nags after every `.go` edit; do not dismiss it.
- **Never swallow errors:** no `x, _ :=` in new code (existing `proj, _ :=` in `grunt.go`/`route.go` are pre-existing tolerances; do not propagate the pattern). Wrap with `fmt.Errorf("context: %w", err)`. The new resolver must **fail loud** — no silent cwd fallback when an explicit target was given.
- **Atomic writes:** all new on-disk state uses tmp+rename (mirror `config.SaveProjects`). The audit log is the one deliberate exception (append-only `O_APPEND` under a mutex) — keep it that way.
- **modernc.org/sqlite only** (`sql.Open("sqlite", ...)`). Do not switch drivers.
- **Keep `styx auto --resume` loadable:** any `pipeline.State` change must be additive with `omitempty` and must not be required by `LoadState`/`Resume`.
- **routing.toml defaults:** this work introduces **no** routing-table change, so `cmd/styx/default_routing.go` is **NOT** touched. If a task ever needs to, it must update `default_routing.go` and its upgrade path in the same commit.
- **The state re-key migration must be idempotent and safe to re-run.**
- **Testing:** table-driven with `t.Run`; fakes over mocks (`httptest` for ollama, scripted `testdata/fakeagent` for agent CLIs, fake-binary stubs for channel adapters). Isolate config via `t.Setenv("XDG_CONFIG_HOME", dir)` + `t.TempDir()`. Run `go build ./... && go vet ./... && gofmt -l .` before every commit.

---

## File structure

New files:

```
internal/target/target.go         Spec + Resolve(): unified alias|dir|cwd -> Project, no silent fallback
internal/target/target_test.go    table-driven resolver tests (exact/prefix/ambiguous/dir/cwd/unknown)
cmd/styx/scan.go                  `styx project scan` walk-down discovery + bulk register
cmd/styx/scan_test.go             scan against a temp tree (nested + vendored dirs)
```

Modified files:

```
internal/config/projects.go       + ID field, + ProjectID() path-hash, + Description field; backfill on load
internal/project/project.go       fix findGitRoot tautology guard; populate ID in autoRegister
cmd/styx/dispatch.go              delete resolveTarget; + resolveGlobalTarget(); reroute verb dispatch
cmd/styx/intel.go                 delete resolveProjectArg; route cmdIntel through target.Resolve
cmd/styx/main.go                  parseGlobalFlags learns --project/--dir (value-consuming)
cmd/styx/build.go                 route through resolveGlobalTarget
cmd/styx/{research,plan,review,context,execute,runs,auto,grunt,route}.go  route through resolveGlobalTarget
cmd/styx/project.go               + `scan` subcommand wiring; populate ID on `project add`
internal/budget/budget.go         + Event.Project/RunID, + usage.project/run_id columns (ALTER), WAL+busy_timeout DSN
internal/brain/action.go          + Dispatch.Project/ExtraRoots, + ActionSchema project/extra_roots
internal/brain/brain.go           + Turn.BoundProjects/KnownProjects
internal/brain/prompt.go          render registry block into the user prompt
internal/channel/channel.go       + Request.ExtraRoots
internal/channel/claude/claude.go + --add-dir per ExtraRoot in claudeArgs
internal/channel/codex/codex.go   + --add-dir per ExtraRoot after `exec`
internal/agent/manager.go         + DispatchSpec.ExtraRoots; render --add-dir into extra in Dispatch
internal/agent/adapter.go         codex ArgsFn: place extra AFTER `exec` (so --add-dir is valid)
cmd/styx/repl.go                  multi-project session: bound set + focus; lazy bind; cross-repo recall; per-dispatch routing; tagged audit; /focus /repos; header
cmd/styx/repl_test.go             extend scripted session to two repos
docs/ARCHITECTURE.md              new internal/target section; update Projects&paths, Brain, Agent, Channels, Budget, Audit, Memory, On-disk layout, cmd/styx; bump last_verified
README.md                         global --project/--dir; `project scan`; launch binding
```

Conventions to follow (from this codebase): table-driven tests with `t.Run`, errors wrapped with `fmt.Errorf("context: %w", err)`, `channel.ClassifiedError` for channel errors, atomic file writes via tmp+rename, `paths.EnsureDir` for directories, status/narration to stderr via `logStatus`. Run `go build ./... && go vet ./...` before every commit.

---

## Phase 0 — Prerequisite: sync the branch onto origin/main

This phase carries no code of its own; it establishes the base the entire plan assumes. **Do it first and do not skip it.**

### Task 0.1: Sync `feature/multi-repo-orchestration` onto `origin/main`

**Files:**
- No source edits. This is a git operation that brings `internal/agent`, `cmd/styx/repl.go`, `internal/audit`, `internal/modelsync`, `cmd/styx/doctor`, and the effort-aware channel adapters into the working tree.

- [ ] **Step 1: Confirm the gap**

Run: `git fetch origin && git log --oneline main..origin/main | wc -l`
Expected: a non-zero count (~48) — `origin/main` is ahead and carries the REPL orchestrator + model-auto-discovery.

Run: `ls cmd/styx/repl.go internal/agent/manager.go internal/audit/log.go 2>&1`
Expected: "No such file or directory" for all three on the current branch (they arrive in Step 2).

- [ ] **Step 2: Merge `origin/main` into the feature branch**

```bash
git checkout feature/multi-repo-orchestration
git merge --no-edit origin/main
```

Expected: a clean merge. The only commit unique to this branch before the merge is the design-spec doc (`4b2e051`), which does not conflict with code. If the merge reports conflicts, resolve them preserving `origin/main`'s code and this branch's spec doc.

- [ ] **Step 3: Verify the orchestrator code is present and the tree builds green**

Run: `ls cmd/styx/repl.go internal/agent/manager.go internal/audit/log.go internal/modelsync 2>&1`
Expected: all present.

Run: `go build ./... && go vet ./... && make test`
Expected: build clean, vet clean, all tests PASS. (The brain integration suite `TestRoutingAccuracy` is env-gated behind `STYX_BRAIN_IT=1` and is expected to be skipped without ollama.)

- [ ] **Step 4: Confirm the spec is still on the branch**

Run: `ls docs/superpowers/specs/2026-06-18-styx-multi-repo-orchestration-design.md`
Expected: present.

- [ ] **Step 5: No commit needed if the merge already produced one.** If `git merge` fast-forwarded or produced a merge commit, the branch is ready. Do not create an empty commit.

---

## Phase 1 — Unified target resolver + global flags + route all verbs

### Task 1.1: `internal/target` resolver

**Files:**
- Create: `internal/target/target.go`
- Test: `internal/target/target_test.go`

**Interfaces:**
- Produces: `target.Spec{Alias, Dir, Cwd string}` and `func target.Resolve(spec Spec) (project.Project, error)`. Precedence Alias → Dir → Cwd. Alias resolution: exact name → unique prefix → existing path → error listing candidates. Never silently falls back to cwd when an explicit Alias/Dir failed. Returned type is `project.Project` (= `config.Project` alias).

- [ ] **Step 1: Write the failing tests** — create `internal/target/target_test.go`:

```go
package target

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
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

func seedRegistry(t *testing.T, projs ...config.Project) {
	t.Helper()
	if err := config.SaveProjects(projs); err != nil {
		t.Fatal(err)
	}
}

func TestResolve(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())

	backend := filepath.Join(t.TempDir(), "ai-ta-backend")
	teacher := filepath.Join(t.TempDir(), "ai-ta-teacher-ui")
	gitInit(t, backend)
	gitInit(t, teacher)
	seedRegistry(t,
		config.Project{Name: "ai-ta-backend", Path: backend, Language: "python"},
		config.Project{Name: "ai-ta-teacher-ui", Path: teacher, Language: "typescript"},
	)

	cases := []struct {
		name     string
		spec     Spec
		wantName string
		wantErr  string // substring; "" = no error
	}{
		{"exact alias", Spec{Alias: "ai-ta-backend"}, "ai-ta-backend", ""},
		{"unique prefix", Spec{Alias: "ai-ta-teacher"}, "ai-ta-teacher-ui", ""},
		{"ambiguous prefix", Spec{Alias: "ai-ta-"}, "", "ambiguous"},
		{"dir resolves to registered repo", Spec{Dir: teacher}, "ai-ta-teacher-ui", ""},
		{"cwd walk-up", Spec{Cwd: backend}, "ai-ta-backend", ""},
		{"unknown alias errors, no cwd fallback", Spec{Alias: "nope", Cwd: backend}, "", "unknown project"},
		{"empty spec errors", Spec{}, "", "no target"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.spec)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got.Name != tc.wantName {
				t.Errorf("got %q, want %q", got.Name, tc.wantName)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/target/ -run TestResolve -v`
Expected: FAIL — `package github.com/ishaanbatra/styx/internal/target ... no Go files` / `undefined: Resolve`.

- [ ] **Step 3: Implement** — create `internal/target/target.go`:

```go
// Package target resolves the active project for any styx invocation from a
// single seam: a {--project alias, --dir path, cwd} spec. It replaces the two
// divergent resolvers (cmd/styx resolveTarget + resolveProjectArg) and never
// silently falls back to the cwd when an explicit target was given and failed.
package target

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/project"
)

// Spec describes how to resolve the active project. Precedence: Alias -> Dir -> Cwd.
type Spec struct {
	Alias string // from --project or a name the brain/user gave (may also be a path)
	Dir   string // from --dir
	Cwd   string // fallback, usually os.Getwd()
}

// Resolve returns the project for the spec. Alias resolution: exact Name match
// -> unique prefix match -> existing directory path -> error listing
// candidates. An explicit Alias/Dir that fails is an error, never a cwd guess.
func Resolve(spec Spec) (project.Project, error) {
	switch {
	case spec.Alias != "":
		return resolveAlias(spec.Alias)
	case spec.Dir != "":
		abs, err := filepath.Abs(spec.Dir)
		if err != nil {
			return project.Project{}, fmt.Errorf("resolve --dir %q: %w", spec.Dir, err)
		}
		return project.CurrentFrom(abs)
	case spec.Cwd != "":
		return project.CurrentFrom(spec.Cwd)
	default:
		return project.Project{}, fmt.Errorf("no target: name a project (--project), pass --dir, or cd into a repo")
	}
}

func resolveAlias(alias string) (project.Project, error) {
	regs, err := config.LoadProjects()
	if err != nil {
		return project.Project{}, fmt.Errorf("load registry: %w", err)
	}
	// 1. exact name.
	for _, p := range regs {
		if p.Name == alias {
			return p, nil
		}
	}
	// 2. unique prefix.
	var prefix []config.Project
	for _, p := range regs {
		if strings.HasPrefix(p.Name, alias) {
			prefix = append(prefix, p)
		}
	}
	if len(prefix) == 1 {
		return prefix[0], nil
	}
	if len(prefix) > 1 {
		return project.Project{}, fmt.Errorf("ambiguous project %q: matches %s", alias, names(prefix))
	}
	// 3. treat as a path: an existing dir, or a path under a registered repo.
	if abs, absErr := filepath.Abs(alias); absErr == nil {
		if fi, statErr := os.Stat(abs); statErr == nil && fi.IsDir() {
			return project.CurrentFrom(abs)
		}
		for _, p := range regs {
			if isUnder(abs, p.Path) {
				return p, nil
			}
		}
	}
	// 4. give up loudly with candidates.
	return project.Project{}, fmt.Errorf("unknown project %q (registered: %s)", alias, names(regs))
}

// isUnder reports whether path is base or lives inside base, using path
// boundaries (so "/x/foo" is not "under" "/x/foobar").
func isUnder(path, base string) bool {
	if base == "" {
		return false
	}
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

func names(projs []config.Project) string {
	if len(projs) == 0 {
		return "(none)"
	}
	ns := make([]string, len(projs))
	for i, p := range projs {
		ns[i] = p.Name
	}
	return strings.Join(ns, ", ")
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/target/ -v`
Expected: PASS (all sub-tests).

- [ ] **Step 5: Commit**

```bash
git add internal/target/
git commit -m "feat(target): unified alias|dir|cwd resolver with no silent fallback"
```

### Task 1.2: Replace both old resolvers; route `build` and `intel` through `target`

**Files:**
- Modify: `cmd/styx/dispatch.go` (delete `resolveTarget` at lines 339–349; add `resolveGlobalTarget` helper)
- Modify: `cmd/styx/intel.go` (delete `resolveProjectArg` at lines 96–118; route `cmdIntel` at line 47)
- Modify: `cmd/styx/build.go` (route `cmdBuild` at line 19)

**Interfaces:**
- Consumes: `target.Resolve` (Task 1.1).
- Produces: `func resolveGlobalTarget(arg string) (project.Project, error)` in `cmd/styx` — resolves `arg` (a positional alias/path, or "") combined with the global `--project`/`--dir` flags (Task 1.3 sets those package vars). Used by every verb.

- [ ] **Step 1: Add the package-level globals and helper** — in `cmd/styx/dispatch.go`, next to the existing `globalQuiet`/`globalVerbose` vars (lines 28–30), add:

```go
// Global target flags, set by main() after parseGlobalFlags.
var (
	globalProjectAlias string
	globalDirArg       string
)

// resolveGlobalTarget resolves the active project. A non-empty positional arg
// (e.g. `styx build backend`, `styx intel ./api`) takes precedence; otherwise
// the global --project/--dir flags are consulted; otherwise the cwd. It never
// silently falls back to the cwd when an explicit alias/dir failed.
func resolveGlobalTarget(arg string) (project.Project, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return project.Project{}, fmt.Errorf("getwd: %w", err)
	}
	alias := arg
	if alias == "" {
		alias = globalProjectAlias
	}
	return target.Resolve(target.Spec{Alias: alias, Dir: globalDirArg, Cwd: cwd})
}
```

Add `"os"`, `"fmt"`, and `"github.com/ishaanbatra/styx/internal/target"` to the import block if not already present.

- [ ] **Step 2: Delete `resolveTarget`** — remove lines 339–349 of `cmd/styx/dispatch.go` (the `resolveTarget` func and its doc comment). Its sole caller is updated below.

- [ ] **Step 3: Delete `resolveProjectArg`** — remove lines 96–118 of `cmd/styx/intel.go` (the whole func). Remove the now-unused `"path/filepath"`/`"strings"` imports only if no other code in the file uses them (verify with `go build`).

- [ ] **Step 4: Reroute the two callers**

In `cmd/styx/build.go` line 19, replace:
```go
	proj, err := resolveTarget(target)
```
with:
```go
	proj, err := resolveGlobalTarget(target)
```

In `cmd/styx/intel.go` line 47, replace:
```go
	proj, err := resolveProjectArg(target)
```
with:
```go
	proj, err := resolveGlobalTarget(target)
```

- [ ] **Step 5: Verify it builds and existing tests pass**

Run: `go build ./... && go test ./cmd/styx/ ./internal/target/`
Expected: PASS. (No new test here — this is a refactor with behavior covered by Task 1.1's resolver tests and the existing intel/build tests. The behavioral change — unknown alias now errors instead of silently using cwd — is asserted in Task 1.1's "unknown alias errors" case.)

- [ ] **Step 6: Commit**

```bash
git add cmd/styx/dispatch.go cmd/styx/intel.go cmd/styx/build.go
git commit -m "refactor(cmd): replace both resolvers with target.Resolve; delete silent fallback"
```

### Task 1.3: Global `--project` / `--dir` flags

**Files:**
- Modify: `cmd/styx/main.go` (`parseGlobalFlags` lines 9–23; `main` lines 25–65)
- Test: `cmd/styx/main_test.go` (create if absent)

**Interfaces:**
- Produces: `parseGlobalFlags(argv) (rest []string, quiet, verbose bool, projectAlias, dirArg string)`. `main` stores `projectAlias`/`dirArg` into `globalProjectAlias`/`globalDirArg` (Task 1.2) before dispatch.

- [ ] **Step 1: Write the failing test** — create (or append to) `cmd/styx/main_test.go`:

```go
package main

import (
	"reflect"
	"testing"
)

func TestParseGlobalFlags(t *testing.T) {
	cases := []struct {
		name        string
		argv        []string
		wantRest    []string
		wantQuiet   bool
		wantProject string
		wantDir     string
	}{
		{"plain verb", []string{"plan", "do x"}, []string{"plan", "do x"}, false, "", ""},
		{"project flag", []string{"--project", "backend", "review"}, []string{"review"}, false, "backend", ""},
		{"dir flag", []string{"--dir", "/repos/api", "plan", "x"}, []string{"plan", "x"}, false, "", "/repos/api"},
		{"project equals form", []string{"--project=backend", "review"}, []string{"review"}, false, "backend", ""},
		{"quiet still works", []string{"--quiet", "--project", "ui", "auto", "g"}, []string{"auto", "g"}, true, "ui", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rest, quiet, _, project, dir := parseGlobalFlags(tc.argv)
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
			if quiet != tc.wantQuiet {
				t.Errorf("quiet = %v, want %v", quiet, tc.wantQuiet)
			}
			if project != tc.wantProject {
				t.Errorf("project = %q, want %q", project, tc.wantProject)
			}
			if dir != tc.wantDir {
				t.Errorf("dir = %q, want %q", dir, tc.wantDir)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/styx/ -run TestParseGlobalFlags -v`
Expected: FAIL — `parseGlobalFlags ... too many return values` / signature mismatch.

- [ ] **Step 3: Implement** — replace `parseGlobalFlags` (lines 9–23) in `cmd/styx/main.go` with a value-consuming indexed loop:

```go
// parseGlobalFlags strips global flags from argv (long form only), returning
// the remaining tokens plus the parsed values. Recognized: --quiet, --verbose,
// --project <alias> (or --project=alias), --dir <path> (or --dir=path).
func parseGlobalFlags(argv []string) (rest []string, quiet, verbose bool, projectAlias, dirArg string) {
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--quiet":
			quiet = true
		case a == "--verbose":
			verbose = true
		case a == "--project":
			if i+1 < len(argv) {
				projectAlias = argv[i+1]
				i++
			}
		case strings.HasPrefix(a, "--project="):
			projectAlias = strings.TrimPrefix(a, "--project=")
		case a == "--dir":
			if i+1 < len(argv) {
				dirArg = argv[i+1]
				i++
			}
		case strings.HasPrefix(a, "--dir="):
			dirArg = strings.TrimPrefix(a, "--dir=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, quiet, verbose, projectAlias, dirArg
}
```

Ensure `"strings"` is imported in `main.go`.

- [ ] **Step 4: Thread the parsed values in `main`** — update both call sites of `parseGlobalFlags` consumption in `main()` (line 26 onward). Replace:
```go
	rest, quiet, verbose := parseGlobalFlags(os.Args[1:])
```
with:
```go
	rest, quiet, verbose, projectAlias, dirArg := parseGlobalFlags(os.Args[1:])
```
and immediately after the existing `globalQuiet = quiet` / `globalVerbose = verbose` assignments (both the `len(rest)==0` REPL branch at ~line 30 and the verb branch at ~line 49), add:
```go
	globalProjectAlias = projectAlias
	globalDirArg = dirArg
```
(Set them in both branches so the bare-`styx` REPL path and the verb path both honor the flags.)

- [ ] **Step 5: Route the bare-REPL and one-shot entrypoints through the target** — Phase 5 makes the session multi-project; for now ensure `newREPLSession` honors the flags. In `cmd/styx/repl.go` line 380, replace `proj, err := project.Current()` with `proj, err := resolveGlobalTarget("")`. This keeps single-repo behavior when no flags are given (cwd) and lets `styx --project backend` / `styx --dir /x` open the REPL on that repo.

  **Bare `styx` from a non-repo cwd (e.g. `~`):** `resolveGlobalTarget("")` with no flags and a non-git cwd returns `project.ErrNotInGitRepo` (the loud-resolver contract — no silent guess). For v1, `newREPLSession` surfaces that error so bare `styx` from `~` prints the targeting guidance ("name a project with --project, pass --dir, or cd into a repo") and exits 1, exactly like a verb. The "run from `~`" capability is therefore exercised by **naming a target** (`styx --project backend`, `styx --dir /x`, or the positional launch binding `styx backend teacher` added in Task 5.5) — not by a focus-less empty REPL. A graceful zero-focus REPL is explicitly out of scope for v1 (recorded under Known deviations); the spec's "run from `~`" is satisfied by the explicit-target forms.

- [ ] **Step 6: Run the tests**

Run: `go test ./cmd/styx/ -run TestParseGlobalFlags -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 7: Commit**

```bash
git add cmd/styx/main.go cmd/styx/main_test.go cmd/styx/repl.go
git commit -m "feat(cmd): global --project/--dir flags routed through target.Resolve"
```

### Task 1.4: Route the remaining verbs through the resolver; fix `findGitRoot`

**Files:**
- Modify: `cmd/styx/research.go:34`, `plan.go:24`, `review.go:17`, `context.go:15`, `execute.go:21`, `runs.go:33,50,82`, `auto.go:53`, `grunt.go:30`, `route.go:24`
- Modify: `internal/project/project.go` (`findGitRoot` lines 107–119)
- Test: `internal/project/project_test.go` (append)

- [ ] **Step 1: Reroute the `project.Current()` call sites.** For each verb that resolves the active project from cwd, replace `project.Current()` with `resolveGlobalTarget("")` so the global `--project`/`--dir` flags take effect. Apply to: `research.go:34`, `plan.go:24`, `review.go:17`, `context.go:15`, `execute.go:21`, `auto.go:53`, `runs.go:33`, `runs.go:50`, `runs.go:82`. For the two best-effort sites that tolerate "not in a repo" (`grunt.go:30` and `route.go:24`, both `proj, _ :=`), keep the `_` error tolerance but switch the call:
```go
	proj, _ := resolveGlobalTarget("")
```
(These only use `proj` for `signals.Extract`; a no-repo cwd is acceptable there.)

- [ ] **Step 2: Fix the `findGitRoot` tautology** — in `internal/project/project.go` replace the guard at line 110:
```go
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && (fi.IsDir() || !fi.IsDir()) {
```
with the honest predicate (a `.git` dir, or a `.git` file as in a worktree/submodule):
```go
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
```

- [ ] **Step 3: Write a test for the worktree `.git`-as-file case** — append to `internal/project/project_test.go`:

```go
func TestFindGitRootAcceptsGitFile(t *testing.T) {
	root := t.TempDir()
	// A linked worktree has a `.git` *file*, not a directory.
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := findGitRoot(root)
	if err != nil {
		t.Fatalf("findGitRoot: %v", err)
	}
	if got != root {
		t.Errorf("findGitRoot = %q, want %q", got, root)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/project/ -run TestFindGitRootAcceptsGitFile -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/styx/ internal/project/project.go internal/project/project_test.go
git commit -m "feat(cmd): route every verb through target.Resolve; fix findGitRoot guard"
```

### Task 1.5: Phase 1 docs (drift contract)

**Files:**
- Modify: `docs/ARCHITECTURE.md` (add `internal/target`; note global flags in cmd/styx section; bump `last_verified`)
- Modify: `README.md` (global flags)

- [ ] **Step 1: Update `docs/ARCHITECTURE.md`.** In the "cmd/styx — verbs and app wiring" section, note that `parseGlobalFlags` now also strips `--project <alias>` / `--dir <path>`, and that every verb resolves its target through `internal/target.Resolve` (replacing the deleted `resolveTarget`/`resolveProjectArg`). Add a new subsection under "Projects & paths" (or a sibling section) describing `internal/target`:

```markdown
## Target resolution (internal/target)

`target.Resolve(Spec{Alias, Dir, Cwd})` is the single seam every verb and the
REPL use to turn a `--project alias` / `--dir path` / cwd into a `Project`.
Precedence is Alias -> Dir -> Cwd; alias resolution is exact-name -> unique
-prefix -> existing-path -> error listing candidates. It never silently falls
back to the cwd when an explicit target was given and failed (the old
`resolveTarget` footgun). `cmd/styx` wraps it as `resolveGlobalTarget(arg)`,
combining a verb's positional target with the global flags.
```

Bump the frontmatter `last_verified:` to today's date (2026-06-18).

- [ ] **Step 2: Update `README.md`.** Add a short "Global flags" note under the Verbs heading:

```markdown
### Global flags

| Flag | What it does |
|---|---|
| `--project <alias>` | Run the verb against a registered project, from anywhere (exact name or unique prefix) |
| `--dir <path>` | Run the verb against the repo at `<path>`, from anywhere |

Without either flag, styx uses the current directory's repo. An explicit
target that can't be resolved is a clear error, never a silent fallback.
```

- [ ] **Step 3: Commit**

```bash
git add docs/ARCHITECTURE.md README.md
git commit -m "docs(target): document internal/target + global --project/--dir flags"
```

---

## Phase 2 — `project scan` + stable project ID + state re-key + WAL hardening

### Task 2.1: Stable project `ID` (path hash) + `Description` field + backfill

**Files:**
- Modify: `internal/config/projects.go` (`Project` struct lines 16–23; `LoadProjects` line 31; add `ProjectID`)
- Modify: `internal/project/project.go` (`autoRegister` lines 136–151)
- Modify: `cmd/styx/project.go` (`project add` literal, line ~28)
- Test: `internal/config/projects_test.go` (append)

**Interfaces:**
- Produces: `config.Project.ID string` (toml `id,omitempty`); `config.Project.Description string` (toml `description,omitempty`); `func config.ProjectID(absPath string) string` returning the first 12 hex chars of `sha256(absPath)`. `LoadProjects` backfills any empty `ID` from `Path`.

- [ ] **Step 1: Write the failing test** — append to `internal/config/projects_test.go`:

```go
func TestProjectIDStableAndDistinct(t *testing.T) {
	a := ProjectID("/Users/x/Documents/GitHub/ai-ta-backend")
	again := ProjectID("/Users/x/Documents/GitHub/ai-ta-backend")
	b := ProjectID("/Users/x/Documents/GitHub/ai-ta-teacher-ui")
	if a != again {
		t.Errorf("ID not stable: %q vs %q", a, again)
	}
	if a == b {
		t.Errorf("distinct paths share an ID: %q", a)
	}
	if len(a) != 12 {
		t.Errorf("ID length = %d, want 12", len(a))
	}
}

func TestLoadProjectsBackfillsID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// Write a legacy entry with no ID.
	if err := SaveProjects([]Project{{Name: "legacy", Path: "/repos/legacy", Language: "go"}}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != ProjectID("/repos/legacy") {
		t.Errorf("ID not backfilled: %+v", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/ -run 'TestProjectID|TestLoadProjectsBackfill' -v`
Expected: FAIL — `undefined: ProjectID` / `Project has no field ID`.

- [ ] **Step 3: Implement.** In `internal/config/projects.go`, add fields to the `Project` struct (lines 16–23):

```go
type Project struct {
	ID           string   `toml:"id,omitempty"`
	Name         string   `toml:"name"`
	Path         string   `toml:"path"`
	Language     string   `toml:"language"`
	Description  string   `toml:"description,omitempty"`
	ResearchDir  string   `toml:"research_dir,omitempty"`
	PlansDir     string   `toml:"plans_dir,omitempty"`
	DefaultVerbs []string `toml:"default_verbs,omitempty"`
}
```

Add the helper (new imports `crypto/sha256`, `encoding/hex`):

```go
// ProjectID returns a stable 12-hex-char identifier for a project, derived from
// its absolute path. Used to key on-disk per-project state (memory, audit,
// intel, threads) so a rename never orphans it.
func ProjectID(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return hex.EncodeToString(sum[:])[:12]
}
```

In `LoadProjects` (line 31), after unmarshalling, backfill empty IDs before returning:

```go
	for i := range pf.Project {
		if pf.Project[i].ID == "" && pf.Project[i].Path != "" {
			pf.Project[i].ID = ProjectID(pf.Project[i].Path)
		}
	}
	return pf.Project, nil
```

(Adjust the variable name to match the existing `projectsFile`/local var.)

- [ ] **Step 4: Populate ID at the two construction sites.** In `internal/project/project.go` `autoRegister` (lines 144–150), set `ID` from the (absolute) root:

```go
	return Project{
		ID:          config.ProjectID(root),
		Name:        name,
		Path:        root,
		Language:    SniffLanguage(root),
		ResearchDir: "styx/research",
		PlansDir:    "styx/plans",
	}
```

In `cmd/styx/project.go` `project add` (line ~28), abs-resolve the path and set ID:

```go
	abs, err := filepath.Abs(args[2])
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", args[2], err)
	}
	return project.Register(config.Project{
		ID:       config.ProjectID(abs),
		Name:     args[1],
		Path:     abs,
		Language: project.SniffLanguage(abs),
	})
```

(Add `"path/filepath"`/`"fmt"` imports to `project.go` if needed.)

- [ ] **Step 5: Update the existing round-trip fixture (required — it WILL break otherwise).** `TestSaveAndLoadProjects` (`internal/config/projects_test.go:36`) compares with `cmp.Diff(want, got)` against `want` Project literals that have no `ID`; after `LoadProjects` backfills `ID`, the diff fails for certain. Add the expected `ID` to each `want` entry, e.g.:

```go
	want := []Project{
		{ID: ProjectID("/Users/x/Documents/GitHub/ai-ta-backend"), Name: "hoot-backend", Path: "/Users/x/Documents/GitHub/ai-ta-backend", Language: "python", ResearchDir: "docs/research", PlansDir: "docs/plans", DefaultVerbs: []string{"plan", "build", "review"}},
		{ID: ProjectID("/Users/x/Documents/GitHub/VoiceResumeBot"), Name: "voiceresumebot", Path: "/Users/x/Documents/GitHub/VoiceResumeBot", Language: "python"},
	}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/config/ ./internal/project/ ./cmd/styx/ && go build ./...`
Expected: PASS (`ID`/`Description` are `omitempty` so legacy `projects.toml` files still round-trip; the updated fixture matches the backfilled IDs).

- [ ] **Step 7: Commit**

```bash
git add internal/config/projects.go internal/project/project.go cmd/styx/project.go internal/config/projects_test.go
git commit -m "feat(config): stable project ID (path hash) + description; backfill on load"
```

### Task 2.2: Re-key per-project state (memory db, audit dir, intel dir, threads) by ID

**Files:**
- Modify: `cmd/styx/repl.go` (memory db line 391; threads line 406; audit dir line 416)
- Modify: `internal/intel/intel.go` (`indexDir` lines 89–96)
- Test: `internal/intel/intel_test.go` (append) and rely on the migration test in Task 2.3

**Interfaces:**
- Consumes: `config.ProjectID` / `proj.ID` (Task 2.1).
- Produces: all per-project state paths keyed by `proj.ID` instead of `proj.Name`. `global.db` stays a fixed name (NOT re-keyed).

- [ ] **Step 1: Re-key the intel index dir** — in `internal/intel/intel.go` `indexDir` (line 95), change `proj.Name` to `proj.ID` and update the doc comment (line 89):

```go
// indexDir returns ~/.config/styx/state/intel/<project-id>/
func indexDir(proj config.Project) (string, error) {
	state, err := paths.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(state, "intel", proj.ID), nil
}
```

- [ ] **Step 2: Re-key memory db, threads, and audit dir in `cmd/styx/repl.go`.** (These edits move with the session refactor in Phase 5, but the re-key itself lands here so the migration in Task 2.3 has a stable target.) Replace:
  - line 391 `memory.Open(filepath.Join(memDir, proj.Name+".db"))` → `memory.Open(filepath.Join(memDir, proj.ID+".db"))`
  - line 406 `agent.LoadThreads(proj.Name)` → `agent.LoadThreads(proj.ID)`
  - line 416 `filepath.Join(auditDir, proj.Name)` → `filepath.Join(auditDir, proj.ID)`

Leave `global.db` (line 395) and `cmd/styx/dispatch.go:156` unchanged — global memory is shared, not per-project.

- [ ] **Step 3: Add an intel test asserting ID-keyed dir** — append to `internal/intel/intel_test.go`:

```go
func TestIndexDirKeyedByID(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	d, err := indexDir(config.Project{ID: "abc123def456", Name: "renamed-since", Path: "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(d) != "abc123def456" {
		t.Errorf("indexDir = %q, want .../abc123def456", d)
	}
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/intel/ -run TestIndexDirKeyedByID -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/intel/intel.go internal/intel/intel_test.go cmd/styx/repl.go
git commit -m "feat(state): re-key memory/audit/intel/threads from project name to stable ID"
```

### Task 2.3: Idempotent migration of `Name`-keyed state to `ID`-keyed

**Files:**
- Create: `internal/config/migrate_state.go` (or add to an existing `cmd/styx/doctor.go` — see Step 3)
- Test: `internal/config/migrate_state_test.go`

**Interfaces:**
- Produces: `func config.MigrateProjectState(projs []Project) error` — for each project, renames legacy `Name`-keyed state to `ID`-keyed if the old exists and the new does not. Idempotent: re-running is a no-op. Covers memory db file, audit dir, intel dir, threads file.

- [ ] **Step 1: Write the failing test** — create `internal/config/migrate_state_test.go`:

```go
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
	memDir, _ := paths.MemoryDir()
	auditDir, _ := paths.AuditDir()
	_ = os.MkdirAll(memDir, 0o755)
	_ = os.MkdirAll(filepath.Join(auditDir, p.Name), 0o755)
	if err := os.WriteFile(filepath.Join(memDir, p.Name+".db"), []byte("legacy"), 0o644); err != nil {
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
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/ -run TestMigrateProjectStateIdempotent -v`
Expected: FAIL — `undefined: MigrateProjectState`.

- [ ] **Step 3: Implement** — create `internal/config/migrate_state.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/paths"
)

// MigrateProjectState renames legacy Name-keyed per-project state to the stable
// ID key. It is idempotent and safe to re-run: each rename only happens when the
// old path exists and the new one does not. Covers the memory db, audit dir,
// intel dir, and agent threads file. global.db is shared and never touched.
func MigrateProjectState(projs []Project) error {
	memDir, err := paths.MemoryDir()
	if err != nil {
		return err
	}
	auditDir, err := paths.AuditDir()
	if err != nil {
		return err
	}
	threadsDir, err := paths.ThreadsDir()
	if err != nil {
		return err
	}
	stateDir, err := paths.StateDir()
	if err != nil {
		return err
	}
	intelDir := filepath.Join(stateDir, "intel")

	for _, p := range projs {
		if p.ID == "" || p.Name == "" || p.ID == p.Name {
			continue
		}
		moves := [][2]string{
			{filepath.Join(memDir, p.Name+".db"), filepath.Join(memDir, p.ID+".db")},
			{filepath.Join(threadsDir, p.Name+".json"), filepath.Join(threadsDir, p.ID+".json")},
			{filepath.Join(auditDir, p.Name), filepath.Join(auditDir, p.ID)},
			{filepath.Join(intelDir, p.Name), filepath.Join(intelDir, p.ID)},
		}
		for _, m := range moves {
			if err := renameIfNeeded(m[0], m[1]); err != nil {
				return fmt.Errorf("migrate %s -> %s: %w", m[0], m[1], err)
			}
		}
	}
	return nil
}

// renameIfNeeded moves old to new only when old exists and new does not. If
// BOTH exist (e.g. an ID-keyed db was created before the migration ran), the
// legacy Name-keyed path is intentionally left in place rather than deleted —
// migration must be non-destructive. Such orphans are never read again (all
// reads now key by ID); we log them so the user can reconcile by hand.
func renameIfNeeded(old, new string) error {
	if _, err := os.Stat(old); err != nil {
		return nil // nothing to migrate (already done or never existed)
	}
	if _, err := os.Stat(new); err == nil {
		fmt.Fprintf(os.Stderr, "[styx] migration: both %s and %s exist; leaving legacy copy (delete manually if unneeded)\n", old, new)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(new), 0o755); err != nil {
		return err
	}
	return os.Rename(old, new)
}
```

> **Design note (orphaned legacy state):** the migration is deliberately non-destructive. The common path (old exists, new absent) renames cleanly. The rare both-exist case leaves the legacy Name-keyed copy on disk — it is never read again but is not auto-deleted, to guarantee no data loss on a re-run. This is the intentional trade-off behind "idempotent and safe to re-run."

- [ ] **Step 4: Wire the migration to run lazily.** Call `config.MigrateProjectState` once at startup after the registry is loaded. The cleanest hook is `ensureFirstRun` in `cmd/styx/dispatch.go` (runs before every dispatch) **and** as an explicit `styx doctor` step. In `ensureFirstRun`, after config is seeded, add:

```go
	if projs, err := config.LoadProjects(); err == nil {
		if err := config.MigrateProjectState(projs); err != nil {
			fmt.Fprintf(os.Stderr, "[styx] state migration warning: %v\n", err)
		}
	}
```

(Migration failure is a warning, not a hard error — it must never brick startup. Do not swallow it silently; surface to stderr.) Also add a line to the `doctor` flow that calls it explicitly and reports what moved.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/config/ -run TestMigrateProjectStateIdempotent -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 6: Commit**

```bash
git add internal/config/migrate_state.go internal/config/migrate_state_test.go cmd/styx/dispatch.go cmd/styx/doctor.go
git commit -m "feat(config): idempotent migration of name-keyed state to project ID"
```

### Task 2.4: `styx project scan [root] [--depth N]`

**Files:**
- Create: `cmd/styx/scan.go`
- Test: `cmd/styx/scan_test.go`
- Modify: `cmd/styx/project.go` (add `scan` subcommand case)

**Interfaces:**
- Consumes: `project.CurrentFrom` / `project.Register` / `config.LoadProjects`.
- Produces: `func cmdProjectScan(args []string) error`; walks down from `root` (default `~`), finds git roots up to `--depth` (default 4), prunes `node_modules`/`vendor`/`.git`, does not descend into a repo once found, and bulk-registers new ones.

- [ ] **Step 1: Write the failing test** — create `cmd/styx/scan_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/config"
)

func gitInitScan(t *testing.T, dir string) {
	t.Helper()
	_ = os.MkdirAll(dir, 0o755)
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v (%s)", dir, err, out)
	}
}

func TestScanFindsReposPrunesVendored(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	gitInitScan(t, filepath.Join(root, "api"))
	gitInitScan(t, filepath.Join(root, "web"))
	// A nested repo inside a found repo must NOT be descended into.
	gitInitScan(t, filepath.Join(root, "api", "subrepo"))
	// A repo inside node_modules must be pruned.
	gitInitScan(t, filepath.Join(root, "web", "node_modules", "pkg"))

	if err := cmdProjectScan([]string{root}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	projs, err := config.LoadProjects()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, p := range projs {
		got[p.Path] = true
	}
	if !got[filepath.Join(root, "api")] || !got[filepath.Join(root, "web")] {
		t.Errorf("expected api and web registered, got %v", got)
	}
	if got[filepath.Join(root, "api", "subrepo")] {
		t.Errorf("descended into nested repo")
	}
	if got[filepath.Join(root, "web", "node_modules", "pkg")] {
		t.Errorf("did not prune node_modules")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/styx/ -run TestScanFindsReposPrunesVendored -v`
Expected: FAIL — `undefined: cmdProjectScan`.

- [ ] **Step 3: Implement** — create `cmd/styx/scan.go`:

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ishaanbatra/styx/internal/config"
	"github.com/ishaanbatra/styx/internal/project"
)

var scanPrune = map[string]bool{
	"node_modules": true, "vendor": true, ".git": true,
	".venv": true, "venv": true, "dist": true, "build": true,
}

// cmdProjectScan walks down from root (default ~) up to --depth levels, finds
// git roots, prunes vendored/build dirs, does not descend into a repo once
// found, and bulk-registers any new ones via project.CurrentFrom.
func cmdProjectScan(args []string) error {
	root := ""
	depth := 4
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--depth":
			if i+1 < len(args) {
				fmt.Sscanf(args[i+1], "%d", &depth)
				i++
			}
		default:
			if root == "" {
				root = args[i]
			}
		}
	}
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("home dir: %w", err)
		}
		root = home
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve scan root %q: %w", root, err)
	}

	before, err := config.LoadProjects()
	if err != nil {
		return fmt.Errorf("load registry: %w", err)
	}
	known := map[string]bool{}
	for _, p := range before {
		known[p.Path] = true
	}

	found := 0
	walk(abs, abs, depth, func(repo string) {
		if known[repo] {
			return
		}
		p, err := project.CurrentFrom(repo) // registers + persists atomically
		if err != nil {
			fmt.Fprintf(os.Stderr, "[styx] skip %s: %v\n", repo, err)
			return
		}
		known[repo] = true
		found++
		logStatus("registered %s (%s) at %s", p.Name, p.Language, p.Path)
	})
	logStatus("scan complete: %d new project(s) registered", found)
	return nil
}

// walk descends from dir, invoking onRepo for each git root and NOT descending
// into a repo once found. Bounded by maxDepth levels below base.
func walk(base, dir string, maxDepth int, onRepo func(repo string)) {
	if depthOf(base, dir) > maxDepth {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		onRepo(dir)
		return // do not descend into a repo
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || scanPrune[e.Name()] {
			continue
		}
		walk(base, filepath.Join(dir, e.Name()), maxDepth, onRepo)
	}
}

func depthOf(base, dir string) int {
	rel, err := filepath.Rel(base, dir)
	if err != nil || rel == "." {
		return 0
	}
	return len(filepath.SplitList(rel)) + countSep(rel)
}

func countSep(p string) int {
	n := 0
	for _, r := range p {
		if r == filepath.Separator {
			n++
		}
	}
	return n
}
```

- [ ] **Step 4: Wire the subcommand** — in `cmd/styx/project.go`'s `cmdProject` switch, add:

```go
	case "scan":
		return cmdProjectScan(args[1:])
```

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/styx/ -run TestScanFindsReposPrunesVendored -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/styx/scan.go cmd/styx/scan_test.go cmd/styx/project.go
git commit -m "feat(project): styx project scan — bounded walk-down repo discovery"
```

### Task 2.5: WAL + busy_timeout on the budget sqlite

**Files:**
- Modify: `internal/budget/budget.go` (`New` line 99 DSN)
- Test: `internal/budget/budget_test.go` (append a concurrent-writers test)

- [ ] **Step 1: Write the failing test** — append to `internal/budget/budget_test.go`:

```go
func TestConcurrentWritersNoLock(t *testing.T) {
	dir := t.TempDir()
	tr, err := New(filepath.Join(dir, "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	const writers = 8
	const each = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := tr.Record(context.Background(), Event{
					Channel: "claude", Verb: "thread", Model: "haiku", Success: true,
				}); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Record errored (database locked?): %v", err)
	}
}
```

Ensure `"sync"` is imported in the test file.

- [ ] **Step 2: Run the test to verify it can fail without WAL.** With the bare DSN this can intermittently produce "database is locked" under contention.

Run: `go test ./internal/budget/ -run TestConcurrentWritersNoLock -count=5 -v`
Expected: may FAIL intermittently with `database is locked` (proving the gap). If it passes by luck, proceed — the DSN change below makes it deterministic.

- [ ] **Step 3: Implement** — in `internal/budget/budget.go` `New` (line 99), change the DSN:

```go
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
```

(Verified against `modernc.org/sqlite@v1.28.0`: query params after the first `?` are applied as `PRAGMA` per connection even for a bare path. `busy_timeout` is in ms.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/budget/ -run TestConcurrentWritersNoLock -count=5 -v`
Expected: PASS on every iteration.

- [ ] **Step 5: Commit**

```bash
git add internal/budget/budget.go internal/budget/budget_test.go
git commit -m "feat(budget): WAL journal mode + busy_timeout for multi-process safety"
```

### Task 2.6: Phase 2 docs (drift contract)

**Files:**
- Modify: `docs/ARCHITECTURE.md` (Projects & paths: stable ID + scan; On-disk layout: ID-keyed dirs; Budget: WAL; bump `last_verified`)
- Modify: `README.md` (`project scan`)

- [ ] **Step 1: Update `docs/ARCHITECTURE.md`.** In "Projects & paths", document `config.Project.ID` (stable 12-hex path hash), `ProjectID`, the load-time backfill, and `project scan` (bounded walk-down discovery, pruning, no descend-into-repo). In "On-disk layout", change the per-project paths to show the `<id>` key:

```
~/.config/styx/state/memory/<id>.db          per-project memory sqlite (id = ProjectID(path))
~/.config/styx/state/memory/global.db        shared cross-project memory
~/.config/styx/state/audit/<id>/             per-project audit JSONL stream
~/.config/styx/state/threads/<id>.json       per-project agent threads
~/.config/styx/state/intel/<id>/index.json   per-project codebase intel
```

Note the idempotent `MigrateProjectState` (lazy on startup + `doctor`). In "Budget", note the WAL+busy_timeout DSN for concurrent processes, and record the spec §1e confirmation: `projects.toml` (`config.SaveProjects`) and the models cache (`modelsync.Cache.Save`) already use atomic tmp+rename, and the per-repo `internal/pipeline/lock.go` lock already makes same-repo cross-terminal runs safe — only the shared budget DB needed the WAL change. Bump `last_verified:` to 2026-06-18.

- [ ] **Step 2: Update `README.md`.** Change the `project ls/add/rm/rename` verb-table row to include `scan`, and add a row:

```markdown
| `project scan [root] [--depth N]` | Walk down from `root` (default `~`), find git repos, bulk-register them (prunes node_modules/vendor; depth default 4) |
```

In the Configuration section, change the per-project state paths to note they are keyed by stable project id.

- [ ] **Step 3: Commit**

```bash
git add docs/ARCHITECTURE.md README.md
git commit -m "docs(project): document stable ID, state re-key/migration, project scan, WAL"
```

---

## Phase 3 — Run-id + per-event project tag on budget events

### Task 3.1: Add `Project` + `RunID` to budget events (additive sqlite migration)

**Files:**
- Modify: `internal/budget/budget.go` (`Event` lines 41–49; `New` migration block lines 107–114; `Record` INSERT lines 170–182)
- Test: `internal/budget/budget_test.go` (append a column-migration + round-trip test)

**Interfaces:**
- Produces: `budget.Event.Project string` and `budget.Event.RunID string`; new `usage.project` and `usage.run_id` columns. Backward-compatible with existing `usage.db` (additive `ALTER TABLE`, idempotent).

- [ ] **Step 1: Write the failing test** — append to `internal/budget/budget_test.go`:

```go
func TestProjectAndRunIDColumnsMigrateAndPersist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "usage.db")
	tr, err := New(p)
	if err != nil {
		t.Fatal(err)
	}
	tr.Close()
	tr2, err := New(p) // reopen: ALTERs must be idempotent
	if err != nil {
		t.Fatalf("reopen after migration: %v", err)
	}
	defer tr2.Close()
	if err := tr2.Record(context.Background(), Event{
		Channel: "claude", Verb: "thread", Model: "opus",
		Project: "abc123def456", RunID: "20260618-101500-fix", Success: true,
	}); err != nil {
		t.Fatalf("record with project/run_id: %v", err)
	}
	// Assert the columns persisted.
	var gotProject, gotRun string
	row := tr2.db.QueryRowContext(context.Background(),
		`SELECT project, run_id FROM usage ORDER BY ts DESC LIMIT 1`)
	if err := row.Scan(&gotProject, &gotRun); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if gotProject != "abc123def456" || gotRun != "20260618-101500-fix" {
		t.Errorf("got (%q,%q), want (abc123def456, 20260618-101500-fix)", gotProject, gotRun)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/budget/ -run TestProjectAndRunIDColumnsMigrate -v`
Expected: FAIL — `Event has no field Project` / `no such column: project`.

- [ ] **Step 3: Implement.** In `internal/budget/budget.go`, extend `Event` (lines 41–49):

```go
type Event struct {
	Channel   string
	Verb      string
	Model     string
	TokensIn  int
	TokensOut int
	Success   bool
	ErrorKind string
	Project   string // resolved project ID ("" = none)
	RunID     string // per-session / per-verb run correlation id ("" = none)
}
```

In `New`, immediately after the existing `model` column ALTER (line 114), add two more idempotent ALTERs mirroring the exact pattern:

```go
	for _, col := range []string{
		`ALTER TABLE usage ADD COLUMN project TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE usage ADD COLUMN run_id TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := db.ExecContext(context.Background(), col); err != nil {
			if !strings.Contains(err.Error(), "duplicate column name") {
				db.Close()
				return nil, fmt.Errorf("migrate usage columns: %w", err)
			}
		}
	}
```

In `Record` (lines 175–177), add the two columns to the INSERT:

```go
	_, err := t.db.ExecContext(ctx,
		`INSERT INTO usage (ts, channel, verb, model, tokens_in, tokens_out, success, error_kind, project, run_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), e.Channel, e.Verb, e.Model, e.TokensIn, e.TokensOut, successInt, e.ErrorKind, e.Project, e.RunID)
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/budget/ -v && go build ./...`
Expected: PASS. (All existing `Record`/`Event` call sites compile unchanged — the new fields default to `""`.)

- [ ] **Step 5: Commit**

```bash
git add internal/budget/budget.go internal/budget/budget_test.go
git commit -m "feat(budget): per-event project tag + run-id columns (additive migration)"
```

### Task 3.2: Mint run-ids and populate project/run tags at every Record call site

**Files:**
- Modify: `internal/agent/manager.go` (add `ProjectID`/`RunID` fields; `record` lines 110–129)
- Modify: `cmd/styx/repl.go` (mint a session run-id; set `Manager.RunID`/`ProjectID`)
- Modify: `cmd/styx/review.go` (`runReviewSynthesized` signature + Record calls at lines 57, 95)
- Modify: `cmd/styx/grunt.go` (`sendWithFallback` signature + Record at line 80)
- Modify: `cmd/styx/plan.go` (line 92) and `cmd/styx/auto.go` (lines 184, 264) — the other callers of the two shared helpers
- Test: `internal/agent/manager_test.go` (append) — assert the thread Record carries project + run id

**Interfaces:**
- Consumes: `pipeline.NewRunID(string) string` (existing, `internal/pipeline/state.go:59`); `config.ProjectID` (Task 2.1).
- Produces: `agent.Manager.ProjectID string` + `agent.Manager.RunID string` threaded into `record`; `runReviewSynthesized` gains a `runID string` param (project id derived internally from `projectPath`); `sendWithFallback` gains a `projectID string` param (run id minted internally from `req.Verb`).

> **Why the verb-path is not a one-line edit:** the two `budget.Record` calls live in **shared helpers**, not the verb functions. `review.go:57` and `review.go:95` are inside `runReviewSynthesized(a, ctx, prog, projectPath, diff)` (review.go:39), shared with the auto review stage (`auto.go:264`). `grunt.go:80` is inside `sendWithFallback(a, ctx, req, cr, raw)` (grunt.go:61), shared by `cmdOneShot` (grunt.go:41), `plan.go:92`, and `auto.go:184`. We thread the tag in through new parameters and resolve it in each caller — never by adding a second `resolveGlobalTarget` inside the helper (which could resolve a *different* project than `projectPath` when `--project`/`--dir` differ from cwd, and would mis-attribute the auto pipeline's events).

- [ ] **Step 1: Write the failing test** — append to `internal/agent/manager_test.go` (mirror `newManagerFixture`). The test opens the usage db directly (a second `sql.Open` to the same file is safe under WAL; the `budget` import already registers the `sqlite` driver) so no new accessor on `budget.Tracker` is needed:

```go
func TestDispatchRecordsProjectAndRunID(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.db")
	bud, err := budget.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer bud.Close()
	threads, _ := LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	m := &Manager{
		Project:   config.Project{ID: "pid123", Name: "proj", Path: dir},
		ProjectID: "pid123",
		RunID:     "run-xyz",
		Threads:   threads,
		Adapters:  map[string]Adapter{"claude": &ClaudeAdapter{BinPath: fakeBin(t)}},
		Budget:    bud,
		Timeout:   10 * time.Second,
	}
	t.Setenv("FAKEAGENT_TEXT", "ok")
	if _, err := m.Dispatch(context.Background(), DispatchSpec{Thread: "claude", CLI: "claude", Message: "hi"}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Read the row back via an independent connection (driver registered by the budget import).
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var project, runID string
	row := db.QueryRowContext(context.Background(),
		`SELECT project, run_id FROM usage ORDER BY ts DESC LIMIT 1`)
	if err := row.Scan(&project, &runID); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if project != "pid123" || runID != "run-xyz" {
		t.Errorf("got (%q,%q), want (pid123, run-xyz)", project, runID)
	}
}
```

Add `"database/sql"` to the test file's imports (the `sqlite` driver is registered transitively via the existing `budget` import).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agent/ -run TestDispatchRecordsProjectAndRunID -v`
Expected: FAIL — `Manager has no field ProjectID/RunID`.

- [ ] **Step 3: Implement.** In `internal/agent/manager.go`, add to the `Manager` struct (lines 32–44):

```go
	ProjectID string // stable id of the bound project; tags budget events
	RunID     string // session run-id; tags budget events
```

In `record` (lines 120–128), populate the new Event fields:

```go
	_ = m.Budget.Record(ctx, budget.Event{
		Channel:   spec.CLI,
		Verb:      "thread",
		Model:     spec.Model,
		TokensIn:  res.InputTokens,
		TokensOut: res.OutputTokens,
		Success:   sendErr == nil,
		ErrorKind: kind,
		Project:   m.ProjectID,
		RunID:     m.RunID,
	})
```

In `cmd/styx/repl.go` `newREPLSession`, mint a session run-id and set the Manager fields. Just before the `mgr := &agent.Manager{...}` literal (line 445), add:

```go
	runID := pipeline.NewRunID("repl-" + proj.Name)
```

and inside the literal add `ProjectID: proj.ID,` and `RunID: runID,`. Add `"github.com/ishaanbatra/styx/internal/pipeline"` to imports.

**Review path — thread the tag through the shared helper.** Change `runReviewSynthesized` (review.go:39) to take a `runID` param and derive the project id from the `projectPath` it already receives:

```go
func runReviewSynthesized(a *app, ctx context.Context, prog *progress.Tracker, runID, projectPath, diff string) (string, error) {
	projectID := config.ProjectID(projectPath)
	// ... existing body ...
```

In both `budget.Event{...}` literals (lines 57 and 95) set `Project: projectID, RunID: runID,`. Update the two callers:
- `cmd/styx/review.go:28` (`cmdReview`): `proj` is already resolved (with checked error) at review.go:17 by Task 1.4. Mint `runID := pipeline.NewRunID("review")` and call `runReviewSynthesized(a, context.Background(), a.progress, runID, proj.Path, diff)`.
- `cmd/styx/auto.go:264` (auto review stage): pass the pipeline's run id in scope — `runReviewSynthesized(a, ctx, progress.Quiet(), r.State.RunID, proj.Path, diff)` (use the `*pipeline.Runner` field name actually in scope at that line; it is the run already minted for the pipeline).

Add `"github.com/ishaanbatra/styx/internal/config"` to `review.go` imports if not present.

**Grunt/fallback path — thread a `projectID` param through the shared helper.** Change `sendWithFallback` (grunt.go:61) to take a `projectID` param and mint the run id internally (one per invocation, shared across its fallback attempts):

```go
func sendWithFallback(a *app, ctx context.Context, projectID string, req router.Request, cr channel.Request, raw bool) (channel.Response, router.ChannelModel, error) {
	runID := pipeline.NewRunID(req.Verb)
	// ... existing body ...
```

In the `budget.Event{...}` literal (line 80) set `Project: projectID, RunID: runID,`. Update **all three** callers to pass the resolved project id:
- `cmd/styx/grunt.go:41` (`cmdOneShot`): `proj` is resolved at grunt.go:30 (best-effort `proj, _ :=` is the existing tolerance). Pass `proj.ID`.
- `cmd/styx/plan.go:92`: `proj` is resolved (checked) at plan.go:24 by Task 1.4. Pass `proj.ID`.
- `cmd/styx/auto.go:184`: `proj` is in scope. Pass `proj.ID`.

Add `"github.com/ishaanbatra/styx/internal/pipeline"` to `grunt.go` imports.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/ -run 'TestDispatchRecordsProjectAndRunID' -v && go build ./...`
Expected: PASS; build clean. Then `go test ./internal/agent/ ./internal/budget/ ./cmd/styx/` to confirm the shared-helper signature changes compile everywhere and nothing else broke.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/manager.go cmd/styx/repl.go cmd/styx/review.go cmd/styx/grunt.go cmd/styx/plan.go cmd/styx/auto.go internal/agent/manager_test.go
git commit -m "feat(budget): tag usage events with project id + run-id at every call site"
```

### Task 3.3: Resume-contract guard — `auto --resume` stays loadable

**Files:**
- Modify: `internal/pipeline/state.go` (`State` struct lines 33–43; `NewState`; `LoadState`)
- Test: `internal/pipeline/state_test.go` (append a version-less load test)

This task makes the additive forward-compat guarantee explicit, since Phase 1 rerouted `cmdAuto` through `resolveGlobalTarget` and run-ids now flow through the system. No required field changes; only an additive, `omitempty` version field with a load-time normalization.

- [ ] **Step 1: Write the failing test** — append to `internal/pipeline/state_test.go`:

```go
func TestLoadStateVersionlessStillLoads(t *testing.T) {
	dir := t.TempDir()
	// A pre-version state.json (no "version" key) must load without error.
	legacy := `{"run_id":"20260101-000000-x","goal":"g","status":"running","current_stage":2,"branch":"styx/x","stages":[{"id":1,"name":"research","status":"completed"},{"id":2,"name":"intel","status":"running"}]}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState legacy: %v", err)
	}
	if s.RunID != "20260101-000000-x" || s.CurrentStage != 2 {
		t.Errorf("legacy state misread: %+v", s)
	}
	if s.Version != StateVersion {
		t.Errorf("Version not normalized: got %d want %d", s.Version, StateVersion)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/pipeline/ -run TestLoadStateVersionlessStillLoads -v`
Expected: FAIL — `undefined: StateVersion` / `s.Version undefined`.

- [ ] **Step 3: Implement.** In `internal/pipeline/state.go`, add a package const near the status consts:

```go
// StateVersion is the current state.json schema version. Old files (no version
// key) load as version 0 and are normalized on read; the field is additive.
const StateVersion = 1
```

Add the field to `State` (after `RunID`, line 34):

```go
	Version int `json:"version,omitempty"`
```

Set it in `NewState` (the scaffold, lines 101–119): add `Version: StateVersion,` to the returned literal. Normalize on read — in `LoadState`, between the `json.Unmarshal` (line 95) and `return` (line 97), add:

```go
	if s.Version == 0 {
		s.Version = StateVersion
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/pipeline/ -v && go build ./...`
Expected: PASS (existing round-trip + resume tests unaffected; new legacy-load test green).

- [ ] **Step 5: Commit**

```bash
git add internal/pipeline/state.go internal/pipeline/state_test.go
git commit -m "feat(pipeline): additive State.Version with read normalization; resume stays loadable"
```

### Task 3.4: Phase 3 docs (drift contract)

**Files:**
- Modify: `docs/ARCHITECTURE.md` (Budget section: project/run_id columns + run-id minting; Pipelines: State.Version; bump `last_verified`)

- [ ] **Step 1: Update `docs/ARCHITECTURE.md`.** In "Budget", document that `usage` rows now carry `project` (resolved project id) and `run_id` (minted per REPL session via `pipeline.NewRunID` and per verb invocation), the seam the self-improvement spec will read back. In "Pipelines", note `State.Version` (additive, normalized on load; old `state.json` stays loadable). Bump `last_verified:` to 2026-06-18.

- [ ] **Step 2: Commit**

```bash
git add docs/ARCHITECTURE.md
git commit -m "docs(budget): document project/run-id usage tags and State.Version"
```

---

## Phase 4 — Brain & channel/agent multi-root

### Task 4.1: Brain `Dispatch.Project` / `ExtraRoots` + `ActionSchema`

**Files:**
- Modify: `internal/brain/action.go` (`Dispatch` lines 31–39; `ActionSchema` lines 128–156)
- Test: `internal/brain/action_test.go` (append)

**Interfaces:**
- Produces: `brain.Dispatch.Project string` (json `project,omitempty`) and `brain.Dispatch.ExtraRoots []string` (json `extra_roots,omitempty`). `ActionSchema` gains the two dispatch-item properties. `Valid()` stays structural (empty or any non-empty project is structurally valid; registry resolution + escalation happens in the REPL — Phase 5 Task 5.2).

- [ ] **Step 1: Write the failing tests** — append to `internal/brain/action_test.go`:

```go
func TestDispatchProjectAndExtraRootsRoundTrip(t *testing.T) {
	raw := `{"action":"dispatch","confidence":0.9,"dispatches":[
		{"thread":"claude","message":"trace the upload","project":"ai-ta-teacher-ui","extra_roots":["/repos/ai-ta-backend"]}
	]}`
	var a Action
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		t.Fatal(err)
	}
	d := a.Dispatches[0]
	if d.Project != "ai-ta-teacher-ui" {
		t.Errorf("Project = %q", d.Project)
	}
	if len(d.ExtraRoots) != 1 || d.ExtraRoots[0] != "/repos/ai-ta-backend" {
		t.Errorf("ExtraRoots = %v", d.ExtraRoots)
	}
}

func TestProjectAndExtraRootsInSchema(t *testing.T) {
	for _, want := range []string{`"project"`, `"extra_roots"`} {
		if !bytes.Contains(ActionSchema, []byte(want)) {
			t.Errorf("ActionSchema missing %s", want)
		}
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/brain/ -run 'TestDispatchProjectAndExtraRoots|TestProjectAndExtraRootsInSchema' -v`
Expected: FAIL — `Dispatch has no field Project` / schema missing keys.

- [ ] **Step 3: Implement.** In `internal/brain/action.go`, extend the `Dispatch` struct (lines 31–39):

```go
type Dispatch struct {
	Thread     string    `json:"thread"`
	Model      string    `json:"model,omitempty"`
	Message    string    `json:"message"`
	Project    string    `json:"project,omitempty"`     // primary repo name; cwd + commit target ("" = focus)
	ExtraRoots []string  `json:"extra_roots,omitempty"` // additional repo names, attached via --add-dir
	CLIOptions []string  `json:"cli_options,omitempty"`
	Rationale  string    `json:"rationale,omitempty"`
	Risk       RiskLevel `json:"risk,omitempty"`
}
```

In `ActionSchema` (lines 139–146), add the two properties inside `dispatches.items.properties` (keep them out of `required`, which stays `["thread","message"]`):

```
          "project": {"type": "string"},
          "extra_roots": {"type": "array", "items": {"type": "string"}},
```

(Insert alongside `cli_options`, matching the existing space indentation of that block.)

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/brain/ -v && go build ./...`
Expected: PASS (existing `TestActionValid`, `TestActionSchemaIsValidJSON`, etc. unaffected — `Valid()` signature unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/brain/action.go internal/brain/action_test.go
git commit -m "feat(brain): Dispatch.Project + ExtraRoots in struct and ActionSchema"
```

### Task 4.2: Inject the project registry into the brain prompt

**Files:**
- Modify: `internal/brain/brain.go` (`Turn` struct lines 17–24)
- Modify: `internal/brain/prompt.go` (`BuildPrompt` lines 82–106; insert in the user-prompt builder after the MemoryHits block at line 102)
- Modify: `cmd/styx/repl.go` (Turn construction lines 61–67; add a `renderProjects` helper)
- Test: `internal/brain/prompt_test.go` (extend)

**Interfaces:**
- Produces: `brain.Turn.BoundProjects []string` and `brain.Turn.KnownProjects []string` (pre-rendered one-liners). `BuildPrompt` emits a "Bound projects:" / "Known projects:" block in the user prompt after MemoryHits and before the utterance. The brain package stays free of `internal/config`/`internal/project` (rendering happens in `cmd/styx`).

- [ ] **Step 1: Write the failing test** — extend `internal/brain/prompt_test.go` `TestBuildPrompt`'s `Turn` literal (lines 27–33) to add:

```go
		BoundProjects: []string{"ai-ta-backend (python): embedding + RAG service [bound]"},
		KnownProjects: []string{"ai-ta-teacher-ui (typescript): teacher upload UI"},
```

and add assertions after `sys, user := BuildPrompt(turn)`:

```go
	for _, want := range []string{
		"ai-ta-backend (python): embedding + RAG service [bound]",
		"ai-ta-teacher-ui (typescript): teacher upload UI",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt missing project line %q:\n%s", want, user)
		}
	}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/brain/ -run TestBuildPrompt -v`
Expected: FAIL — `Turn has no field BoundProjects`.

- [ ] **Step 3: Implement.** In `internal/brain/brain.go`, add to `Turn` (after `MemoryHits`, line 23):

```go
	BoundProjects []string // one-liner per repo currently bound to the session
	KnownProjects []string // one-liner per other registered repo
```

In `internal/brain/prompt.go` `BuildPrompt`, after the MemoryHits block (line 102) and before the `User utterance:` write (line 104), add (use tabs, matching the file):

```go
	if len(t.BoundProjects) > 0 {
		u.WriteString("Bound projects (the session is working in these):\n" + strings.Join(t.BoundProjects, "\n") + "\n\n")
	}
	if len(t.KnownProjects) > 0 {
		u.WriteString("Known projects (name one in `project`/`extra_roots` to bring it in):\n" + strings.Join(t.KnownProjects, "\n") + "\n\n")
	}
```

- [ ] **Step 4: Render the registry in `cmd/styx/repl.go`.** Add a helper:

```go
// renderProject formats one registry entry into a brain-facing one-liner.
func renderProject(p config.Project, bound bool) string {
	desc := p.Description
	if desc == "" {
		desc = filepath.Base(p.Path)
	}
	line := fmt.Sprintf("%s (%s): %s", p.Name, p.Language, desc)
	if bound {
		line += " [bound]"
	}
	return line
}
```

In the `turn` method's `brain.Turn{...}` construction (lines 61–67), populate the two slices from the session's bound set and the registry (Phase 5 supplies `s.bound`; for now, until 5.x lands, populate `BoundProjects` from the single focus project and `KnownProjects` from `project.List()` minus the bound one). Concretely:

```go
		BoundProjects: s.renderBoundProjects(),
		KnownProjects: s.renderKnownProjects(),
```

and add the two methods (single-project version now; Phase 5 Task 5.1 generalizes them over the bound set):

```go
func (s *replSession) renderBoundProjects() []string {
	return []string{renderProject(s.proj, true)}
}

func (s *replSession) renderKnownProjects() []string {
	regs, err := project.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range regs {
		if p.ID == s.proj.ID {
			continue
		}
		out = append(out, renderProject(p, false))
	}
	return out
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/brain/ ./cmd/styx/ && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 6: Commit**

```bash
git add internal/brain/brain.go internal/brain/prompt.go cmd/styx/repl.go internal/brain/prompt_test.go
git commit -m "feat(brain): inject bound + known project registry into the turn prompt"
```

### Task 4.3: Channel `Request.ExtraRoots` → `--add-dir` (claude, codex)

**Files:**
- Modify: `internal/channel/channel.go` (`Request` lines 17–27)
- Modify: `internal/channel/claude/claude.go` (`claudeArgs` lines 60–74)
- Modify: `internal/channel/codex/codex.go` (`codexArgs` lines 51–69)
- Modify: `internal/channel/agy/agy.go` (loop after line 52)
- Test: `internal/channel/{claude,codex,agy}/*_test.go`

**Interfaces:**
- Produces: `channel.Request.ExtraRoots []string`. Each adapter emits `--add-dir <root>` per non-empty root. `WorkingDir`/`cmd.Dir` is unchanged (ExtraRoots is additive — the process cwd stays `WorkingDir`).

- [ ] **Step 1: Write the failing tests.**

`internal/channel/codex/codex_test.go` (strongest — exact order via `reflect.DeepEqual`):

```go
func TestCodexArgs_ExtraRoots(t *testing.T) {
	got := codexArgs(channel.Request{Prompt: "hi", ExtraRoots: []string{"/a", "/b"}})
	want := []string{"exec", "--add-dir", "/a", "--add-dir", "/b", "hi"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}
```

`internal/channel/claude/claude_test.go`:

```go
func TestClaudeArgs_ExtraRoots(t *testing.T) {
	got := claudeArgs(channel.Request{Prompt: "hi", ExtraRoots: []string{"/a", "/b"}})
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--add-dir /a") || !strings.Contains(joined, "--add-dir /b") {
		t.Errorf("missing --add-dir roots in %q", joined)
	}
}
```

`internal/channel/agy/agy_test.go` (extend the env-file capture pattern):

```go
func TestSend_AppendsExtraRoots(t *testing.T) {
	dir := fakeAgy(t, `echo "$@" > "$STYX_TEST_ARGS_FILE"; echo ok`)
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("STYX_TEST_ARGS_FILE", argsFile)
	c := New()
	if _, err := c.Send(context.Background(), channel.Request{Prompt: "hi", ExtraRoots: []string{"/a", "/b"}}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(argsFile)
	got := string(b)
	if !contains(got, "/a") || !contains(got, "/b") {
		t.Errorf("expected both roots in args, got: %s", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/channel/... -run ExtraRoots -v`
Expected: FAIL — `Request has no field ExtraRoots`.

- [ ] **Step 3: Implement.** In `internal/channel/channel.go`, add to `Request` (after `WorkingDir`, line 25):

```go
	ExtraRoots  []string     // additional repo roots, attached via the CLI's --add-dir (cross-repo work)
```

In `internal/channel/claude/claude.go` `claudeArgs`, before `return args` (line 73), append:

```go
	for _, root := range req.ExtraRoots {
		if root != "" {
			args = append(args, "--add-dir", root)
		}
	}
```

In `internal/channel/codex/codex.go` `codexArgs`, after the `--sandbox` block (after line 66) and **before** the prompt is appended (line 67), insert:

```go
	for _, root := range req.ExtraRoots {
		if root != "" {
			args = append(args, "--add-dir", root)
		}
	}
```

(Order matters: `--add-dir` is an `exec` subcommand flag, so it must sit after `exec` and before the prompt positional.)

In `internal/channel/agy/agy.go`, after the existing WorkingDir `--add-dir` block (after line 52), add:

```go
	for _, root := range req.ExtraRoots {
		if root != "" {
			args = append(args, "--add-dir", root)
		}
	}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/channel/... -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/channel.go internal/channel/claude/ internal/channel/codex/ internal/channel/agy/
git commit -m "feat(channel): Request.ExtraRoots -> --add-dir for claude/codex/agy"
```

### Task 4.4: Agent `DispatchSpec.ExtraRoots` → `--add-dir`; fix codex agent ArgsFn order

**Files:**
- Modify: `internal/agent/manager.go` (`DispatchSpec` lines 20–28; `Dispatch` lines 46–83)
- Modify: `internal/agent/adapter.go` (`NewCodexAdapter` ArgsFn — place `extra` after `exec`)
- Modify: `cmd/styx/repl.go` (populate `DispatchSpec.ExtraRoots` — placeholder now; Phase 5 Task 5.2 fills it from resolved repos)
- Test: `internal/agent/manager_test.go` and `internal/agent/adapter_test.go` (append)

**Interfaces:**
- Produces: `agent.DispatchSpec.ExtraRoots []string` (absolute paths). `Manager.Dispatch` renders them to `--add-dir <root>` pairs and merges into the `extra` slice passed to `Runner.Send`. The codex agent ArgsFn is reordered so merged `--add-dir` flags land after `exec` (valid for `codex exec`).

- [ ] **Step 1: Write the failing tests.**

`internal/agent/adapter_test.go` (codex order):

```go
func TestCodexAdapterPlacesExtraAfterExec(t *testing.T) {
	a := NewCodexAdapter()
	got := a.BuildArgs("hello", "", "opus", []string{"--add-dir", "/a"}, false)
	// --add-dir must come AFTER exec and before the message.
	want := []string{"--model", "opus", "exec", "--add-dir", "/a", "hello"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args = %v, want %v", got, want)
	}
}
```

`internal/agent/manager_test.go` (ExtraRoots become --add-dir in argv via fakeagent ARGS_LOG):

```go
func TestDispatchRendersExtraRoots(t *testing.T) {
	dir := t.TempDir()
	threads, _ := LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	argsLog := filepath.Join(dir, "args.log")
	m := &Manager{
		Project:  config.Project{Name: "proj", Path: dir},
		Threads:  threads,
		Adapters: map[string]Adapter{"claude": &ClaudeAdapter{BinPath: fakeBin(t)}},
		Timeout:  10 * time.Second,
	}
	t.Setenv("FAKEAGENT_TEXT", "ok")
	t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)
	if _, err := m.Dispatch(context.Background(), DispatchSpec{
		Thread: "claude", CLI: "claude", Message: "hi",
		ExtraRoots: []string{"/repos/other"},
	}, nil); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	b, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(b), "--add-dir") || !strings.Contains(string(b), "/repos/other") {
		t.Errorf("expected --add-dir /repos/other in argv, got: %s", b)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/agent/ -run 'TestCodexAdapterPlacesExtraAfterExec|TestDispatchRendersExtraRoots' -v`
Expected: FAIL — codex order wrong (`exec` after extra) and `DispatchSpec has no field ExtraRoots`.

- [ ] **Step 3: Implement.** In `internal/agent/manager.go`, add to `DispatchSpec` (after `Extra`, line 26):

```go
	ExtraRoots []string // absolute repo roots attached via --add-dir (cross-repo dispatch)
```

In `Dispatch` (line 62), build the merged extra before the first `run.Send`:

```go
	extra := append(append([]string{}, spec.Extra...), addDirArgs(spec.ExtraRoots)...)
	res, err := run.Send(ctx, msg, spec.Model, extra, spec.ReadOnly)
```

and reuse `extra` in the crash-recovery resend (line 68). Add the helper:

```go
// addDirArgs renders extra repo roots as repeated --add-dir <root> flags.
// All three agent CLIs (claude, codex, agy) accept --add-dir.
func addDirArgs(roots []string) []string {
	var out []string
	for _, r := range roots {
		if r != "" {
			out = append(out, "--add-dir", r)
		}
	}
	return out
}
```

In `internal/agent/adapter.go` `NewCodexAdapter`, reorder the ArgsFn so `extra` lands after `exec`:

```go
		ArgsFn: func(msg, model string, extra []string) []string {
			args := []string{}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, "exec")
			args = append(args, extra...)
			return append(args, msg)
		},
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/ -v && go build ./...`
Expected: PASS (existing runner/manager/adapter tests still green — the reorder only affects where `extra` sits, and existing tests pass `extra=nil`/empty).

- [ ] **Step 5: Commit**

```bash
git add internal/agent/manager.go internal/agent/adapter.go internal/agent/manager_test.go internal/agent/adapter_test.go
git commit -m "feat(agent): DispatchSpec.ExtraRoots -> --add-dir; fix codex exec arg order"
```

### Task 4.5: Phase 4 docs (drift contract)

**Files:**
- Modify: `docs/ARCHITECTURE.md` (Brain, Channels, Agent threads sections; bump `last_verified`)

- [ ] **Step 1: Update `docs/ARCHITECTURE.md`.** In "Brain", document `Dispatch.Project`/`ExtraRoots`, the `ActionSchema` additions, and that `BuildPrompt` now injects bound + known projects so the model can map repo names. In "Channels", document `Request.ExtraRoots → --add-dir` for claude/codex/agy. In "Agent threads", document `DispatchSpec.ExtraRoots`, the `--add-dir` rendering in `Manager.Dispatch`, and the codex `exec` arg-order fix. Bump `last_verified:` to 2026-06-18.

- [ ] **Step 2: Commit**

```bash
git add docs/ARCHITECTURE.md
git commit -m "docs(brain,channel,agent): document multi-root dispatch + registry prompt"
```

---

## Phase 5 — Multi-project REPL session

### Task 5.1: Bound-project set + focus pointer + lazy bind; accessor methods

**Files:**
- Modify: `cmd/styx/repl.go` (`replSession` struct lines 30–52; `newREPLSession` lines 379–504; migrate single-project field reads)
- Modify: `cmd/styx/repl_test.go` (`newTestSession` lines 44–104)

**Interfaces:**
- Produces: a `boundProject` bundle `{proj config.Project; mgr *agent.Manager; mem *memory.Store; closers []func() error}`; `replSession.bound map[string]*boundProject` (keyed by `proj.ID`); `replSession.focus string` (the focus project's ID). Accessor methods `(s *replSession) proj() config.Project`, `mgr() *agent.Manager`, `mem() *memory.Store`, and `bind(p config.Project) (*boundProject, error)` (memoized, lazy). `glob`, `emb`, `brain`, `audit`, `tiers`, `tracker`, `runID`, `in`/`out` stay session-global on `replSession`.

- [ ] **Step 1: Replace the single-project fields with the bound set.** In `cmd/styx/repl.go`, change `replSession` (lines 32–52): remove `proj config.Project`, `mgr *agent.Manager`, `mem *memory.Store` and add the bound-set fields **plus the four per-Manager inputs that `bind` (Step 2) needs as session-global fields** (today they are locals/`a.routing.*` values inside `newREPLSession`; `bind` must read them off the session):

```go
	bound        map[string]*boundProject                              // keyed by project ID
	focus        string                                                // ID of the current-focus project
	runID        string                                                // session run-id for budget tagging
	summarize    func(ctx context.Context, text string) (string, error) // cheap local summarizer (was a local closure)
	thresholdPct float64                                               // = a.routing.Brain.ContextThresholdPct
	distillModel string                                                // = a.routing.Tiers["haiku"]
	timeout      time.Duration                                         // computed claude subprocess budget
```

In `newREPLSession` (Step 2's prose) these are populated from the existing local `summarize` closure (repl.go:461–469), `a.routing.Brain.ContextThresholdPct`, `a.routing.Tiers["haiku"]`, and the computed `timeout` (repl.go:441–444) — the same values that fed the old single `Manager` literal.

Add the bundle type and accessors:

```go
// boundProject is one repo bound to the session: its own agent Manager, threads,
// and memory store. Bound lazily on first reference.
type boundProject struct {
	proj    config.Project
	mgr     *agent.Manager
	mem     *memory.Store
	closers []func() error
}

func (s *replSession) proj() config.Project { return s.bound[s.focus].proj }
func (s *replSession) mgr() *agent.Manager  { return s.bound[s.focus].mgr }
func (s *replSession) mem() *memory.Store   { return s.bound[s.focus].mem }
```

- [ ] **Step 2: Extract the per-project wiring (repl.go:384–460) into `bind`.** Add:

```go
// bind returns the bound bundle for p, creating it lazily (memoized by ID).
// Reuses the session-global embedder, budget tracker, brain, and run-id.
func (s *replSession) bind(p config.Project) (*boundProject, error) {
	if bp, ok := s.bound[p.ID]; ok {
		return bp, nil
	}
	memDir, err := paths.MemoryDir()
	if err != nil {
		return nil, err
	}
	if err := paths.EnsureDir(memDir); err != nil {
		return nil, err
	}
	mem, err := memory.Open(filepath.Join(memDir, p.ID+".db"))
	if err != nil {
		return nil, fmt.Errorf("open project memory: %w", err)
	}
	threads, err := agent.LoadThreads(p.ID)
	if err != nil {
		mem.Close()
		return nil, err
	}
	mgr := &agent.Manager{
		Project:   p,
		ProjectID: p.ID,
		RunID:     s.runID,
		Threads:   threads,
		Adapters: map[string]agent.Adapter{
			"claude": agent.NewClaudeAdapter(),
			"codex":  agent.NewCodexAdapter(),
			"agy":    agent.NewAgyAdapter(),
		},
		Budget:       s.tracker,
		Mem:          mem,
		Emb:          s.emb,
		Summarize:    s.summarize,
		ThresholdPct: s.thresholdPct,
		DistillModel: s.distillModel,
		Timeout:      s.timeout,
	}
	mgr.OnCompact = func(name string) { s.println("↻ " + name + " thread compacted") }
	bp := &boundProject{proj: p, mgr: mgr, mem: mem, closers: []func() error{mem.Close}}
	s.bound[p.ID] = bp
	return bp, nil
}
```

(Move `summarize`, `thresholdPct`, `distillModel`, `timeout` to `replSession` fields set in `newREPLSession`, since `bind` needs them per-repo. The single `glob`, `emb`, `audit`, `tracker`, `brain` stay session-global.)

`newREPLSession` becomes: resolve the seed project via `resolveGlobalTarget("")`, mint `runID`, open `glob` + `audit` (the session-global audit stream — see Task 5.4), construct the `replSession`, then `bind` the seed project and set `s.focus = seed.ID`. The cleanup closure closes every bound project's closers + `glob` + the audit logger.

- [ ] **Step 3: Migrate all single-project reads.** Replace every `s.proj` → `s.proj()`, `s.mgr` → `s.mgr()`, `s.mem` → `s.mem()` throughout `repl.go` (turn, execute, runDispatches, runOneDispatch, saveMemoryText, endSession, slash, pipelines closures, header). The pipelines map closures (lines 483–494) must reference the focus project at call time, not a captured `proj` — change e.g. `cmdIntel(a, []string{proj.Name})` to `cmdIntel(a, []string{s.proj().Name})`.

- [ ] **Step 4: Update the test constructor.** In `cmd/styx/repl_test.go` `newTestSession` (lines 44–104), build the session via the new shape: create the bundle and bound map directly. Add a helper:

```go
func bindTestProject(t *testing.T, name string, bud *budget.Tracker) *boundProject {
	t.Helper()
	dir := t.TempDir()
	mem, err := memory.Open(filepath.Join(dir, "mem.db"))
	if err != nil {
		t.Fatal(err)
	}
	threads, _ := agent.LoadThreadsFrom(filepath.Join(dir, "threads.json"))
	fake, _ := filepath.Abs("../../testdata/fakeagent")
	p := config.Project{ID: name, Name: name, Path: dir}
	return &boundProject{
		proj: p,
		mem:  mem,
		mgr: &agent.Manager{
			Project: p, ProjectID: name, Threads: threads,
			Adapters:     map[string]agent.Adapter{"claude": &agent.ClaudeAdapter{BinPath: fake}},
			Budget:       bud, Mem: mem, Emb: replEmbedder{},
			ThresholdPct: 70, DistillModel: "haiku", Timeout: 10 * time.Second,
		},
		closers: []func() error{mem.Close},
	}
}
```

and set `s.bound = map[string]*boundProject{"testproj": bp}; s.focus = "testproj"` instead of the removed `proj`/`mgr`/`mem` literal fields. Existing tests that read `s.mem`/`s.mgr`/`s.proj` switch to the accessor methods `s.mem()`/`s.mgr()`/`s.proj()`.

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/styx/ -v && go build ./...`
Expected: PASS — all existing REPL tests (`TestTurnReply`, `TestTurnDispatch...`, `TestScriptedSession`, `TestAuditTrail`, ship-risk) green through the accessors.

- [ ] **Step 6: Commit**

```bash
git add cmd/styx/repl.go cmd/styx/repl_test.go
git commit -m "feat(repl): bound-project set + focus pointer + lazy per-repo bind"
```

### Task 5.2: Per-dispatch project routing + ExtraRoots resolution + escalate-on-unknown

**Files:**
- Modify: `cmd/styx/repl.go` (`runOneDispatch` lines 158–186)
- Test: `cmd/styx/repl_test.go` (append a two-repo dispatch test)

**Interfaces:**
- Consumes: `brain.Dispatch.Project`/`ExtraRoots` (Task 4.1), `target.Resolve`, `s.bind` (Task 5.1).
- Produces: a dispatch whose `Dispatch.Project` selects (and lazily binds) the target repo's Manager (cwd = that repo); `Dispatch.ExtraRoots` (names) resolve to absolute paths set on `DispatchSpec.ExtraRoots`. An unresolvable project/root name does not silently guess — it surfaces an error the REPL reports and asks the user about.

- [ ] **Step 1: Write the failing test** — append to `cmd/styx/repl_test.go` (mirror `TestScriptedSession`):

```go
func TestDispatchTargetsNamedRepoWithExtraRoots(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "done")

	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "ai-ta-backend", bud)
	b := bindTestProject(t, "ai-ta-teacher-ui", bud)
	// Register both so target.Resolve / extra-root resolution find them.
	if err := config.SaveProjects([]config.Project{
		{ID: "ai-ta-backend", Name: "ai-ta-backend", Path: a.proj.Path},
		{ID: "ai-ta-teacher-ui", Name: "ai-ta-teacher-ui", Path: b.proj.Path},
	}); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	s := &replSession{
		bound:   map[string]*boundProject{"ai-ta-backend": a, "ai-ta-teacher-ui": b},
		focus:   "ai-ta-backend",
		brain:   scriptedBrain{}, // returns a dispatch with Project + ExtraRoots
		emb:     replEmbedder{},
		tracker: bud,
		tiers:   map[string]string{"opus": "opus", "haiku": "haiku"},
		in:      bufio.NewReader(strings.NewReader("")),
		out:     out,
	}
	d := brain.Dispatch{
		Thread: "claude", Message: "trace upload",
		Project: "ai-ta-teacher-ui", ExtraRoots: []string{"ai-ta-backend"},
	}
	if err := s.runOneDispatch(context.Background(), d, "opus"); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// The teacher-ui thread ran; backend's did not.
	if b.mgr.Threads.Get("claude", "claude").Turns != 1 {
		t.Errorf("teacher-ui thread did not run")
	}
	if a.mgr.Threads.Get("claude", "claude").Turns != 0 {
		t.Errorf("backend thread should be untouched")
	}
}
```

(`scriptedBrain` is unused here since we call `runOneDispatch` directly; drop the brain field if the helper resists — the test drives `runOneDispatch` straight.)

Also add the **negative test** the spec §6 demands (the guarantee the structural `Valid()` displaced — "unresolved repo name from the brain → escalate, never a silent guess"): an unknown `Dispatch.Project` must surface an error and run **no** thread:

```go
func TestDispatchUnknownTargetSurfacesError(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())

	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "backend", bud)
	_ = config.SaveProjects([]config.Project{{ID: "backend", Name: "backend", Path: a.proj.Path}})

	s := &replSession{
		bound: map[string]*boundProject{"backend": a}, focus: "backend",
		emb: replEmbedder{}, tracker: bud,
		tiers: map[string]string{"opus": "opus"},
		in:    bufio.NewReader(strings.NewReader("")), out: &bytes.Buffer{},
	}
	err := s.runOneDispatch(context.Background(), brain.Dispatch{
		Thread: "claude", Message: "x", Project: "nope-not-registered",
	}, "opus")
	if err == nil || !strings.Contains(err.Error(), "unknown project") {
		t.Fatalf("want unknown-project error, got %v", err)
	}
	if !errors.Is(err, errUnresolvedRepo) {
		t.Errorf("error should wrap errUnresolvedRepo so the turn loop escalates")
	}
	if a.mgr.Threads.Get("claude", "claude").Turns != 0 {
		t.Errorf("no thread should run on an unresolved target (no silent fallback to focus)")
	}
}
```

(Add `"errors"` to the test file imports if not present.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/styx/ -run 'TestDispatchTargetsNamedRepoWithExtraRoots|TestDispatchUnknownTargetSurfacesError' -v`
Expected: FAIL — `runOneDispatch` ignores `d.Project`/`d.ExtraRoots` (runs in focus, no roots; unknown target silently uses focus).

- [ ] **Step 3: Implement.** In `cmd/styx/repl.go` `runOneDispatch` (lines 158–186), before the `s.mgr().Dispatch` call, resolve the target repo and extra roots:

```go
	// Resolve the dispatch's target repo (default = focus) and bind it lazily.
	bp := s.bound[s.focus]
	if d.Project != "" {
		p, err := target.Resolve(target.Spec{Alias: d.Project})
		if err != nil {
			return fmt.Errorf("dispatch target %q: %w", d.Project, err)
		}
		bp, err = s.bind(p)
		if err != nil {
			return err
		}
	}
	// Resolve extra-root names to absolute paths.
	var roots []string
	for _, name := range d.ExtraRoots {
		rp, err := target.Resolve(target.Spec{Alias: name})
		if err != nil {
			return fmt.Errorf("extra root %q: %w", name, err)
		}
		if _, err := s.bind(rp); err != nil {
			return err
		}
		roots = append(roots, rp.Path)
	}
```

then change the dispatch to use `bp.mgr` and pass `ExtraRoots`:

```go
	res, err := bp.mgr.Dispatch(ctx, agent.DispatchSpec{
		Thread:     d.Thread,
		CLI:        d.Thread,
		Model:      model,
		Message:    d.Message,
		Extra:      d.CLIOptions,
		ExtraRoots: roots,
		ReadOnly:   d.Risk == brain.RiskRead,
	}, s.printEvent)
```

**Escalate to the user, never guess (spec §2a/§5).** A resolution failure must not silently fall back to focus. Define a sentinel and route it to the existing `askUserRoute` so the user is asked which repo they meant (matching the spec's "escalate / ask the user"). At the top of `runOneDispatch`'s resolution block, wrap the errors with a sentinel:

```go
// errUnresolvedRepo marks a brain-named repo that did not resolve, so the turn
// loop escalates to the user instead of guessing.
var errUnresolvedRepo = errors.New("unresolved repo")
```

Return `fmt.Errorf("%w: dispatch target %q: %v", errUnresolvedRepo, d.Project, err)` (and the analogous form for an extra root). In `turn` (repl.go:55), where `brain.ErrNeedUser` is already caught and routed to `askUserRoute` (repl.go:68–70), add the same handling for `errUnresolvedRepo`:

```go
	if errors.Is(err, errUnresolvedRepo) {
		s.println("◆ " + err.Error())
		return s.askUserRoute(ctx, utterance)
	}
```

This surfaces the candidate list (the resolver error already lists registered names) **and** prompts the user, rather than dropping the turn.

Add `"github.com/ishaanbatra/styx/internal/target"` (and `"errors"` if not already imported) to `cmd/styx/repl.go`.

- [ ] **Step 4: Run the tests**

Run: `go test ./cmd/styx/ -run TestDispatchTargetsNamedRepoWithExtraRoots -v && go build ./...`
Expected: PASS; build clean. Then `go test ./cmd/styx/` to confirm no regressions.

- [ ] **Step 5: Commit**

```bash
git add cmd/styx/repl.go cmd/styx/repl_test.go
git commit -m "feat(repl): per-dispatch repo routing + extra-root resolution; escalate on unknown"
```

### Task 5.3: Session-scoped recall across all bound repos

**Files:**
- Modify: `cmd/styx/repl.go` (`turn` recall line 57; `saveMemoryText` line 296–298; `endSession` line 652–655)
- Test: `cmd/styx/repl_test.go` (append a cross-repo recall test)

**Interfaces:**
- Consumes: `memory.Recall(ctx, emb, query, k, stores ...*Store)` (already variadic).
- Produces: recall spans every bound repo's `mem` store plus `glob`; memory writes target the focus repo's store.

- [ ] **Step 1: Write the failing test** — append to `cmd/styx/repl_test.go`:

```go
func TestRecallSpansBoundRepos(t *testing.T) {
	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "A", bud)
	b := bindTestProject(t, "B", bud)
	glob, _ := memory.Open(filepath.Join(t.TempDir(), "glob.db"))

	s := &replSession{
		bound: map[string]*boundProject{"A": a, "B": b},
		focus: "A",
		glob:  glob,
		emb:   replEmbedder{},
	}
	// A fact learned in repo B.
	vec, _ := s.emb.Embed(context.Background(), "the embedding worker lives in B")
	if _, err := b.mem.Add(context.Background(), memory.Item{
		Kind: memory.KindFact, Text: "the embedding worker lives in B",
		Project: "B", Embedding: vec, Confidence: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Recall from focus A must surface B's fact.
	hits, err := s.recallAll(context.Background(), "where is the embedding worker")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.Item.Text, "embedding worker lives in B") {
			found = true
		}
	}
	if !found {
		t.Errorf("cross-repo recall did not surface B's fact: %+v", hits)
	}
}
```

(`replEmbedder` must map the query and the stored text to nearby vectors; mirror the existing `replEmbedder`/`fakeEmbedder` deterministic mapping in the test files.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/styx/ -run TestRecallSpansBoundRepos -v`
Expected: FAIL — `undefined: s.recallAll`.

- [ ] **Step 3: Implement.** Add a `recallAll` helper and use it in `turn`:

```go
// recallAll recalls across every bound repo's store plus the global store, so a
// fact learned in one repo surfaces when the focus is elsewhere.
func (s *replSession) recallAll(ctx context.Context, utterance string) ([]memory.Hit, error) {
	stores := make([]*memory.Store, 0, len(s.bound)+1)
	for _, bp := range s.bound {
		stores = append(stores, bp.mem)
	}
	stores = append(stores, s.glob)
	return memory.Recall(ctx, s.emb, utterance, 5, stores...)
}
```

In `turn` (line 57), replace the two-store recall:

```go
	hits, err := s.recallAll(ctx, utterance)
```

In `saveMemoryText` (lines 296–298) and `endSession` (lines 652–655), write to the focus store and stamp `Project: s.proj().ID` (use the ID now, not Name, so attribution matches the re-key):

```go
	if _, err := s.mem().Add(ctx, memory.Item{
		Kind: kind, Text: text, Source: "repl",
		Project: s.proj().ID, Scope: scope, Confidence: confidence, Embedding: vec,
	}); err != nil {
```

(Note: `memory.Recall` aborts on the first store error today; for cross-repo robustness this is acceptable for v1 — all bound stores are freshly opened by `bind`. Leave the existing behavior; do not silently swallow.)

- [ ] **Step 4: Run the tests**

Run: `go test ./cmd/styx/ -run TestRecallSpansBoundRepos -v && go build ./...`
Expected: PASS; build clean.

- [ ] **Step 5: Commit**

```bash
git add cmd/styx/repl.go cmd/styx/repl_test.go
git commit -m "feat(repl): session-scoped recall across all bound repos"
```

### Task 5.4: One project-tagged audit stream

**Files:**
- Modify: `internal/audit/log.go` (`Record` struct lines 28–34)
- Modify: `cmd/styx/repl.go` (`auditf` lines 343–348; audit-open in `newREPLSession`; `/audit` rendering)
- Test: `internal/audit/log_test.go` (append) and `cmd/styx/repl_test.go`

**Interfaces:**
- Produces: `audit.Record.Project string` (json `project,omitempty`). The session keeps a single `*audit.Logger`; each record is tagged with the project it touched. The audit file lives under the focus/seed project's ID dir (already re-keyed in Task 2.2) but records carry the actual touched project, since a session may span repos.

- [ ] **Step 1: Write the failing test** — append to `internal/audit/log_test.go`:

```go
func TestRecordCarriesProject(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Append(Record{Kind: KindDispatch, Detail: "claude·opus", Project: "pid123"}); err != nil {
		t.Fatal(err)
	}
	recs, err := l.Tail(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Project != "pid123" {
		t.Errorf("project not round-tripped: %+v", recs)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/audit/ -run TestRecordCarriesProject -v`
Expected: FAIL — `Record has no field Project`.

- [ ] **Step 3: Implement.** In `internal/audit/log.go`, add to `Record` (after `Detail`, line 32):

```go
	Project string            `json:"project,omitempty"`
```

In `cmd/styx/repl.go`, extend `auditf` to accept the touched project and stamp it. Change the signature:

```go
func (s *replSession) auditf(kind audit.Kind, detail, projectID string, meta map[string]string) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Append(audit.Record{Kind: kind, Detail: detail, Project: projectID, Meta: meta})
}
```

Update **all six** `auditf` call sites (grounded line numbers) to pass the project ID:

| Call site | Kind | projectID argument |
|---|---|---|
| repl.go:56 (`turn`) | `KindTurn` | `s.focus` |
| repl.go:76 (`turn`) | `KindDecision` | `s.focus` |
| repl.go:90 (`execute`) | `KindRiskPrompt` | `s.focus` |
| repl.go:106 (`execute`) | `KindPipeline` | `s.focus` |
| repl.go:159 (`runOneDispatch`) | `KindDispatch` | `bp.proj.ID` (the resolved target from Task 5.2) |
| repl.go:302 (`saveMemoryText`) | `KindMemoryWrite` | `s.proj().ID` |

The audit-open in `newREPLSession` stays under the seed project's ID dir (Task 2.2 already keyed it by `proj.ID`). In `/audit` (lines 601–613), render the project tag in the Tail loop:

```go
		for _, r := range recs {
			tag := r.Project
			if tag == "" {
				tag = "-"
			}
			s.println(fmt.Sprintf("%s  %-12s %-12s %s", r.At.Format("15:04:05"), tag, r.Kind, r.Detail))
		}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/audit/ ./cmd/styx/ && go build ./...`
Expected: PASS (existing `TestAuditTrail` updated to the new `auditf` arity).

- [ ] **Step 5: Commit**

```bash
git add internal/audit/log.go cmd/styx/repl.go internal/audit/log_test.go cmd/styx/repl_test.go
git commit -m "feat(audit): project-tagged records in one session audit stream"
```

### Task 5.5: Header, `/threads`/`/repos`/`/focus`, two-repo scripted session, launch binding

**Files:**
- Modify: `cmd/styx/repl.go` (`cmdREPL` header line 550; `slash` lines 583–618; `renderBoundProjects`/`renderKnownProjects` from Task 4.2; launch binding in `cmdREPL`/`newREPLSession`)
- Modify: `cmd/styx/dispatch.go` / `cmd/styx/main.go` (launch binding: `styx <repo...>` seeds the bound set — optional `--workspace` deferred)
- Test: `cmd/styx/repl_test.go` (two-repo scripted session)

- [ ] **Step 1: Generalize the registry renderers over the bound set.** Update `renderBoundProjects` (Task 4.2) to iterate `s.bound`:

```go
func (s *replSession) renderBoundProjects() []string {
	var out []string
	for _, bp := range s.bound {
		out = append(out, renderProject(bp.proj, true))
	}
	return out
}

func (s *replSession) renderKnownProjects() []string {
	regs, err := project.List()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range regs {
		if _, bound := s.bound[p.ID]; bound {
			continue
		}
		out = append(out, renderProject(p, false))
	}
	return out
}
```

- [ ] **Step 2: Multi-repo header + `/threads` + `/repos` + `/focus`.** Update `cmdREPL` header (line 550) to show focus + bound count:

```go
	fmt.Printf("styx — %s (%d repo%s) · /status /repos /focus /budget /threads /why /audit /quit\n",
		s.proj().Name, len(s.bound), plural(len(s.bound)))
```

In `slash`, make `/status`/`/threads` iterate all bound managers (prefix each block with the project name), and add `/repos` (list bound + focus marker) and `/focus <name>` (resolve, bind, flip focus):

```go
	case "/repos":
		for id, bp := range s.bound {
			marker := "  "
			if id == s.focus {
				marker = "→ "
			}
			s.println(marker + renderProject(bp.proj, true))
		}
	case "/focus":
		fields := strings.Fields(line)
		if len(fields) < 2 {
			s.println("usage: /focus <project>")
			return false
		}
		p, err := target.Resolve(target.Spec{Alias: fields[1]})
		if err != nil {
			s.println("focus: " + err.Error())
			return false
		}
		if _, err := s.bind(p); err != nil {
			s.println("focus: " + err.Error())
			return false
		}
		s.focus = p.ID
		s.println("→ focus: " + p.Name)
```

Add a `plural` helper. Update the `default:` help line to mention `/repos /focus`.

- [ ] **Step 3: Launch binding.** In `cmdREPL`/`newREPLSession`, after binding the seed project, bind any positional repo args (`styx ai-ta-backend ai-ta-teacher-ui` seeds the set; the first becomes focus). Thread the post-flag positional args from `main`/`dispatch` into `newREPLSession`. (Defer `--workspace <name>` named groups to a fast-follow per the spec's open question; lazy name-binding covers the core need.)

- [ ] **Step 4: Write the two-repo scripted session test** — append to `cmd/styx/repl_test.go` a `TestTwoRepoScriptedSession` that binds two repos, drives a turn whose brain action dispatches to repo B with repo A as an extra root, then asserts: B's thread ran with A attached (fakeagent ARGS_LOG shows `--add-dir <A path>`), a remember in focus surfaces via cross-repo recall, and `/repos` lists both with the focus marker. Reuse `bindTestProject` (Task 5.1) and the `FAKEAGENT_ARGS_LOG` capture pattern.

```go
func TestTwoRepoScriptedSession(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKEAGENT_TEXT", "traced")
	argsLog := filepath.Join(t.TempDir(), "args.log")
	t.Setenv("FAKEAGENT_ARGS_LOG", argsLog)

	bud, _ := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	a := bindTestProject(t, "backend", bud)
	b := bindTestProject(t, "teacher", bud)
	_ = config.SaveProjects([]config.Project{
		{ID: "backend", Name: "backend", Path: a.proj.Path},
		{ID: "teacher", Name: "teacher", Path: b.proj.Path},
	})
	out := &bytes.Buffer{}
	s := &replSession{
		bound: map[string]*boundProject{"backend": a, "teacher": b},
		focus: "backend", emb: replEmbedder{}, tracker: bud,
		tiers: map[string]string{"opus": "opus", "haiku": "haiku"},
		in: bufio.NewReader(strings.NewReader("")), out: out,
	}
	d := brain.Dispatch{Thread: "claude", Message: "trace upload", Project: "teacher", ExtraRoots: []string{"backend"}}
	if err := s.runOneDispatch(context.Background(), d, "opus"); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(argsLog)
	if !strings.Contains(string(log), "--add-dir") || !strings.Contains(string(log), a.proj.Path) {
		t.Errorf("backend not attached to teacher dispatch: %s", log)
	}
	if b.mgr.Threads.Get("claude", "claude").Turns != 1 {
		t.Errorf("teacher thread did not run")
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./cmd/styx/ -v && go build ./...`
Expected: PASS — the two-repo session drives a cross-repo dispatch and all single-repo tests stay green.

- [ ] **Step 6: Commit**

```bash
git add cmd/styx/repl.go cmd/styx/dispatch.go cmd/styx/main.go cmd/styx/repl_test.go
git commit -m "feat(repl): multi-repo header, /repos + /focus, launch binding, two-repo E2E"
```

### Task 5.6: Phase 5 docs (drift contract) + Self-review

**Files:**
- Modify: `docs/ARCHITECTURE.md` (Brain/Memory/Audit + a new "Multi-project REPL session" subsection; bump `last_verified`)
- Modify: `README.md` (launch binding, `/repos`/`/focus`)

- [ ] **Step 1: Update `docs/ARCHITECTURE.md`.** Add a "Multi-project session" subsection under the REPL/Agent coverage: the bound-project set keyed by ID, lazy bind, current-focus pointer, per-dispatch repo routing + ExtraRoots, session-scoped recall across bound stores, one project-tagged audit stream, and the `/repos`/`/focus` commands + launch binding. Update "Memory" to note cross-repo recall, "Audit" to note the project tag. Bump `last_verified:` to 2026-06-18.

- [ ] **Step 2: Update `README.md`.** Add to the Conversational section:

```markdown
| `styx <repo...>` | Open the REPL bound to one or more named repos (first is focus) |
```

Note the `/repos` and `/focus <name>` slash commands and that naming a repo mid-conversation binds it lazily.

- [ ] **Step 3: Run the full suite + vet + gofmt**

Run: `go build ./... && go vet ./... && gofmt -l . && make test`
Expected: build clean, vet clean, `gofmt -l` prints nothing, all tests PASS.

- [ ] **Step 4: Commit**

```bash
git add docs/ARCHITECTURE.md README.md
git commit -m "docs(repl): document multi-project session, /repos + /focus, launch binding"
```

---

## Self-review notes (spec coverage)

Checked against `docs/superpowers/specs/2026-06-18-styx-multi-repo-orchestration-design.md`:

- **§1a Unified target resolver** → Task 1.1 (`internal/target`), Task 1.2 (delete both old resolvers, reroute). No silent cwd fallback asserted in 1.1's "unknown alias errors" case.
- **§1b Global `--project`/`--dir`** → Task 1.3 (`parseGlobalFlags`), Task 1.4 (reroute every verb).
- **§1c `project scan`** → Task 2.4 (walk-down, prune, no-descend, bulk register).
- **§1d Stable project ID + re-key** → Task 2.1 (ID + Description + backfill), Task 2.2 (re-key memory/audit/intel/**threads**), Task 2.3 (idempotent migration). The threads file (the spec's three under-counted to include a fourth) is re-keyed and migrated.
- **§1e Multi-terminal hardening** → Task 2.5 (WAL + busy_timeout on the budget DB; concurrent-writers test). Task 2.6's doc step records the confirmation the spec also asks for: `projects.toml` (`config.SaveProjects`) and the models cache (`modelsync.Cache.Save`) already use atomic tmp+rename, and the per-repo `internal/pipeline/lock.go` lock already makes same-repo cross-terminal runs safe — so only the shared budget DB needed the WAL change.
- **§1f Run-id + project tag** → Task 3.1 (Event fields + columns), Task 3.2 (mint + populate at all 4 Record sites). Data seam only; no evaluator (correctly out of scope).
- **§2a Brain `Dispatch`** → Task 4.1 (struct + schema), Task 4.2 (registry in prompt). `Valid()` kept structural; registry resolution + escalation lives in Task 5.2 (a deliberate, documented choice — keeps `brain` free of `config`/`project`, no import cycle, no churn to 8 `Valid()` callers).
- **§2b Agent `DispatchSpec`** → Task 4.4 (`ExtraRoots` + codex arg-order fix). The "project/working-dir override" is realized via per-repo Manager selection in Task 5.2 (the spec's own §2b wording: "routes to that project's manager"), not a per-dispatch WorkDir field.
- **§2c Channel `Request`** → Task 4.3 (`ExtraRoots → --add-dir`, claude/codex/agy). `WorkingDir`/`cmd.Dir` retained (additive — corrects the spec's "removes the bottleneck" phrasing, which is about routing capability not field deletion).
- **§3 Session model** → Task 5.1 (bound set + focus + lazy bind), 5.2 (per-dispatch routing), 5.3 (cross-repo recall), 5.4 (tagged audit), 5.5 (header/`/repos`/`/focus`/launch).
- **§4 Backward compatibility** → Task 3.3 (`auto --resume` loadable, additive `State.Version`); single-repo UX preserved via accessor methods (Task 5.1); migration idempotent (Task 2.3).
- **§5 Error handling** → loud resolver (1.1, no silent cwd fallback), escalate-via-`askUserRoute` on an unknown brain-named repo (5.2, wrapped in `errUnresolvedRepo`), unchanged ship-risk gate (carried through 5.1's accessor migration).
- **§6 Testing** → every task ships table-driven tests with fakes. Specifically the spec's named cases: brain `project`/`extra_roots` round-trip + schema (4.1), the **negative** unresolvable-name test the `Valid()` deviation displaced (`TestDispatchUnknownTargetSurfacesError`, 5.2), channel `--add-dir` emission (4.3, codex via `reflect.DeepEqual`), `project scan` against a vendored tree (2.4), two-repo session + cross-repo recall (5.3, 5.5), migration idempotency (2.3, 3.1), and the budget concurrent-writers test (2.5).
- **§7 Documentation** → per-phase docs tasks (1.5, 2.6, 3.4, 4.5, 5.6) update `docs/ARCHITECTURE.md` + `README.md` and bump `last_verified` within each phase's commits, honoring the drift contract.
- **§8 Scope boundary** → the self-improvement evaluator/apply-back is **not** planned; only its data seam (run-id + project tag) lands in Phase 3.

**Known deviations (intentional, called out inline):**
- Phase 0 (branch sync onto `origin/main`) is a prerequisite the spec assumes implicitly — made explicit because the feature branch is based on stale local-main and lacks `repl.go`/`agent`/`audit`/effort-aware channels.
- `Description` field added to `config.Project` (Task 2.1/4.2) to satisfy "one-line language/description" — the spec referenced a description with no field defined; it round-trips for free and falls back to the path basename.
- `Valid()` left structural; project-name resolution + escalation handled in the REPL dispatch path (Task 5.2) rather than inside `Valid()` — avoids a `brain`→`config` import cycle and a signature change across 8 `Valid()` callers. The spec-mandated negative behavior ("unresolvable name → escalate / ask the user, never a silent guess") is preserved and **tested**: Task 5.2 wraps the resolver error in `errUnresolvedRepo`, routes it through `askUserRoute` (the same path as `brain.ErrNeedUser`), and `TestDispatchUnknownTargetSurfacesError` asserts no thread runs and the error surfaces.
- `--workspace` named groups deferred to a fast-follow (spec open question); lazy name-binding + positional launch binding cover the core need.
- The codex **agent** adapter's `extra` placement was reordered (Task 4.4) — a latent bug where brain-emitted `--add-dir` via `cli_options` would have landed before `exec`.
- Bare `styx` from a non-repo cwd (e.g. `~`) with no target errors with targeting guidance rather than opening a focus-less REPL (Task 1.3 Step 5). "Run from `~`" is satisfied by the explicit-target forms (`--project`/`--dir`/positional launch binding); a graceful zero-focus REPL is a fast-follow.
