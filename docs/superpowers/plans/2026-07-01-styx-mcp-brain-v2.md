# styx MCP Brain v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend the v1 styx MCP server with a task-fit **capability floor** (never silently degrade a complex task below a capable model), a loud budget-block, and four new tools — `channel_health`, `get_intel`, `refresh_intel`, `recall` — each a thin adapter over an existing on-disk subsystem.

**Architecture:** v2 adds no new state stores and no SDKs. It (1) teaches `internal/signals` a table-driven capability-tier vocabulary + signal→floor map; (2) makes `internal/router` derive an explicit floor, restrict fallback to floor-clearing targets, and *refuse loud* when all are over budget (fixing a latent chain-exhaustion bug); (3) adds read-only query methods to `internal/budget` and one staleness helper to `internal/intel`; (4) appends new tool handlers to the existing `cmd/styx/mcp.go` `mcpTools()` seam. The MCP transport layer (`internal/mcpserver`) is untouched — v2 only registers more `Tool` values.

**Tech Stack:** Go 1.22, standard library only (hand-rolled JSON-RPC over stdio from v1 — no MCP SDK), `modernc.org/sqlite` (pure Go), `BurntSushi/toml`, `github.com/google/go-cmp/cmp` (tests). Module path `github.com/ishaanbatra/styx`.

## Baseline & Dependency (read first)

- **v1 is NOT yet in the code tree.** `internal/mcpserver/` and `cmd/styx/mcp.go` do not exist on any main-tree branch. v1 exists only as `docs/superpowers/plans/2026-06-29-styx-mcp-routing-brain.md`. Per the v2 spec §"Baseline", **v1 must be implemented and merged to `main` before this plan runs**, and v2 branches off `main` as `feature/styx-mcp-brain-v2`.
- This plan assumes v1's merged artifacts exist exactly as its plan specifies:
  - `internal/mcpserver/server.go` — `type Tool struct { Name, Description string; InputSchema any; Handler func(ctx context.Context, args json.RawMessage) (any, error) }`, `func New(name, version string, tools []Tool) *Server`, `func (s *Server) Serve(ctx, in, out) error`. A handler returning an `error` is surfaced by the server as an MCP **tool result** with `isError:true` and the error text (not a JSON-RPC protocol error) — this is how every v2 tool "degrades loud".
  - `cmd/styx/mcp.go` — `type routeArgs struct { Task, Verb string; Signals []string; Project string }`, `type budgetSnapshot struct {...}`, `type routeResult struct { Channel, Model, Effort string; FallbackChain []string; Reasoning string; Budget budgetSnapshot; Degraded bool }`, `func budgetSnapshotFor(ctx, t *budget.Tracker, channel string) budgetSnapshot`, `func handleRoute(ctx, r *router.Router, t *budget.Tracker, a routeArgs) (routeResult, error)`, `func mcpTools(a *app) []mcpserver.Tool`, `func cmdMCP(a *app, args []string) error`, `var defaultChannelNames = []string{"claude","codex","agy","ollama"}`, `const mcpServerVersion`.
  - The v1 in-memory test harness in `cmd/styx/mcp_test.go`: `func testRouterAndTracker(t *testing.T) (*router.Router, *budget.Tracker)` (builds a real router over a real sqlite tracker in `t.TempDir()`), and the `mcpserver.New("styx","test", mcpTools(a))` + `srv.Serve(ctx, strings.NewReader(in), &out)` end-to-end pattern.
- **If v1 is not yet merged when you start, STOP** and implement/merge the v1 plan first. Every task below builds on those symbols.

## Global Constraints

Copied verbatim from the v2 spec and CLAUDE.md; every task's requirements implicitly include these:

- **No provider SDKs.** Channels stay CLIs/HTTP. v2 adds only server-side stdlib code; do not add an Anthropic/OpenAI client. (CLAUDE.md "Key tech decisions")
- **No new state stores.** Every tool reads existing on-disk data (append-only sqlite usage log, intel `index.json`, memory sqlite). (spec §Summary)
- **Additive only.** `route` output gains fields; nothing in v1 output is renamed or removed. v1 consumers and the OpenClaw registration keep working unchanged. `router.Decision` gains fields; it is built by struct literal only inside `internal/router` — appending fields is backward compatible. (spec §"Backward compatibility")
- **Never swallow errors.** No `x, _ :=`. Surface every error wrapped with context (`fmt.Errorf("...: %w", err)`); tool errors surface as classified MCP tool errors. (CLAUDE.md)
- **Unknown project is a classified error, never a silent fallback to cwd or a default.** Project-scoped tools (`get_intel`/`refresh_intel`/`recall`) require a project; resolution reuses `internal/target` but must NOT fall back to cwd. (spec §Cross-cutting)
- **Degrade loud.** `recall` requires local Ollama embeddings; when unavailable it returns a classified error, never empty-as-success. The router refuses loud at the floor, never returns an over-cap/below-floor channel as if fine. (spec §1.3, §4)
- **Drift contract.** Editing an `internal/**` or `cmd/styx/**` file requires updating its owner doc (`docs/ARCHITECTURE.md`) **in the same commit** and bumping its `last_verified` date; adding an MCP tool also updates `README.md`. (CLAUDE.md) Task 9 consolidates the doc pass; each earlier task's commit should also touch the owner doc where practical.
- **Atomic file writes** (tmp + rename); **bounded subprocesses** (context timeouts — `intel.Build` already enforces per-call agy timeouts). (CLAUDE.md)
- **Status/narration to stderr** via `logStatus` (respect `--quiet`); in the `mcp` path stdout is the JSON-RPC protocol — never print results to stdout there. (CLAUDE.md)

## Design Decisions (where this plan interprets the spec)

These are the deliberate engineering calls made to turn the design-approved spec into concrete code. Flagged so a reviewer can veto before execution.

1. **Capability-tier vocabulary.** The floor is expressed as a coarse `signals.Tier` rank (`local < haiku < sonnet < opus`), classified from a target's `channel:model` by a hand-curated table (styx routing is a transparent table, not an LLM). The default `routing.toml` already uses tier tokens (`claude:opus`, `claude:sonnet`, `codex`, `ollama:…`), so the classifier is exact. `floor` in the JSON output is the tier keyword (e.g. `"sonnet"`), not a channel-qualified string like the spec's illustrative `"claude:sonnet"` — a tier is channel-agnostic (both `claude:sonnet` and `codex` clear a `sonnet` floor), which the illustration blurs.
2. **Floor = safety floor, not cost optimizer — and budget is per-channel.** styx's budget/availability check is keyed on the **channel name only** (`router.overCap`/`unavailable`/`capFor` and `config.BudgetCaps` have one cap per channel — the model string is ignored). Two consequences the spec's illustrative "`opus` over cap → `sonnet`" example glosses over: (i) a same-channel model downgrade (`claude:opus`→`claude:sonnet`) can **never** relieve an over-cap `claude` — degradation only helps when it crosses to a *different* channel; (ii) "cheapest acceptable within budget" is therefore a choice among **channels**, and the rule author's ordered chain already encodes that channel preference. So the floor's job is (a) exclude below-floor **channels** (e.g. `ollama`) from the degrade set and (b) refuse loud when every floor-clearing channel is over budget. `chosen` = the first *available* floor-clearing target in chain order (the author's preferred available capable channel). This satisfies every spec success criterion, matches the realistic degraded case (`claude` over cap → `codex`, skipping below-floor `ollama`; `escalate_to: claude:opus`; `degraded: true`), and does **not** override the author's primary in the non-degraded case. Verified backward-compatible with every existing `internal/router` test.
3. **`ClassifiedSignals` and `RetryAfterS` live at the handler, not on `Decision`.** The router receives signals; it doesn't classify (that's `signals.Extract`, called by the caller). And retry timing needs concrete budget internals the router's abstract `BudgetSource` can't see. So `route`'s `classified_signals` = the signals the handler fed in, and `retry_after_s` is computed by the handler from `*budget.Tracker` when the router reports `BlockedByBudget`. `Floor`, `TierPlan`, `BlockedByBudget` DO go on `Decision` (router-owned, surfaced by `styx route --explain`).
4. **`recall` classification at the MCP boundary.** `internal/memory` stays untouched (its REPL swallow-on-error at `repl.go:137` is out of v2 scope). The `recall` handler wraps any `memory.Recall` error as a `channel.ClassifiedError` with a loud message — sufficient for the MCP surface to "degrade loud" without changing REPL behavior.
5. **`retry_after_s` is best-effort.** It reports remaining cooldown, else the time until the oldest in-window message ages out under a message cap. A pure token-budget block (30-day window) may yield `0` (unknown) — `blocked_by_budget=true` is the load-bearing signal; the hint is advisory.
6. **Project resolution is strict.** MCP tools resolve via `target.Resolve(target.Spec{Alias: name})` — alias/path only, never cwd (the CLI's `resolveGlobalTarget` falls back to cwd; the MCP path must not). Empty or unknown project → classified error.

## File Structure

**Created:**
- `internal/signals/floor.go` — `Tier` type + `TierOf` + `signalFloor` map + `Floor`. Sits beside signal definitions (spec §"router change").
- `internal/signals/floor_test.go` — table-driven `TierOf`/`Floor` tests.

**Modified:**
- `internal/signals/signals.go` — add named signal constants; refactor `Extract` to use them (so the floor map can't drift from emitted strings).
- `internal/router/router.go` — `TierPlan` struct; `Decision` gains `Floor`/`TierPlan`/`BlockedByBudget`; floor-restricted degradation + chain-exhaustion fix in `Route`; `Explain` prints floor/blocked when set.
- `internal/router/router_test.go` — floor-excludes-below-floor, chain-exhaustion→blocked (regression guard), escalate_to, non-floored-unchanged.
- `internal/budget/budget.go` — `BreakerThreshold`/`BreakerWindow` consts; `ChannelHealth` struct + method; `RetryAfter` method (+ `windowRetry` helper).
- `internal/budget/budget_test.go` — `ChannelHealth` buckets/circuit; `RetryAfter` cooldown/window.
- `internal/intel/intel.go` — extract `Staleness(proj, idx)` from `IsStale`; `IsStale` delegates (no double disk read for callers that already loaded).
- `internal/intel/intel_test.go` — `Staleness` age/commit cases.
- `cmd/styx/mcp.go` — route-v2 additive fields + handler; `channel_health`, `get_intel`, `refresh_intel`, `recall` args/results/schemas/handlers; `resolveProjectStrict`; register all in `mcpTools`; update `cmdMCP` status string.
- `cmd/styx/mcp_test.go` — handler unit tests + end-to-end stdio tests for each new tool/field.
- `docs/ARCHITECTURE.md`, `README.md`, `docs/superpowers/free-tier-tracker.md` — drift-contract updates (Task 9).

Rationale for splitting the tier/floor logic into `internal/signals/floor.go` rather than growing `signals.go`: `signals.go` today is a single pure `Extract`; the tier vocabulary is a distinct concern (routing capability, not classification) and `internal/router` will import it — a focused file keeps both readable.

**Test-file imports:** the new tests reference packages the v1 `cmd/styx/mcp_test.go` import block does not yet include — add them as each task's tests land: `time` (Task 5), `errors` + `internal/intel` + `internal/channel` (Task 7), `path/filepath` + `internal/memory` + `internal/paths` (Task 8), `bytes` + `strings` (end-to-end tests). Run `goimports`/`gofmt` and let the compiler tell you what's missing before each commit.

---

### Task 1: signals — capability-tier vocabulary + signal→floor map

**Files:**
- Create: `internal/signals/floor.go`
- Modify: `internal/signals/signals.go` (add named constants; refactor `Extract`)
- Test: `internal/signals/floor_test.go` (create); `internal/signals/signals_test.go` must stay green

**Interfaces:**
- Consumes: nothing new (pure package; already imports `strings`, `internal/config`).
- Produces (used by Task 2's router):
  - `type Tier int` with `const ( TierLocal Tier = iota; TierHaiku; TierSonnet; TierOpus )` and `func (Tier) String() string`
  - `func TierOf(channel, model string) Tier`
  - `func Floor(sigs []string) Tier`
  - exported signal constants `SigTrivial`, `SigDeep`, `SigComplex`, `SigInteractive` (all `= "trivial"/"deep"/"complex"/"interactive"`)

- [ ] **Step 1: Write the failing test** — `internal/signals/floor_test.go`

```go
package signals

import "testing"

func TestTierOf(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		model   string
		want    Tier
	}{
		{"ollama local", "ollama", "qwen2.5-coder:14b", TierLocal},
		{"claude opus", "claude", "opus", TierOpus},
		{"claude opus versioned", "claude", "opus-4-7", TierOpus},
		{"claude sonnet", "claude", "sonnet", TierSonnet},
		{"claude haiku", "claude", "haiku", TierHaiku},
		{"claude interactive falls to sonnet", "claude", "interactive", TierSonnet},
		{"codex is capable", "codex", "gpt-5", TierSonnet},
		{"codex bare", "codex", "", TierSonnet},
		{"agy is capable", "agy", "default", TierSonnet},
		{"unknown cloud stays capable", "mystery", "x", TierSonnet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TierOf(c.channel, c.model); got != c.want {
				t.Errorf("TierOf(%q,%q) = %v, want %v", c.channel, c.model, got, c.want)
			}
		})
	}
}

func TestFloor(t *testing.T) {
	cases := []struct {
		name string
		sigs []string
		want Tier
	}{
		{"no signals -> no floor", nil, TierLocal},
		{"trivial imposes no floor", []string{SigTrivial}, TierLocal},
		{"lang only -> no floor", []string{"lang:go"}, TierLocal},
		{"complex -> sonnet floor", []string{SigComplex}, TierSonnet},
		{"deep -> sonnet floor", []string{SigDeep}, TierSonnet},
		{"complex + lang -> sonnet", []string{SigComplex, "lang:go"}, TierSonnet},
		{"complex + deep -> highest (sonnet)", []string{SigComplex, SigDeep}, TierSonnet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Floor(c.sigs); got != c.want {
				t.Errorf("Floor(%v) = %v, want %v", c.sigs, got, c.want)
			}
		})
	}
}

func TestTierString(t *testing.T) {
	for tier, want := range map[Tier]string{
		TierLocal: "local", TierHaiku: "haiku", TierSonnet: "sonnet", TierOpus: "opus",
	} {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q, want %q", tier, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/signals/ -run 'TestTierOf|TestFloor|TestTierString' -v`
Expected: FAIL — `undefined: Tier`, `undefined: TierOf`, `undefined: Floor`, `undefined: SigComplex`.

- [ ] **Step 3: Create `internal/signals/floor.go`**

```go
package signals

import "strings"

// Tier is a coarse capability rank for a routing target. Higher is more capable
// (and generally more expensive). It is the vocabulary the router's capability
// floor is expressed in.
type Tier int

const (
	TierLocal  Tier = iota // ollama / local models — weakest
	TierHaiku              // small cloud tier (claude haiku-class)
	TierSonnet             // general capable cloud (sonnet / codex / agy)
	TierOpus              // top cloud tier (opus / fable)
)

// String renders the tier as its lowercase keyword, matching the [tiers] vocab
// in routing.toml (plus "local" for ollama).
func (t Tier) String() string {
	switch t {
	case TierOpus:
		return "opus"
	case TierSonnet:
		return "sonnet"
	case TierHaiku:
		return "haiku"
	default:
		return "local"
	}
}

// TierOf ranks a routing target by its channel and model string. The mapping is
// hand-curated (styx routing is a transparent table, not an LLM) and biased
// toward inclusion: an unknown cloud channel is treated as sonnet-class so it is
// never wrongly excluded from a capable-tier floor. Only ollama is below-floor.
func TierOf(channel, model string) Tier {
	switch channel {
	case "ollama":
		return TierLocal
	case "claude":
		m := strings.ToLower(model)
		switch {
		case strings.Contains(m, "opus"), strings.Contains(m, "fable"):
			return TierOpus
		case strings.Contains(m, "haiku"):
			return TierHaiku
		default: // "sonnet", "interactive", or unspecified claude
			return TierSonnet
		}
	default: // codex, agy, gemini, or any other cloud channel
		return TierSonnet
	}
}

// signalFloor maps a classification signal to the minimum capability tier a task
// carrying that signal requires. Signals with no entry impose no floor. Kept
// beside the signal definitions so the map cannot drift from what Extract emits.
var signalFloor = map[string]Tier{
	SigComplex: TierSonnet,
	SigDeep:    TierSonnet,
}

// Floor returns the highest minimum tier required by any of the given signals,
// or TierLocal when no signal imposes a floor.
func Floor(sigs []string) Tier {
	floor := TierLocal
	for _, s := range sigs {
		if t, ok := signalFloor[s]; ok && t > floor {
			floor = t
		}
	}
	return floor
}
```

- [ ] **Step 4: Add named constants to `internal/signals/signals.go` and refactor `Extract`**

First read `internal/signals/signals.go`. Add this constant block immediately after the `complexKeywords` var (around line 15):

```go
// Signal name constants. Keeping the emitted strings here (rather than inline
// literals) lets the capability-floor map in floor.go reference the same values.
const (
	SigTrivial     = "trivial"
	SigDeep        = "deep"
	SigComplex     = "complex"
	SigInteractive = "interactive"
)
```

Then in `Extract`, replace the four inline string literals with the constants — `"interactive"` → `SigInteractive`, `"trivial"` → `SigTrivial`, `"deep"` → `SigDeep`, `"complex"` → `SigComplex`. The `"lang:"+proj.Language` line is unchanged (dynamic). The emitted string values are identical, so `signals_test.go` (which compares against literals like `"complex"`) still passes.

- [ ] **Step 5: Run all signals tests to verify pass**

Run: `go test ./internal/signals/ -v`
Expected: PASS — new `TestTierOf`/`TestFloor`/`TestTierString` green AND the pre-existing `TestExtract` still green (unchanged string values).

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/signals/
git add internal/signals/floor.go internal/signals/floor_test.go internal/signals/signals.go
git commit -m "feat(signals): capability-tier vocabulary and signal->floor map"
```

---

### Task 2: router — floor-restricted degradation + chain-exhaustion fix

**Files:**
- Modify: `internal/router/router.go` (add `TierPlan`; extend `Decision`; rewrite the single-channel degradation block in `Route`; extend `Explain`)
- Test: `internal/router/router_test.go`

**Interfaces:**
- Consumes (from Task 1): `signals.Tier`, `signals.TierOf(channel, model) Tier`, `signals.Floor(sigs []string) Tier`. Add import `"github.com/ishaanbatra/styx/internal/signals"` to `router.go` (no import cycle — `signals` imports only `internal/config`).
- Produces (used by Task 5's handler):
  - `type TierPlan struct { Acceptable []string; Chosen string; EscalateTo string }`
  - `Decision` new fields `Floor string`, `TierPlan TierPlan`, `BlockedByBudget bool`

- [ ] **Step 1: Write the failing tests** — append to `internal/router/router_test.go`

```go
func TestRoute_ComplexFloorDegradesToCapableChannel(t *testing.T) {
	// plan+complex: claude:opus primary over its 80% cap. IMPORTANT: styx budget
	// is per-CHANNEL, not per-model — so a same-channel opus->sonnet drop can never
	// escape the claude cap. Degradation must land on a DIFFERENT capable channel.
	// The complex floor (sonnet) keeps codex but excludes the below-floor ollama
	// fallback, so the decision degrades to codex — never ollama, never blocked.
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus",
				Fallback: []string{"codex", "ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}}, // codex uncapped -> available
		map[string]float64{"claude": 95},                          // only claude over cap
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan", Args: []string{"x"}, Signals: []string{"complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "codex" {
		t.Fatalf("chose %s:%s, want codex (below-floor ollama must be excluded)", dec.Channel, dec.Model)
	}
	if !dec.Degraded || dec.BlockedByBudget {
		t.Fatalf("want degraded=true blocked=false, got degraded=%v blocked=%v", dec.Degraded, dec.BlockedByBudget)
	}
	if dec.Floor != "sonnet" {
		t.Fatalf("floor = %q, want sonnet", dec.Floor)
	}
	wantAcc := []string{"claude:opus", "codex"} // ollama excluded from the floor-clearing set
	if diff := cmp.Diff(wantAcc, dec.TierPlan.Acceptable); diff != "" {
		t.Fatalf("acceptable mismatch (-want +got):\n%s", diff)
	}
	if dec.TierPlan.Chosen != "codex" {
		t.Fatalf("tier_plan.chosen = %q, want codex", dec.TierPlan.Chosen)
	}
	if dec.TierPlan.EscalateTo != "claude:opus" {
		t.Fatalf("escalate_to = %q, want claude:opus", dec.TierPlan.EscalateTo)
	}
}

func TestRoute_ChainExhaustionBlocksLoud(t *testing.T) {
	// Regression guard for the chain-exhaustion bug: opus primary AND its only
	// floor-clearing fallback (codex) are BOTH over cap. Old code returned the
	// over-cap primary with Degraded=true and never refused. New behavior:
	// BlockedByBudget=true, chosen stays the floor-clearing primary as a concrete
	// recommendation.
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus",
				Fallback: []string{"codex", "ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}, Codex: config.ChannelCap{CapPct: 80}},
		map[string]float64{"claude": 95, "codex": 90}, // both capable channels over cap
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan", Args: []string{"x"}, Signals: []string{"complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if !dec.BlockedByBudget {
		t.Fatalf("want BlockedByBudget=true (all floor-clearing channels over cap), got false")
	}
	if !dec.Degraded {
		t.Fatalf("want Degraded=true when blocked")
	}
	if dec.Channel != "claude" || dec.Model != "opus" {
		t.Fatalf("blocked chosen = %s:%s, want the floor-clearing primary claude:opus", dec.Channel, dec.Model)
	}
	// ollama must NOT be chosen: styx never returns a below-floor channel.
	if dec.Channel == "ollama" {
		t.Fatal("blocked path degraded to below-floor ollama — floor violated")
	}
}

func TestRoute_NonFlooredUnchanged(t *testing.T) {
	// A task with no complex/deep signal keeps v1 behavior: over-cap primary
	// degrades to the first available fallback, including ollama; never blocked.
	r := newRouter(
		[]config.Rule{
			{Verb: "plan", Use: "claude:sonnet",
				Fallback: []string{"ollama:qwen2.5-coder:14b"}},
		},
		config.BudgetCaps{Claude: config.ChannelCap{CapPct: 80}},
		map[string]float64{"claude": 95},
	)
	dec, err := r.Route(context.Background(), Request{Verb: "plan", Args: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if dec.Channel != "ollama" || dec.BlockedByBudget {
		t.Fatalf("non-floored task: got %s:%s blocked=%v, want ollama not blocked", dec.Channel, dec.Model, dec.BlockedByBudget)
	}
	if dec.Floor != "local" {
		t.Fatalf("floor = %q, want local (no floor)", dec.Floor)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/router/ -run 'TestRoute_ComplexFloor|TestRoute_ChainExhaustion|TestRoute_NonFloored' -v`
Expected: FAIL — `dec.BlockedByBudget undefined`, `dec.Floor undefined`, `dec.TierPlan undefined`.

- [ ] **Step 3: Add `TierPlan` type and extend `Decision`** in `internal/router/router.go`

Add near the `ChannelModel` type:

```go
// TierPlan is the capability-floor view of a routing decision: the floor-clearing
// candidate targets (chain order), the one chosen within budget, and the next
// higher-tier target to escalate to.
type TierPlan struct {
	Acceptable []string // channel:model targets that clear the floor
	Chosen     string   // channel:model actually chosen
	EscalateTo string   // next higher-tier acceptable target, or ""
}
```

Add these fields to the `Decision` struct (append after `Degraded`):

```go
	// Capability-floor fields (v2). Floor is the minimum tier the request's
	// signals require (e.g. "sonnet"); TierPlan is the floor-restricted candidate
	// view; BlockedByBudget is true when every floor-clearing target is over
	// budget or circuit-open — the loud-refusal signal. When blocked, Channel/Model
	// still name the floor-clearing primary as a concrete recommendation (never a
	// below-floor lie, never null).
	Floor           string
	TierPlan        TierPlan
	BlockedByBudget bool
```

- [ ] **Step 4: Add tier-plan helpers** in `internal/router/router.go`

```go
// cmStr renders a ChannelModel as "channel:model" (or bare channel when Model is empty).
func cmStr(c ChannelModel) string {
	if c.Model == "" {
		return c.Channel
	}
	return c.Channel + ":" + c.Model
}

// buildTierPlan reports the floor-clearing candidates, the chosen target, and the
// lowest-tier candidate strictly above the chosen tier (the escalation target).
func buildTierPlan(acceptable []ChannelModel, chosen ChannelModel) TierPlan {
	tp := TierPlan{Chosen: cmStr(chosen)}
	chosenTier := signals.TierOf(chosen.Channel, chosen.Model)
	var escalate *ChannelModel
	for i := range acceptable {
		tp.Acceptable = append(tp.Acceptable, cmStr(acceptable[i]))
		t := signals.TierOf(acceptable[i].Channel, acceptable[i].Model)
		if t > chosenTier && (escalate == nil || t < signals.TierOf(escalate.Channel, escalate.Model)) {
			c := acceptable[i]
			escalate = &c
		}
	}
	if escalate != nil {
		tp.EscalateTo = cmStr(*escalate)
	}
	return tp
}
```

- [ ] **Step 5: Rewrite the degradation block in `Route`**

Read `internal/router/router.go` around lines 115–134. That block currently parses `primary` from `rule.Use` and `fallback` from `rule.Fallback`, then does `chosen := primary; degraded := false; …` and returns. Replace **from `chosen := primary` through the single-channel `return Decision{…}, nil`** with:

```go
	// Assemble the ordered candidate chain and derive the capability floor.
	targets := append([]ChannelModel{primary}, fallback...)
	floor := signals.Floor(req.Signals)

	// floorClearing = candidates that meet the floor, in chain order.
	var floorClearing []ChannelModel
	for _, t := range targets {
		if signals.TierOf(t.Channel, t.Model) >= floor {
			floorClearing = append(floorClearing, t)
		}
	}
	routeSet := floorClearing
	floorUnmet := len(floorClearing) == 0
	if floorUnmet {
		// Misconfigured rule: no chain target meets the floor. Route best-effort
		// over the full chain but flag it loudly — never silently pretend it fits.
		routeSet = targets
	}

	chosen := routeSet[0]
	degraded := false
	blocked := false
	reason := fmt.Sprintf("matched rule #%d -> %s:%s", idx, chosen.Channel, chosen.Model)
	if r.unavailable(ctx, chosen.Channel) {
		degraded = true
		found := false
		for _, f := range routeSet[1:] {
			if !r.unavailable(ctx, f.Channel) {
				chosen = f
				found = true
				reason = fmt.Sprintf("rule #%d primary (%s:%s) unavailable; degraded to %s:%s (>= floor %s)",
					idx, primary.Channel, primary.Model, f.Channel, f.Model, floor)
				break
			}
		}
		if !found {
			// Every floor-clearing target is over budget / circuit-open. Refuse
			// LOUD: keep the floor-clearing primary as a concrete recommendation
			// but set BlockedByBudget so a consumer never runs it thinking it's fine.
			blocked = true
			chosen = routeSet[0]
			reason = fmt.Sprintf("rule #%d: all targets >= floor %s are over budget or circuit-open; blocked (recommend %s:%s once budget frees)",
				idx, floor, chosen.Channel, chosen.Model)
		}
	}
	if floorUnmet {
		degraded = true
		reason = fmt.Sprintf("rule #%d: no chain target meets required floor %s; best-effort %s:%s may be under-capable",
			idx, floor, chosen.Channel, chosen.Model)
	}

	return Decision{
		Channel:         chosen.Channel,
		Model:           chosen.Model,
		Effort:          rule.Effort,
		Fallback:        fallback,
		RuleIdx:         idx,
		Reason:          reason,
		Degraded:        degraded,
		Floor:           floor.String(),
		TierPlan:        buildTierPlan(floorClearing, chosen),
		BlockedByBudget: blocked,
	}, nil
```

Notes for the implementer:
- Keep the existing `primary`/`fallback` parsing above this block unchanged.
- The parallel-rule branch and the no-match `ollama` default (earlier in `Route`) are **unchanged** — floor enforcement applies only to matched single-channel rules (documented limitation).
- `buildTierPlan` is passed `floorClearing` (the true ≥floor subset). In the `floorUnmet` case that slice is empty, so `TierPlan.Acceptable` is empty while `Chosen` names the best-effort target — an honest "nothing met your floor" signal.

- [ ] **Step 6: Extend `Explain` to surface the floor/blocked state**

Read `internal/router/router.go` around lines 138–161 (`Explain`). It writes into a `strings.Builder` named `b` via `fmt.Fprintf(&b, …)` and ends with `return b.String()`. Immediately **before** the final `return b.String()`, insert two conditional lines using the same Builder (`d` is `Explain`'s local `Decision` — match the actual receiver/variable name the function gives its decision):

```go
	if d.Floor != "" && d.Floor != "local" {
		fmt.Fprintf(&b, "floor: %s\n", d.Floor)
	}
	if d.BlockedByBudget {
		fmt.Fprintf(&b, "blocked: all targets >= floor %s over budget or circuit-open\n", d.Floor)
	}
```

These print only for floored/blocked decisions, so non-floored `--explain` output is byte-for-byte unchanged (existing `TestExplain_DescribesPickedRule` uses substring `contains` and stays green).

- [ ] **Step 7: Run new + full existing router tests**

Run: `go test ./internal/router/ -v`
Expected: PASS — the three new tests green AND all pre-existing tests still green: in `router_test.go` — `TestRoute_FirstMatchWins`, `TestRoute_SignalsMustAllMatch`, `TestRoute_BudgetCapDegradesToFallback`, `TestRoute_BudgetCapPrimaryUnderCap_NoDegradation`, `TestRoute_NoMatchDefaultsToOllama`, `TestRoute_ParallelRule`, `TestParseChannelModel_BareChannel`, `TestRoute_CarriesEffort`, `TestExplain_DescribesPickedRule`; and in `breaker_test.go` — the breaker-forces-fallback and nil-breaker tests. (Verified during planning: no existing test degrades below a `sonnet` floor — the only tests passing a `complex`/`trivial` signal are `TestRoute_FirstMatchWins` (single opus target, still opus) and `TestRoute_SignalsMustAllMatch` (`trivial`/`lang:*` impose no floor) — so none regress.)

- [ ] **Step 8: Commit**

```bash
gofmt -w internal/router/
git add internal/router/router.go internal/router/router_test.go
git commit -m "feat(router): capability floor with loud budget-block; fix chain-exhaustion"
```

---

### Task 3: budget — channel_health + retry_after reads

**Files:**
- Modify: `internal/budget/budget.go` (add consts, `ChannelHealth` struct + method, `RetryAfter` + `windowRetry`)
- Test: `internal/budget/budget_test.go`

**Interfaces:**
- Consumes: existing `t.db`, `t.cooldownUntil`, `t.mu`, `t.msgLimits`, `WindowSession`, `WindowWeek` (all in-package). No new imports beyond `database/sql` (already imported) and `math` if needed (not needed here — use `int(...Seconds())`).
- Produces (used by Task 5/6 handlers):
  - `const BreakerThreshold = 3`; `const BreakerWindow = 10 * time.Minute`
  - `type ChannelHealth struct { Channel string; CircuitOpen bool; FailuresRecent int; WindowSeconds int; ErrorKinds map[string]int; CooldownRemainingSeconds float64 }`
  - `func (t *Tracker) ChannelHealth(ctx context.Context, channel string, threshold int, window time.Duration) (ChannelHealth, error)`
  - `func (t *Tracker) RetryAfter(ctx context.Context, channel string) (int, error)`

- [ ] **Step 1: Write the failing tests** — append to `internal/budget/budget_test.go`

```go
func TestChannelHealth_BucketsAndCircuit(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	// 3 failures with distinct kinds + 1 success.
	for _, kind := range []string{"timeout", "429", "5xx"} {
		if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", Success: false, ErrorKind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Record(ctx, Event{Channel: "claude", Verb: "plan", Success: true}); err != nil {
		t.Fatal(err)
	}
	h, err := tr.ChannelHealth(ctx, "claude", BreakerThreshold, BreakerWindow)
	if err != nil {
		t.Fatal(err)
	}
	if h.FailuresRecent != 3 {
		t.Fatalf("failures_recent = %d, want 3 (success excluded)", h.FailuresRecent)
	}
	if !h.CircuitOpen {
		t.Fatalf("circuit_open = false, want true (3 >= threshold 3)")
	}
	if h.WindowSeconds != int(BreakerWindow/time.Second) {
		t.Fatalf("window_s = %d, want %d", h.WindowSeconds, int(BreakerWindow/time.Second))
	}
	sum := 0
	for _, n := range h.ErrorKinds {
		sum += n
	}
	if sum != h.FailuresRecent {
		t.Fatalf("error_kinds sum = %d, want %d", sum, h.FailuresRecent)
	}
	// Raw stored labels "timeout"/"429"/"5xx" surface as the spec's friendly,
	// zero-filled buckets timeout/rate_limit/server/other.
	if h.ErrorKinds["timeout"] != 1 || h.ErrorKinds["rate_limit"] != 1 || h.ErrorKinds["server"] != 1 || h.ErrorKinds["other"] != 0 {
		t.Fatalf("error_kinds = %v, want timeout:1 rate_limit:1 server:1 other:0", h.ErrorKinds)
	}
}

func TestChannelHealth_HealthyChannel(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	h, err := tr.ChannelHealth(ctx, "codex", BreakerThreshold, BreakerWindow)
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	for _, n := range h.ErrorKinds {
		sum += n
	}
	// Buckets are zero-filled (4 keys) but all zero for a fresh channel.
	if h.CircuitOpen || h.FailuresRecent != 0 || sum != 0 || h.CooldownRemainingSeconds != 0 {
		t.Fatalf("fresh channel not healthy: %+v", h)
	}
}

func TestRetryAfter_Cooldown(t *testing.T) {
	tr := newTestTracker(t)
	ctx := context.Background()
	if err := tr.MarkCooldown(ctx, "claude", time.Now().Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s, err := tr.RetryAfter(ctx, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if s < 14*60 || s > 15*60 {
		t.Fatalf("retry_after = %d s, want ~15m", s)
	}
}

func TestRetryAfter_NoLimitsZero(t *testing.T) {
	tr := newTestTracker(t)
	s, err := tr.RetryAfter(context.Background(), "codex")
	if err != nil {
		t.Fatal(err)
	}
	if s != 0 {
		t.Fatalf("retry_after = %d, want 0 when no cooldown and no message cap hit", s)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/budget/ -run 'TestChannelHealth|TestRetryAfter' -v`
Expected: FAIL — `undefined: BreakerThreshold`, `tr.ChannelHealth undefined`, `tr.RetryAfter undefined`.

- [ ] **Step 3: Add consts + `ChannelHealth`** in `internal/budget/budget.go`

Add near the top-level consts (beside `WindowSession`/`WindowWeek`):

```go
// BreakerThreshold and BreakerWindow are the production circuit-breaker settings
// (mirrors cmd/styx/dispatch.go's budgetSource.Broken). Exposed so channel_health
// reports the same open/closed state the router routes on.
const (
	BreakerThreshold = 3
	BreakerWindow    = 10 * time.Minute
)

// ChannelHealth is a read-only snapshot of a channel's recent reliability, built
// from the existing usage + cooldown tables (no new state).
type ChannelHealth struct {
	Channel                  string
	CircuitOpen              bool
	FailuresRecent           int
	WindowSeconds            int
	ErrorKinds               map[string]int
	CooldownRemainingSeconds float64
}

// healthKind maps a raw stored error_kind ("timeout"/"429"/"5xx"/"") to the
// channel_health bucket label the tool contract exposes.
func healthKind(raw string) string {
	switch raw {
	case "timeout":
		return "timeout"
	case "429":
		return "rate_limit"
	case "5xx":
		return "server"
	default:
		return "other"
	}
}
```

Add the method:

```go
// ChannelHealth reports whether channel's circuit is open, how many failures it
// had in the window, per-kind failure buckets, and remaining cooldown. Pure read
// over usage + cooldown; adds no state. Buckets are zero-filled with the friendly
// labels timeout/rate_limit/server/other so a consumer can index them directly.
func (t *Tracker) ChannelHealth(ctx context.Context, channel string, threshold int, window time.Duration) (ChannelHealth, error) {
	cutoff := time.Now().Add(-window).Unix()
	h := ChannelHealth{
		Channel:       channel,
		WindowSeconds: int(window / time.Second),
		ErrorKinds:    map[string]int{"timeout": 0, "rate_limit": 0, "server": 0, "other": 0},
	}

	var fails int
	if err := t.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage WHERE channel = ? AND ts >= ? AND success = 0`,
		channel, cutoff).Scan(&fails); err != nil {
		return ChannelHealth{}, fmt.Errorf("channel health failures %s: %w", channel, err)
	}
	h.FailuresRecent = fails
	h.CircuitOpen = fails >= threshold

	rows, err := t.db.QueryContext(ctx,
		`SELECT COALESCE(NULLIF(error_kind, ''), 'other') AS k, COUNT(*)
		 FROM usage WHERE channel = ? AND ts >= ? AND success = 0 GROUP BY k`,
		channel, cutoff)
	if err != nil {
		return ChannelHealth{}, fmt.Errorf("channel health kinds %s: %w", channel, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return ChannelHealth{}, fmt.Errorf("scan channel health kind %s: %w", channel, err)
		}
		h.ErrorKinds[healthKind(k)] += n
	}
	if err := rows.Err(); err != nil {
		return ChannelHealth{}, fmt.Errorf("iterate channel health kinds %s: %w", channel, err)
	}

	cd, err := t.cooldownUntil(ctx, channel)
	if err != nil {
		return ChannelHealth{}, fmt.Errorf("channel health cooldown %s: %w", channel, err)
	}
	if !cd.IsZero() {
		if rem := time.Until(cd).Seconds(); rem > 0 {
			h.CooldownRemainingSeconds = rem
		}
	}
	return h, nil
}
```

- [ ] **Step 4: Add `RetryAfter` + `windowRetry`** in `internal/budget/budget.go`

```go
// RetryAfter returns the seconds until channel next regains capacity: the
// remaining cooldown if one is active, else the time until the oldest in-window
// message ages out under a hit message cap, else 0 (unknown / no limit). Best
// effort — a pure token-budget block has no short-window estimate and returns 0.
func (t *Tracker) RetryAfter(ctx context.Context, channel string) (int, error) {
	cd, err := t.cooldownUntil(ctx, channel)
	if err != nil {
		return 0, fmt.Errorf("retry-after cooldown %s: %w", channel, err)
	}
	if !cd.IsZero() {
		if s := int(time.Until(cd).Seconds()); s > 0 {
			return s, nil
		}
	}

	t.mu.RLock()
	lim, ok := t.msgLimits[channel]
	t.mu.RUnlock()
	if !ok {
		return 0, nil
	}
	if s, err := t.windowRetry(ctx, channel, WindowSession, lim.session); err != nil {
		return 0, err
	} else if s > 0 {
		return s, nil
	}
	if s, err := t.windowRetry(ctx, channel, WindowWeek, lim.weekly); err != nil {
		return 0, err
	} else if s > 0 {
		return s, nil
	}
	return 0, nil
}

// windowRetry returns the seconds until the oldest usage row in the window ages
// out, but only when the in-window message count has reached limit; else 0.
func (t *Tracker) windowRetry(ctx context.Context, channel string, window time.Duration, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-window).Unix()
	var count int
	var oldest sql.NullInt64
	if err := t.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MIN(ts) FROM usage WHERE channel = ? AND ts >= ?`,
		channel, cutoff).Scan(&count, &oldest); err != nil {
		return 0, fmt.Errorf("retry-after window %s: %w", channel, err)
	}
	if count < limit || !oldest.Valid {
		return 0, nil
	}
	expiry := time.Unix(oldest.Int64, 0).Add(window)
	if s := int(time.Until(expiry).Seconds()); s > 0 {
		return s, nil
	}
	return 0, nil
}
```

- [ ] **Step 5: (optional cleanup) point the production breaker at the new consts**

In `cmd/styx/dispatch.go` around line 254, `budgetSource.Broken` calls `ShouldCircuitBreak(ctx, ch, 3, 10*time.Minute)`. Replace the literals with `budget.BreakerThreshold, budget.BreakerWindow` so the router and `channel_health` provably share one setting. (If you make this edit, note it in `docs/ARCHITECTURE.md` in this task's commit.)

- [ ] **Step 6: Run budget tests**

Run: `go test ./internal/budget/ -v`
Expected: PASS — new tests green; all existing budget tests still green.

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/budget/ cmd/styx/
git add internal/budget/budget.go internal/budget/budget_test.go cmd/styx/dispatch.go
git commit -m "feat(budget): channel_health + retry_after read queries (no new state)"
```

---

### Task 4: intel — Staleness helper without double disk read

**Files:**
- Modify: `internal/intel/intel.go` (extract `Staleness(proj, idx)`; make `IsStale` delegate)
- Test: `internal/intel/intel_test.go`

**Interfaces:**
- Consumes: existing `Index`, `MaxAgeBeforeStale`, `MaxCommitsBeforeStale`, `commitsSince`.
- Produces (used by Task 7's `get_intel`/`refresh_intel`): `func Staleness(proj config.Project, idx *Index) (bool, string)` — the age-then-commits check against an already-loaded index, no disk read.

- [ ] **Step 1: Write the failing test** — append to `internal/intel/intel_test.go`

```go
func TestStaleness_FreshAndAged(t *testing.T) {
	proj := config.Project{ID: "p", Name: "p", Path: t.TempDir()} // no git -> commitsSince returns 0 baseline for GitHead ""
	fresh := &Index{BuiltAt: time.Now().UTC(), GitHead: ""}
	if stale, reason := Staleness(proj, fresh); stale {
		t.Fatalf("fresh index reported stale: %q", reason)
	}
	aged := &Index{BuiltAt: time.Now().UTC().Add(-8 * 24 * time.Hour), GitHead: ""}
	stale, reason := Staleness(proj, aged)
	if !stale {
		t.Fatal("8-day-old index not reported stale")
	}
	if reason == "" {
		t.Fatal("stale index returned empty reason")
	}
}
```

(Note: `GitHead: ""` makes `commitsSince` return 0, so only the age rule can trip — keeping the test independent of the temp dir's git state.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/intel/ -run TestStaleness -v`
Expected: FAIL — `undefined: Staleness`.

- [ ] **Step 3: Extract `Staleness` and delegate from `IsStale`**

Read `internal/intel/intel.go` lines 187–203 (`IsStale`). It currently `Load`s the index, then does the age check (line ~195) and the commits check (line ~198). Refactor so the age/commits logic lives in a new exported function taking an already-loaded `*Index`:

```go
// Staleness reports whether an already-loaded index is stale and why, using the
// same age-then-commits rule as IsStale but without re-reading from disk. Age
// rule: BuiltAt older than MaxAgeBeforeStale. Commit rule: more than
// MaxCommitsBeforeStale commits since GitHead. Returns (false, "") when fresh.
func Staleness(proj config.Project, idx *Index) (bool, string) {
	if time.Since(idx.BuiltAt) > MaxAgeBeforeStale {
		return true, fmt.Sprintf("index is %d days old", int(time.Since(idx.BuiltAt).Hours()/24))
	}
	if n := commitsSince(proj.Path, idx.GitHead); n > MaxCommitsBeforeStale {
		return true, fmt.Sprintf("%d commits since build (max %d)", n, MaxCommitsBeforeStale)
	}
	return false, ""
}
```

Then rewrite `IsStale` to keep its existing "no index built yet" / "load failed" branches but delegate the fresh-index checks:

```go
func IsStale(proj config.Project) (bool, string, error) {
	idx, err := Load(proj)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return true, "no index built yet", nil
		}
		return true, "load failed: " + err.Error(), nil
	}
	stale, reason := Staleness(proj, idx)
	return stale, reason, nil
}
```

Preserve whatever exact wording/imports `IsStale` used for the not-exist and load-failed branches (match the file). `Staleness` must reproduce the exact age/commit reason strings from the original so no other test regresses.

- [ ] **Step 4: Run intel tests**

Run: `go test ./internal/intel/ -v`
Expected: PASS — `TestStaleness_FreshAndAged` green; all existing intel tests (including any that exercise `IsStale`) still green.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/intel/
git add internal/intel/intel.go internal/intel/intel_test.go
git commit -m "refactor(intel): extract Staleness(proj, idx) so get_intel avoids double read"
```

---

### Task 5: mcp route v2 — floor / tier_plan / blocked / classified_signals / retry_after

**Files:**
- Modify: `cmd/styx/mcp.go` (extend `routeResult`, add `tierPlan`, extend `handleRoute`)
- Test: `cmd/styx/mcp_test.go`

**Interfaces:**
- Consumes: `router.Decision.{Floor,TierPlan,BlockedByBudget}` (Task 2); `budget.Tracker.RetryAfter` (Task 3); existing v1 `handleRoute` signature `handleRoute(ctx, r *router.Router, t *budget.Tracker, a routeArgs) (routeResult, error)`.
- Produces (JSON additive fields on `route`): `classified_signals`, `floor`, `tier_plan{acceptable,chosen,escalate_to}`, `blocked_by_budget`, `retry_after_s`.
- Add imports to `mcp.go` as needed: `strings`.

- [ ] **Step 1: Write the failing tests** — append to `cmd/styx/mcp_test.go`

```go
func TestHandleRoute_V2Fields_ComplexClassifiedAndFloor(t *testing.T) {
	r, tr := testRouterAndTracker(t) // rule: {Verb:"build", Use:"codex:gpt-5"} per v1 fixture
	ctx := context.Background()
	// No signals passed: handler must classify. "refactor" yields the "complex" signal.
	res, err := handleRoute(ctx, r, tr, routeArgs{Task: "refactor the auth architecture", Verb: "build"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range res.ClassifiedSignals {
		if s == "complex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("classified_signals = %v, want to include 'complex'", res.ClassifiedSignals)
	}
	if res.Floor != "sonnet" {
		t.Fatalf("floor = %q, want sonnet for a complex task", res.Floor)
	}
	if res.TierPlan == nil || res.TierPlan.Chosen == "" {
		t.Fatalf("tier_plan missing: %+v", res.TierPlan)
	}
}

func TestHandleRoute_V2_BlockedByBudgetSetsRetryAfter(t *testing.T) {
	tr, err := budget.New(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })
	// A plan+complex rule whose only floor-clearing targets (claude, codex) are
	// both over cap; cooldown gives a concrete retry hint.
	ctx := context.Background()
	if err := tr.MarkCooldown(ctx, "claude", time.Now().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	r := router.FromConfig(config.Routing{
		Budget: config.BudgetCaps{
			Claude: config.ChannelCap{CapPct: 80},
			Codex:  config.ChannelCap{CapPct: 80},
		},
		Rules: []config.Rule{
			{Verb: "plan", Signals: []string{"complex"}, Use: "claude:opus", Fallback: []string{"codex"}},
		},
	}, overCapBudget{}) // both claude+codex reported over cap; see helper below
	res, err := handleRoute(ctx, r, tr, routeArgs{Task: "redesign the whole thing", Verb: "plan", Signals: []string{"complex"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.BlockedByBudget {
		t.Fatalf("blocked_by_budget = false, want true when all capable channels over cap")
	}
	if res.Channel == "ollama" {
		t.Fatal("blocked route returned below-floor ollama")
	}
	if res.RetryAfterS <= 0 {
		t.Fatalf("retry_after_s = %d, want > 0 (claude cooldown ~30m)", res.RetryAfterS)
	}
}

// overCapBudget reports every channel as 100% used so overCap() fires.
type overCapBudget struct{}

func (overCapBudget) UsedPct(ctx context.Context, channel string) (float64, error) { return 100, nil }
```

(If the v1 fixture already defines an over-cap budget stub, reuse it instead of `overCapBudget`.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/styx/ -run 'TestHandleRoute_V2' -v`
Expected: FAIL — `res.ClassifiedSignals undefined`, `res.Floor undefined`, `res.TierPlan undefined`, `res.BlockedByBudget undefined`, `res.RetryAfterS undefined`.

- [ ] **Step 3: Add the `tierPlan` result type and extend `routeResult`** in `cmd/styx/mcp.go`

Add:

```go
type tierPlan struct {
	Acceptable []string `json:"acceptable"`
	Chosen     string   `json:"chosen"`
	EscalateTo string   `json:"escalate_to,omitempty"`
}
```

Append these fields to the existing `routeResult` struct (do NOT reorder or rename the v1 fields):

```go
	ClassifiedSignals []string   `json:"classified_signals,omitempty"`
	Floor             string     `json:"floor,omitempty"`
	TierPlan          *tierPlan  `json:"tier_plan,omitempty"`
	BlockedByBudget   bool       `json:"blocked_by_budget"`
	RetryAfterS       int        `json:"retry_after_s,omitempty"`
```

- [ ] **Step 4: Extend `handleRoute`** in `cmd/styx/mcp.go`

Read the current v1 `handleRoute`. It computes `sigs` (from `a.Signals` or `signals.Extract`), builds `router.Request`, calls `r.Route`, flattens `dec.Fallback`, sets `Reasoning`/`Budget`. Keep all of that. Immediately before the final `return routeResult{...}, nil`, ensure the `sigs` slice is in scope, and populate the new fields on the returned struct:

```go
	// sigs is the signal slice used for routing (Extracted here when the caller
	// omitted them). Surface it and the floor plan.
	res.ClassifiedSignals = sigs
	res.Floor = dec.Floor
	res.BlockedByBudget = dec.BlockedByBudget
	res.TierPlan = &tierPlan{
		Acceptable: dec.TierPlan.Acceptable,
		Chosen:     dec.TierPlan.Chosen,
		EscalateTo: dec.TierPlan.EscalateTo,
	}
	if dec.BlockedByBudget {
		res.RetryAfterS = minRetryAfter(ctx, t, dec.TierPlan.Acceptable)
	}
```

If v1's `handleRoute` returns a struct literal directly, refactor it to build a named `res := routeResult{...}` first, then set the fields above, then `return res, nil`. (`res.TierPlan` is always populated — v1 consumers ignore unknown JSON keys.)

Add the helper:

```go
// minRetryAfter returns the smallest positive RetryAfter across the acceptable
// targets' channels, or 0 when none has a known retry window.
func minRetryAfter(ctx context.Context, t *budget.Tracker, acceptable []string) int {
	best := 0
	for _, cm := range acceptable {
		ch := cm
		if i := strings.IndexByte(cm, ':'); i >= 0 {
			ch = cm[:i]
		}
		s, err := t.RetryAfter(ctx, ch)
		if err != nil || s <= 0 {
			continue
		}
		if best == 0 || s < best {
			best = s
		}
	}
	return best
}
```

- [ ] **Step 5: Run route tests + the v1 route tests**

Run: `go test ./cmd/styx/ -run 'TestHandleRoute|TestMCPTools_EndToEndRoute' -v`
Expected: PASS — new v2 tests green; v1 `handleRoute`/end-to-end route tests still green (additive fields don't disturb v1 assertions).

- [ ] **Step 6: Commit**

```bash
gofmt -w cmd/styx/
git add cmd/styx/mcp.go cmd/styx/mcp_test.go
git commit -m "feat(mcp): route v2 — floor, tier_plan, blocked_by_budget, retry_after"
```

---

### Task 6: channel_health tool

**Files:**
- Modify: `cmd/styx/mcp.go` (args/result/schema/handler; register in `mcpTools`)
- Test: `cmd/styx/mcp_test.go`

**Interfaces:**
- Consumes: `budget.Tracker.ChannelHealth` (Task 3), `budget.BreakerThreshold`, `budget.BreakerWindow`, `defaultChannelNames` (v1).
- Produces MCP tool `channel_health`.

- [ ] **Step 1: Write the failing test** — append to `cmd/styx/mcp_test.go`

```go
func TestHandleChannelHealth_AllAndSingle(t *testing.T) {
	_, tr := testRouterAndTracker(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := tr.Record(ctx, budget.Event{Channel: "claude", Verb: "plan", Success: false, ErrorKind: "5xx"}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := handleChannelHealth(ctx, tr, channelHealthArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(defaultChannelNames) {
		t.Fatalf("got %d channels, want %d", len(all), len(defaultChannelNames))
	}
	var claude *channelHealthResult
	for i := range all {
		if all[i].Channel == "claude" {
			claude = &all[i]
		}
	}
	if claude == nil || !claude.CircuitOpen || claude.FailuresRecent != 3 || claude.ErrorKinds["server"] != 3 {
		t.Fatalf("claude health wrong: %+v", claude)
	}

	one, err := handleChannelHealth(ctx, tr, channelHealthArgs{Channel: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Channel != "codex" || one[0].CircuitOpen {
		t.Fatalf("single-channel health wrong: %+v", one)
	}
}

func TestMCPTools_EndToEndChannelHealth(t *testing.T) {
	r, tr := testRouterAndTracker(t)
	a := &app{router: r, tracker: tr}
	srv := mcpserver.New("styx", "test", mcpTools(a))
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"channel_health","arguments":{}}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := srv.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"circuit_open"`) || !strings.Contains(out.String(), `"error_kinds"`) {
		t.Fatalf("channel_health output missing fields:\n%s", out.String())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/styx/ -run 'ChannelHealth' -v`
Expected: FAIL — `channelHealthArgs`/`channelHealthResult`/`handleChannelHealth` undefined; `channel_health` not registered.

- [ ] **Step 3: Add args, result, schema, handler** in `cmd/styx/mcp.go`

```go
type channelHealthArgs struct {
	Channel string `json:"channel"`
}

type channelHealthResult struct {
	Channel            string         `json:"channel"`
	CircuitOpen        bool           `json:"circuit_open"`
	FailuresRecent     int            `json:"failures_recent"`
	WindowS            int            `json:"window_s"`
	ErrorKinds         map[string]int `json:"error_kinds"`
	CooldownRemainingS float64        `json:"cooldown_remaining_s"`
}

var channelHealthSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"channel": map[string]any{
			"type":        "string",
			"description": "Channel to inspect (claude|codex|agy|ollama). Omit for all channels.",
		},
	},
}

// handleChannelHealth reports circuit/failure/cooldown state per channel from the
// existing usage log — a consumer can avoid a flaky provider before dispatch.
func handleChannelHealth(ctx context.Context, t *budget.Tracker, a channelHealthArgs) ([]channelHealthResult, error) {
	channels := defaultChannelNames
	if a.Channel != "" {
		channels = []string{a.Channel}
	}
	out := make([]channelHealthResult, 0, len(channels))
	for _, ch := range channels {
		h, err := t.ChannelHealth(ctx, ch, budget.BreakerThreshold, budget.BreakerWindow)
		if err != nil {
			return nil, fmt.Errorf("channel_health %s: %w", ch, err)
		}
		out = append(out, channelHealthResult{
			Channel:            h.Channel,
			CircuitOpen:        h.CircuitOpen,
			FailuresRecent:     h.FailuresRecent,
			WindowS:            h.WindowSeconds,
			ErrorKinds:         h.ErrorKinds,
			CooldownRemainingS: h.CooldownRemainingSeconds,
		})
	}
	return out, nil
}
```

- [ ] **Step 4: Register the tool in `mcpTools`** — append this `mcpserver.Tool` literal to the slice `mcpTools(a)` returns:

```go
		{
			Name:        "channel_health",
			Description: "Report each channel's circuit-breaker state, recent failure count, per-kind error buckets, and remaining cooldown — so a consumer can avoid a flaky provider before dispatch.",
			InputSchema: channelHealthSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in channelHealthArgs
				if len(raw) > 0 {
					if err := json.Unmarshal(raw, &in); err != nil {
						return nil, fmt.Errorf("channel_health: invalid arguments: %w", err)
					}
				}
				return handleChannelHealth(ctx, a.tracker, in)
			},
		},
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/styx/ -run 'ChannelHealth' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w cmd/styx/
git add cmd/styx/mcp.go cmd/styx/mcp_test.go
git commit -m "feat(mcp): channel_health tool"
```

---

### Task 7: get_intel + refresh_intel tools + strict project resolver

**Files:**
- Modify: `cmd/styx/mcp.go` (`resolveProjectStrict`; intel args/results/schemas/handlers; register both tools)
- Test: `cmd/styx/mcp_test.go`

**Interfaces:**
- Consumes: `target.Resolve`, `channel.ClassifiedError`/`channel.ErrKindOther`, `intel.Load`/`intel.Staleness` (Task 4)/`intel.Build`/`intel.ToMarkdown`/`intel.WriteContextMD`, `intel.Index`/`Conventions`/`KeySymbol`/`Module`/`Commit`/`Todo`, `agyAdapter`/`rawChannel` (existing in `cmd/styx`), `a.channels["agy"]`, `a.progress`.
- Produces MCP tools `get_intel`, `refresh_intel`, and `func resolveProjectStrict(name string) (config.Project, error)` (reused by Task 8).
- Add imports to `mcp.go`: `errors`, `os`, `github.com/ishaanbatra/styx/internal/config`, `.../internal/intel`, `.../internal/target`, `.../internal/progress` (for `handleRefreshIntel`'s `*progress.Tracker` param), and `.../internal/channel` (for `ClassifiedError`/`ErrKindOther` — add if the v1 file doesn't already import it). `intel.AgyClient` comes from the `intel` import; `agyAdapter`/`rawChannel` already exist in package `main`.

- [ ] **Step 1: Write the failing tests** — append to `cmd/styx/mcp_test.go`

```go
func TestResolveProjectStrict_Errors(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if _, err := resolveProjectStrict(""); err == nil {
		t.Fatal("empty project accepted; want required-error")
	}
	_, err := resolveProjectStrict("definitely-not-a-registered-project")
	if err == nil || !strings.Contains(err.Error(), "unknown project") {
		t.Fatalf("unknown project err = %v, want 'unknown project'", err)
	}
	var ce *channel.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("err not classified: %v", err)
	}
}

func TestHandleGetIntel_WholeSectionAndStale(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := config.Project{ID: "demo", Name: "demo", Path: t.TempDir(), Language: "go"}
	idx := &intel.Index{
		Project: "demo", Path: proj.Path, Language: "go",
		BuiltAt:     time.Now().UTC(),
		SchemaVersion: 1,
		Conventions: intel.Conventions{TestFramework: "go test"},
		KeySymbols:  []intel.KeySymbol{{Name: "Router", File: "router.go", Why: "central"}},
	}
	if err := intel.Save(proj, idx); err != nil {
		t.Fatal(err)
	}
	// whole index
	whole, err := handleGetIntel(context.Background(), proj, "")
	if err != nil {
		t.Fatal(err)
	}
	if whole.Index == nil || whole.Index.Conventions.TestFramework != "go test" {
		t.Fatalf("whole index missing conventions: %+v", whole.Index)
	}
	if whole.Stale {
		t.Fatalf("just-built index reported stale: %q", whole.StalenessReason)
	}
	// section filter
	sec, err := handleGetIntel(context.Background(), proj, "key_symbols")
	if err != nil {
		t.Fatal(err)
	}
	if sec.Index != nil || len(sec.KeySymbols) != 1 || sec.KeySymbols[0].Name != "Router" {
		t.Fatalf("key_symbols section wrong: %+v", sec)
	}
	// unknown section
	if _, err := handleGetIntel(context.Background(), proj, "bogus"); err == nil {
		t.Fatal("unknown section accepted")
	}
}

func TestHandleGetIntel_NotBuiltIsStaleNotError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	proj := config.Project{ID: "never", Name: "never", Path: t.TempDir()}
	res, err := handleGetIntel(context.Background(), proj, "")
	if err != nil {
		t.Fatalf("missing index should not error: %v", err)
	}
	if !res.Stale || res.StalenessReason == "" {
		t.Fatalf("missing index: want stale with reason, got %+v", res)
	}
}
```

- [ ] **Step 2: Run to verify failing**

Run: `go test ./cmd/styx/ -run 'ResolveProjectStrict|HandleGetIntel' -v`
Expected: FAIL — undefined `resolveProjectStrict`, `handleGetIntel`, `intelResult`.

- [ ] **Step 3: Add `resolveProjectStrict`** in `cmd/styx/mcp.go`

```go
// resolveProjectStrict resolves an MCP project argument via the shared target
// resolver (alias or path) WITHOUT any cwd fallback — an MCP server's cwd is not
// the caller's project. Empty or unknown project is a classified error, never a
// silent default.
func resolveProjectStrict(name string) (config.Project, error) {
	if strings.TrimSpace(name) == "" {
		return config.Project{}, &channel.ClassifiedError{
			Kind: channel.ErrKindOther,
			Err:  errors.New("project is required"),
		}
	}
	proj, err := target.Resolve(target.Spec{Alias: name})
	if err != nil {
		return config.Project{}, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
	}
	return proj, nil
}
```

- [ ] **Step 4: Add intel args/result/schemas and `handleGetIntel`** in `cmd/styx/mcp.go`

```go
type getIntelArgs struct {
	Project string `json:"project"`
	Section string `json:"section"`
}

type refreshIntelArgs struct {
	Project string `json:"project"`
}

// intelResult carries either the whole index (section == "") or exactly one
// section slice. Stale/StalenessReason always report freshness; a read never
// rebuilds.
type intelResult struct {
	Project         string       `json:"project"`
	Stale           bool         `json:"stale"`
	StalenessReason string       `json:"staleness_reason,omitempty"`
	Section         string       `json:"section,omitempty"`
	Index           *intel.Index `json:"index,omitempty"`

	Conventions   *intel.Conventions `json:"conventions,omitempty"`
	KeySymbols    []intel.KeySymbol  `json:"key_symbols,omitempty"`
	Modules       []intel.Module     `json:"modules,omitempty"`
	FileTree      []string           `json:"file_tree,omitempty"`
	RecentCommits []intel.Commit     `json:"recent_commits,omitempty"`
	OpenTodos     []intel.Todo       `json:"open_todos,omitempty"`
}

var getIntelSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"project": map[string]any{"type": "string", "description": "Registered project alias or path (required)."},
		"section": map[string]any{
			"type":        "string",
			"description": "Optional slice: conventions | key_symbols | modules | file_tree | recent_commits | open_todos. Omit for the whole index.",
		},
	},
	"required": []any{"project"},
}

var refreshIntelSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"project": map[string]any{"type": "string", "description": "Registered project alias or path (required)."},
	},
	"required": []any{"project"},
}

// handleGetIntel loads the persisted index (never rebuilds on read), reports
// staleness, and returns the whole index or one section.
func handleGetIntel(ctx context.Context, proj config.Project, section string) (intelResult, error) {
	idx, err := intel.Load(proj)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return intelResult{Project: proj.Name, Stale: true, StalenessReason: "no index built yet"}, nil
		}
		return intelResult{}, fmt.Errorf("get_intel load %s: %w", proj.Name, err)
	}
	stale, reason := intel.Staleness(proj, idx)
	res := intelResult{Project: proj.Name, Stale: stale, StalenessReason: reason, Section: section}
	switch section {
	case "":
		res.Index = idx
	case "conventions":
		c := idx.Conventions
		res.Conventions = &c
	case "key_symbols":
		res.KeySymbols = idx.KeySymbols
	case "modules":
		res.Modules = idx.Modules
	case "file_tree":
		res.FileTree = idx.FileTree
	case "recent_commits":
		res.RecentCommits = idx.RecentCommits
	case "open_todos":
		res.OpenTodos = idx.OpenTodos
	default:
		return intelResult{}, fmt.Errorf("get_intel: unknown section %q", section)
	}
	return res, nil
}

// handleRefreshIntel rebuilds the index via agy, rewrites .claude/context.md, and
// returns the fresh result. This is the deliberate write/refresh path.
func handleRefreshIntel(ctx context.Context, proj config.Project, agy intel.AgyClient, prog *progress.Tracker) (intelResult, error) {
	idx, err := intel.Build(ctx, proj, agy, prog)
	if err != nil {
		return intelResult{}, fmt.Errorf("refresh_intel build %s: %w", proj.Name, err)
	}
	if _, err := intel.WriteContextMD(proj.Path, intel.ToMarkdown(idx)); err != nil {
		return intelResult{}, fmt.Errorf("refresh_intel write context %s: %w", proj.Name, err)
	}
	stale, reason := intel.Staleness(proj, idx)
	return intelResult{Project: proj.Name, Stale: stale, StalenessReason: reason, Index: idx}, nil
}
```

- [ ] **Step 5: Register both tools in `mcpTools`** — append to the returned slice:

```go
		{
			Name:        "get_intel",
			Description: "Return the per-project codebase intelligence index styx maintains (file tree, module summaries, conventions, key symbols, recent commits, open TODOs). Pass section to return one slice. Reports staleness; never rebuilds on read.",
			InputSchema: getIntelSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in getIntelArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("get_intel: invalid arguments: %w", err)
				}
				proj, err := resolveProjectStrict(in.Project)
				if err != nil {
					return nil, err
				}
				return handleGetIntel(ctx, proj, in.Section)
			},
		},
		{
			Name:        "refresh_intel",
			Description: "Rebuild the per-project intelligence index (walk + convention sniff + agy module/key-symbol summaries) and return the fresh result. The deliberate write path.",
			InputSchema: refreshIntelSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in refreshIntelArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("refresh_intel: invalid arguments: %w", err)
				}
				proj, err := resolveProjectStrict(in.Project)
				if err != nil {
					return nil, err
				}
				ag, ok := a.channels["agy"]
				if !ok {
					return nil, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: errors.New("refresh_intel: agy channel unavailable")}
				}
				return handleRefreshIntel(ctx, proj, &agyAdapter{ch: rawChannel(ag)}, a.progress)
			},
		},
```

- [ ] **Step 6: Run tests**

Run: `go test ./cmd/styx/ -run 'ResolveProjectStrict|HandleGetIntel' -v`
Expected: PASS.

(No unit test drives `handleRefreshIntel` end-to-end because `intel.Build` shells out to agy; its per-piece pieces are covered by `internal/intel` tests. An end-to-end `refresh_intel` MCP test would need an agy fake channel in the `app` fixture — add only if the v1 fixture already supports injecting a fake channel; otherwise rely on the intel package's own `Build` coverage and this handler's thin wrapping.)

- [ ] **Step 7: Commit**

```bash
gofmt -w cmd/styx/
git add cmd/styx/mcp.go cmd/styx/mcp_test.go
git commit -m "feat(mcp): get_intel + refresh_intel tools; strict project resolver"
```

---

### Task 8: recall tool

**Files:**
- Modify: `cmd/styx/mcp.go` (recall args/result/schema/handler; register tool)
- Test: `cmd/styx/mcp_test.go`

**Interfaces:**
- Consumes: `resolveProjectStrict` (Task 7); `memory.Open`/`Recall`/`NewOllamaEmbedder`/`Hit`/`Item`/`Kind`/`Embedder`/`Store`; `paths.MemoryDir`/`EnsureDir`; `a.routing.Brain.EmbedModel`; `channel.ClassifiedError`.
- Produces MCP tool `recall`.
- Add imports to `mcp.go`: `path/filepath`, `github.com/ishaanbatra/styx/internal/memory`, `.../internal/paths`.

- [ ] **Step 1: Write the failing tests** — append to `cmd/styx/mcp_test.go`

```go
// fakeEmb returns a fixed vector, or an error to simulate ollama-down.
type fakeEmb struct {
	vec []float32
	err error
}

func (f fakeEmb) Embed(ctx context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vec, nil
}

func TestHandleRecall_HitAndOllamaDownLoud(t *testing.T) {
	ctx := context.Background()
	proj := config.Project{ID: "demo", Name: "demo"}
	ps, err := memory.Open(filepath.Join(t.TempDir(), "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer ps.Close()
	gs, err := memory.Open(filepath.Join(t.TempDir(), "global.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer gs.Close()
	if _, err := ps.Add(ctx, memory.Item{Kind: memory.KindDecision, Text: "use codex as implementer", Confidence: 1, Embedding: []float32{1, 0, 0}}); err != nil {
		t.Fatal(err)
	}

	// normal hit: query vector aligned with the stored item.
	ok := fakeEmb{vec: []float32{1, 0, 0}}
	res, err := handleRecall(ctx, proj, ok, ps, gs, recallArgs{Project: "demo", Query: "who implements", K: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Text != "use codex as implementer" {
		t.Fatalf("recall hit wrong: %+v", res.Hits)
	}

	// ollama down: loud classified error, never empty-as-success.
	down := fakeEmb{err: errors.New(`embed call: Post "http://localhost:11434/api/embed": dial tcp: connect: connection refused`)}
	_, err = handleRecall(ctx, proj, down, ps, gs, recallArgs{Project: "demo", Query: "who implements"})
	if err == nil {
		t.Fatal("ollama-down returned nil error (empty-as-success)")
	}
	if !strings.Contains(err.Error(), "recall unavailable") {
		t.Fatalf("recall error not loud: %v", err)
	}
	var ce *channel.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("recall error not classified: %v", err)
	}
}

func TestHandleRecall_QueryRequired(t *testing.T) {
	ps, _ := memory.Open(filepath.Join(t.TempDir(), "p.db"))
	defer ps.Close()
	gs, _ := memory.Open(filepath.Join(t.TempDir(), "g.db"))
	defer gs.Close()
	_, err := handleRecall(context.Background(), config.Project{Name: "p"}, fakeEmb{vec: []float32{1}}, ps, gs, recallArgs{Project: "p", Query: ""})
	if err == nil {
		t.Fatal("empty query accepted")
	}
}
```

- [ ] **Step 2: Run to verify failing**

Run: `go test ./cmd/styx/ -run 'HandleRecall' -v`
Expected: FAIL — undefined `recallArgs`/`recallResult`/`handleRecall`.

- [ ] **Step 3: Add recall args/result/schema + handler** in `cmd/styx/mcp.go`

```go
const defaultRecallK = 5

type recallArgs struct {
	Project string `json:"project"`
	Query   string `json:"query"`
	K       int    `json:"k"`
}

type recallHit struct {
	Text       string  `json:"text"`
	Kind       string  `json:"kind"`
	Source     string  `json:"source,omitempty"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
}

type recallResult struct {
	Project string      `json:"project"`
	Hits    []recallHit `json:"hits"`
}

var recallSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"project": map[string]any{"type": "string", "description": "Registered project alias or path (required)."},
		"query":   map[string]any{"type": "string", "description": "What to recall (required)."},
		"k":       map[string]any{"type": "integer", "description": "Max results (default 5)."},
	},
	"required": []any{"project", "query"},
}

// handleRecall returns decayed top-k project + global memory. It degrades LOUD:
// any Recall failure (notably ollama embeddings down) becomes a classified error,
// never an empty result presented as success.
func handleRecall(ctx context.Context, proj config.Project, emb memory.Embedder, projStore, globalStore *memory.Store, a recallArgs) (recallResult, error) {
	if strings.TrimSpace(a.Query) == "" {
		return recallResult{}, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: errors.New("recall: query is required")}
	}
	k := a.K
	if k <= 0 {
		k = defaultRecallK
	}
	hits, err := memory.Recall(ctx, emb, a.Query, k, projStore, globalStore)
	if err != nil {
		return recallResult{}, &channel.ClassifiedError{
			Kind: channel.ErrKindOther,
			Err:  fmt.Errorf("recall unavailable (ollama embeddings): %w", err),
		}
	}
	out := recallResult{Project: proj.Name, Hits: make([]recallHit, 0, len(hits))}
	for _, h := range hits {
		out.Hits = append(out.Hits, recallHit{
			Text:       h.Item.Text,
			Kind:       string(h.Item.Kind),
			Source:     h.Item.Source,
			Score:      h.Score,
			Confidence: h.Item.Confidence,
		})
	}
	return out, nil
}
```

- [ ] **Step 4: Register the tool in `mcpTools`** — append; the closure opens the per-project + global stores and the Ollama embedder, mirroring the REPL wiring (`repl.go` ~547–557):

```go
		{
			Name:        "recall",
			Description: "Recall the top-k project-scoped long-term memories (decisions, facts, preferences) via semantic similarity with recency/confidence decay. Requires local Ollama embeddings; returns a loud error if unavailable.",
			InputSchema: recallSchema,
			Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
				var in recallArgs
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, fmt.Errorf("recall: invalid arguments: %w", err)
				}
				proj, err := resolveProjectStrict(in.Project)
				if err != nil {
					return nil, err
				}
				memDir, err := paths.MemoryDir()
				if err != nil {
					return nil, fmt.Errorf("recall: memory dir: %w", err)
				}
				if err := paths.EnsureDir(memDir); err != nil {
					return nil, fmt.Errorf("recall: ensure memory dir: %w", err)
				}
				projStore, err := memory.Open(filepath.Join(memDir, proj.ID+".db"))
				if err != nil {
					return nil, fmt.Errorf("recall: open project memory: %w", err)
				}
				defer projStore.Close()
				globalStore, err := memory.Open(filepath.Join(memDir, "global.db"))
				if err != nil {
					return nil, fmt.Errorf("recall: open global memory: %w", err)
				}
				defer globalStore.Close()
				emb := memory.NewOllamaEmbedder("http://localhost:11434", a.routing.Brain.EmbedModel)
				return handleRecall(ctx, proj, emb, projStore, globalStore, in)
			},
		},
```

- [ ] **Step 5: Run tests**

Run: `go test ./cmd/styx/ -run 'HandleRecall' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
gofmt -w cmd/styx/
git add cmd/styx/mcp.go cmd/styx/mcp_test.go
git commit -m "feat(mcp): recall tool with loud ollama-down classified error"
```

---

### Task 9: status string, full build/test, and drift-contract docs

**Files:**
- Modify: `cmd/styx/mcp.go` (`cmdMCP` status string)
- Modify: `docs/ARCHITECTURE.md`, `README.md`, `docs/superpowers/free-tier-tracker.md`
- Test: whole suite

**Interfaces:** none new.

- [ ] **Step 1: Update the `cmdMCP` ready message** in `cmd/styx/mcp.go`

Change the `logStatus` line inside `cmdMCP` from the v1 `"mcp server ready on stdio (route, budget_status, record_usage)"` to list all seven tools:

```go
	logStatus("mcp server ready on stdio (route, budget_status, record_usage, channel_health, get_intel, refresh_intel, recall)")
```

- [ ] **Step 2: Full build + vet + test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: PASS — everything builds and every package's tests are green.

- [ ] **Step 3: Update `docs/ARCHITECTURE.md`** (owner of `cmd/styx/**`, `internal/**`)

In the `mcpserver` subsystem section, document the four new tools (`channel_health`, `get_intel`, `refresh_intel`, `recall`) and the route-v2 additive fields (`classified_signals`, `floor`, `tier_plan`, `blocked_by_budget`, `retry_after_s`). Record the two value-shape decisions so consumers parse them correctly: `floor` is a bare capability-tier keyword (`local`/`haiku`/`sonnet`/`opus`), not a `channel:model` string; `channel_health.error_kinds` uses the friendly, zero-filled keys `timeout`/`rate_limit`/`server`/`other` (mapped from the raw stored `429`/`5xx` labels). In the Router section, add the capability-floor behavior: floor derived from signals (`internal/signals` tier map), degradation restricted to floor-clearing targets, and the loud `BlockedByBudget` refusal replacing the old chain-exhaustion silent-return. In the Budget section, note `ChannelHealth`/`RetryAfter` read methods. In the Intel section, note `Staleness(proj, idx)`. Bump the doc's `last_verified` date in the frontmatter to today (2026-07-01).

- [ ] **Step 4: Update `README.md`** — under the `styx mcp` entry, list the full v2 tool set (route, budget_status, record_usage, channel_health, get_intel, refresh_intel, recall) with one line each.

- [ ] **Step 5: Update `docs/superpowers/free-tier-tracker.md`** — add a line noting the MCP brain v2 (channel-health, task-fit floor, project knowledge) as the follow-on to topic #7.

- [ ] **Step 6: Commit**

```bash
gofmt -w cmd/styx/
git add cmd/styx/mcp.go docs/ARCHITECTURE.md README.md docs/superpowers/free-tier-tracker.md
git commit -m "docs(mcp-v2): document floor + four tools; bump last_verified; status string"
```

---

## Self-Review

**Spec coverage** — every spec section maps to a task:

| Spec requirement | Task |
|---|---|
| `route` v2: classifier when signals omitted → `classified_signals` (§1, criterion 3) | 5 (`ClassifiedSignals = sigs`) |
| `route` v2: capability `floor` derived from signals (§1.1, criterion 1) | 1 (`signals.Floor`) + 2 (router populates `Floor`) |
| `route` v2: `tier_plan{acceptable,chosen,escalate_to}`, never below-floor (§1.1–1.2, criterion 1) | 2 (`buildTierPlan`, floor-restricted degradation) + 5 (JSON) |
| `route` v2: loud `blocked_by_budget` + `retry_after_s`; chain-exhaustion bug fixed (§1.3, §"router change", criterion 2) | 2 (block loud, regression test) + 3 (`RetryAfter`) + 5 (`retry_after_s`) |
| `channel_health` from usage log, no new state (§2, criterion 4) | 3 (`ChannelHealth`) + 6 (tool) |
| `get_intel(project, section?)` + staleness, no rebuild on read (§3, criterion 5) | 4 (`Staleness`) + 7 (`handleGetIntel`) |
| `refresh_intel(project)` rebuild (§3, criterion 5) | 7 (`handleRefreshIntel`) |
| `recall(project, query, k?)`; ollama-down loud classified error (§4, criterion 6) | 8 (`handleRecall`) |
| Unknown project → classified error on all project-scoped tools (§Cross-cutting, criterion 7) | 7 (`resolveProjectStrict`) |
| Router floor mapping lives beside signal definitions (§"router change") | 1 (`internal/signals/floor.go`) |
| Additive output; v1 + OpenClaw registration unchanged (§"Backward compatibility", criterion 8) | 2 (append Decision fields) + 5 (append routeResult fields) |
| No SDKs / no new state (§Cross-cutting) | all — reads only existing subsystems |
| Docs same-commit + `make test` green (criterion 8) | 9 + each task's commit |
| Deferred (`run`, parallel output, reconcile, remote transport) — NOT built | — (explicitly out of scope) |

**Placeholder scan:** every code step shows complete Go; every run step shows the command and expected PASS/FAIL; no "TBD"/"handle errors"/"similar to Task N". Edits to existing functions (router degradation block, `IsStale`, v1 `handleRoute`, `Explain`) instruct the implementer to read the current file first and give the exact replacement/insertion text — required because those functions' surrounding lines were verified but not quoted line-for-line here.

**Type consistency:** `signals.Tier`/`TierOf`/`Floor` (Task 1) are the exact names Task 2 imports. `router.TierPlan{Acceptable,Chosen,EscalateTo}` (Task 2) maps 1:1 to `tierPlan` JSON (Task 5). `budget.ChannelHealth` fields (Task 3) map to `channelHealthResult` (Task 6). `intel.Staleness(proj, idx)` (Task 4) is called identically in Tasks 7. `resolveProjectStrict` (Task 7) is reused verbatim by Task 8. `budget.BreakerThreshold`/`BreakerWindow` (Task 3) are used by Task 6. `defaultRecallK`/`recallArgs`/`recallResult` consistent within Task 8. All confirmed against the live signatures gathered during planning (`memory.Store.Close`, `budget.WindowSession/WindowWeek`, `intel.Build/Load/ToMarkdown/WriteContextMD`, `target.Resolve`, `channel.ClassifiedError`/`ErrKindOther`, `paths.MemoryDir`, `agyAdapter`/`rawChannel`, `app.{routing,channels,progress,tracker,router}`).
