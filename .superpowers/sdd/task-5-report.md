# Task 5 Report: `styx mcp` command + tool assembly + dispatch wiring

## What was implemented

### `cmd/styx/mcp.go` (appended)

- **`const mcpServerVersion = "0.1.0"`** — version string advertised to MCP hosts via `mcpserver.New`.
- **`routeSchema`, `budgetStatusSchema`, `recordUsageSchema`** — `map[string]any` JSON Schema objects with typed properties, descriptions, and `required` arrays, passed as `Tool.InputSchema` to the mcpserver.
- **`mcpTools(a *app) []mcpserver.Tool`** — assembles the three `mcpserver.Tool` values (route, budget_status, record_usage). Each handler closures over `a.router`/`a.tracker`, unmarshals raw JSON arguments into the typed arg struct (`json.Unmarshal`), and delegates to the existing `handleRoute`/`handleBudgetStatus`/`handleRecordUsage` functions. `budget_status` defensively checks `len(raw) > 0` before unmarshal (its schema has no required fields; a host may omit the arguments object entirely).
- **`cmdMCP(a *app, args []string) error`** — constructs `mcpserver.New("styx", mcpServerVersion, mcpTools(a))`, logs readiness to stderr via `logStatus` (stdout is reserved for JSON-RPC protocol), and runs `srv.Serve(context.Background(), os.Stdin, os.Stdout)` returning on EOF or error.
- **Imports added**: `"encoding/json"`, `"os"`, `"github.com/ishaanbatra/styx/internal/mcpserver"`.

### `cmd/styx/dispatch.go` (modified)

Added `case "mcp": return cmdMCP(a, args)` to the **second** switch (after `loadApp()`, alongside `case "auto":`, `case "research":`, etc.). The `mcp` command needs the full `app{router, tracker}` from `loadApp()` for budget-aware routing — correctly placed in the second tier.

### `cmd/styx/mcp_test.go` (appended)

Added `TestMCPTools_EndToEndRoute`: constructs `a := &app{router: r, tracker: tr}` directly, builds the server via `mcpTools(a)`, drives initialize / tools/list / tools/call over a `strings.Reader` / `bytes.Buffer`, and asserts that all three tool names appear in the list response and that the route call returns `codex`. Imports added: `"bytes"`, `"strings"`, `"github.com/ishaanbatra/styx/internal/mcpserver"`.

### `docs/ARCHITECTURE.md` (updated, drift contract)

- Bumped `last_verified` to `2026-06-30T12:00:00Z`.
- Expanded the `mcp.go` bullet to document all new symbols: `mcpServerVersion`, the three schema vars, `mcpTools`, `cmdMCP`, and the stdout-purity constraint.
- Updated the `dispatch.go` bullet to note that `mcp` is an app verb dispatched in the second switch.

### `README.md` (updated, drift contract)

Added `mcp` to the One-shots + admin verb table with a one-line description.

## TDD evidence

1. Wrote the failing test first (`TestMCPTools_EndToEndRoute`) — compile-failed with `undefined: mcpTools` as expected.
2. Implemented `mcpTools` and `cmdMCP` — test passed.
3. All 34 tests in `cmd/styx/` pass; `go build ./...` and `go vet ./...` clean.

## Test results

```
go test ./cmd/styx/ -v → PASS (34/34 tests, 0.476s)
go build ./...         → OK
go vet ./...           → OK
gofmt -w cmd/styx/     → no diffs
```

## Files changed

- `cmd/styx/mcp.go` — new imports + schemas + mcpTools + cmdMCP + mcpServerVersion
- `cmd/styx/dispatch.go` — `case "mcp": return cmdMCP(a, args)` in second switch
- `cmd/styx/mcp_test.go` — new imports + TestMCPTools_EndToEndRoute
- `docs/ARCHITECTURE.md` — expanded mcp.go bullet, updated dispatch.go bullet, bumped last_verified
- `README.md` — added mcp verb to table

## Self-review findings

- **Stdout purity**: `cmdMCP` only writes to stdout via `srv.Serve(..., os.Stdout)`. The `logStatus` call before `Serve` writes to stderr only (as confirmed by `logStatus`'s implementation in dispatch.go). No `fmt.Println`/`fmt.Printf` in the mcp path.
- **budget_status nil-safe**: the handler only calls `json.Unmarshal` when `len(raw) > 0`, preventing a panic when an MCP host omits the arguments field for a schema with no required fields.
- **YAGNI**: No speculative additions — exactly what the brief specified.
- **Test hygiene**: `testRouterAndTracker` (from Tasks 2–4) is reused; the new test adds no setup duplication. The test is not table-driven because it covers one E2E flow, not multiple cases — appropriate.
- **No new dependencies**: stdlib (`encoding/json`, `os`) plus existing `internal/mcpserver`.

## Concerns

None. Implementation is complete, clean, and matches the brief exactly.
