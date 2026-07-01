# Using styx from OpenClaw

styx exposes its budget-aware routing brain as an MCP server. OpenClaw (and any
MCP host) can call it to decide which AI coding agent to use and to keep styx's
subscription budget accurate.

## Register

Add styx to `~/.openclaw/openclaw.json`:

```json
{
  "mcpServers": {
    "styx": { "command": "styx", "args": ["mcp"] }
  }
}
```

Restart the gateway. OpenClaw agents can now call:

- **`route`** — `{ "task": "add dark mode", "verb": "build" }` → chosen channel,
  model, fallback chain, reasoning, and budget snapshot.
- **`budget_status`** — `{ "channel": "claude" }` (or omit for all) → 5h / weekly
  message counts, limits, percentages, cooldowns.
- **`record_usage`** — `{ "channel": "claude", "messages": 1 }` → records what a
  run consumed.

## The `record_usage` convention (important)

When OpenClaw (not styx) executes the chosen agent, styx cannot see that usage.
**Call `record_usage` after each run** so the 5h/weekly windows stay correct. If
you don't, `route` and `budget_status` will still work but their budget numbers
go stale — `budget_status` reports `"stale": true` rather than silently guessing.

styx talks JSON-RPC over stdout; never write other output to stdout in the `mcp`
path. Status goes to stderr.
