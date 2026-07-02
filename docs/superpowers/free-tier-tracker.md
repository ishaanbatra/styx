# Free-Tier Capabilities — Session Tracker

Parent coordination note for the six independent sessions adding free-tier
capabilities to styx. Each session runs cold and cannot see the others — **this
file is the only shared source of truth.** Read it at start, write to it at end.

Created 2026-06-29. Owner: ishaanbatra. (Not owned by ARCHITECTURE.md's drift
contract — this is build-coordination scaffolding, not subsystem docs.)

The six topics: (1) Notifications · (2) Remote trigger / tunneling · (3) Web
search + fetch for research · (4) Free fast LLM "secretary" tier · (5) Durable
cross-machine memory · (6) CI as remote compute (spike).

---

## Protocol (every session follows this)

**At start**
1. Read this entire file.
2. Honor everything under **Shared Conventions**.
3. Check **Dependencies & Conflicts** for your topic — is something you depend on
   done yet? If not, either build the minimal piece you need or stop and flag it.
4. Claim your row in the **Status Board**: set status 🟡, fill in your branch name.

**During**
5. Any decision that could affect another topic (config schema, a shared
   interface, a new budget/quota table, a Keychain key name) → append it to the
   **Decision Log**. If it's cross-cutting, also promote it to **Shared
   Conventions** so later sessions inherit it.
6. Edit **only** your own row and your own decision-log entries. Treat every other
   topic's rows/entries as read-only.
7. Hit a clash with another topic's decision? Do **not** silently diverge — record
   it under **Conflicts** and flag the user.

**At end**
8. Update your status (🔵 in review / ✅ done / ⛔ blocked / ❌ dropped), link the
   branch/PR, note any blockers.

Status legend: ⬜ not started · 🟡 in progress · 🔵 in review · ✅ done · ⛔ blocked · ❌ dropped

---

## Shared Conventions (decide once, honor everywhere)

These are pre-seeded defaults. The first session to act on one confirms or revises
it here; later sessions conform.

- **Branching:** each topic gets its own branch off `main`, named
  `feature/free-<topic>` (e.g. `feature/free-notify`). Do **not** stack on
  `feature/multi-repo-orchestration` or on each other.
- **Secrets:** every token/credential goes through `internal/config/secrets.go`
  (Keychain). Never in TOML, env, or disk. Agree on Keychain key names here to
  avoid collisions. Reserved so far: `remote_bearer_token`, `notify_ntfy_topic`
  (both #2).
- **No SDKs:** outbound providers are HTTP-only (per CLAUDE.md). No API-key SDK
  clients.
- **Shared HTTP-provider shape:** topics #3 (search) and #4 (LLM tier) are both
  "HTTP provider + Keychain token + quota tracking." Whichever lands first defines
  the adapter shape; the second conforms instead of reinventing. Record the shape
  in the Decision Log.
- **Config:** each feature adds a clearly namespaced config section so structs
  don't clobber. Reserve: #1 `[notify]`, #2 `[remote]`, #3 `[research.search]`,
  #4 a channel entry in `routing.toml`, #5 `[sync]`. Confirm exact shape here.
- **Budget/quota:** free tiers have quotas. Extend the existing budget model
  (`internal/budget`), don't invent a parallel one. Degrade **loud** — never
  silent fallback.
- **Docs drift contract:** each feature lands its `docs/ARCHITECTURE.md` +
  `README.md` updates in the **same commit** as the code (per CLAUDE.md).

---

## Status Board

| # | Topic | Status | Branch | PR / outcome | Blockers |
|---|-------|--------|--------|--------------|----------|
| 1 | Notifications | 🟡 | feature/free-notify | — | — |
| 2 | Remote trigger / tunneling | ❌ | feature/free-remote | Dropped 2026-06-30 — redundant with OpenClaw (MCP pivot) | — |
| 3 | Web search + fetch (research) | 🟡 | feature/free-search | — | — |
| 4 | Free fast LLM "secretary" tier | ⬜ | — | — | build conversational-first — brain auto-routes chores, no user-facing verbs (see Decision Log 2026-06-29 #4) |
| 5 | Durable cross-machine memory | ❌ | feature/free-sync (not created) | Dropped 2026-06-29 — single-machine use confirmed; sync is low value | — |
| 6 | CI as remote compute (spike) | ❌ | feature/free-ci | Dropped 2026-06-30 — redundant with OpenClaw (MCP pivot) | — |
| 7 | styx MCP routing brain (OpenClaw consumer) | ✅ | feature/styx-mcp-brain | implemented Tasks 1–6, real-binary smoke-tested; pending final review + merge | — |

---

## Dependencies & Conflicts

**Dependencies**
- **#2 → #1.** Notifications is the *return channel* for the remote trigger. Build
  #1 first; #2 reuses its notify interface. If #1 isn't done when #2 starts, #2
  builds the minimal notify path it needs and records it for #1 to adopt.
- **#3 ↔ #4.** Both are HTTP-provider + Keychain + quota. Share the abstraction
  (see Shared Conventions). Coordinate the adapter shape via the Decision Log.
- **#5 ↔ #3/#4.** If #3 or #4 add quota tables to the budget DB, #5's sync must
  carry them. #5 should check the Decision Log for new tables before designing.
- **#4 internal.** Must keep `routing.toml` ↔ `cmd/styx/default_routing.go` in
  sync (per CLAUDE.md).

**Conflicts** (append as discovered)
- _none yet_

---

## Decision Log (append-only, per topic)

Format: `- [YYYY-MM-DD] (#topic) decision — rationale`

- [2026-06-29] (#5) Dropped durable cross-machine memory/sync — user confirmed
  effectively single-machine use, so the feature solves a problem that doesn't
  exist: styx state already persists durably on disk under `~/.config/styx/`
  with atomic tmp+rename writes, and there is no second machine for it to follow.
  True sync would add speculative complexity (hosted DB/KV, an HTTP sync
  provider, a Keychain token, and an unresolved conflict-resolution design) for
  no current payoff. No code written. Releases the reserved `[sync]` config
  section back to the pool, reserves no Keychain key, and makes the **#5 ↔ #3/#4**
  dependency moot — #3/#4 no longer need to consider sync carrying their quota
  tables. Minimal re-entry point if a second machine ever appears: a manual
  `styx backup` / `styx restore` tarball of `~/.config/styx/` state to free
  object storage (Cloudflare R2 / Backblaze B2) — backup/restore avoids the
  conflict-resolution question entirely; defer true sync until multi-machine is
  real.
- [2026-06-29] (#2) **Scoped "No daemons" relaxation.** Remote trigger ships an
  opt-in, **foreground** `styx serve` command (user-launched, Ctrl-C to stop; no
  background auto-start, no launchd unit). CLAUDE.md's bounded-exec rule is kept:
  the long-lived process only *accepts* requests; every triggered task runs under
  `context.WithTimeout`. Documented as such in ARCHITECTURE.md. Rationale: user
  chose lower-latency server+tunnel over the poll-based alternative. No other topic
  should assume styx is now generally daemonized — this is one narrow command.
- [2026-06-29] (#2) **Inbound transport = managed Cloudflare quick tunnel.**
  `styx serve` spawns `cloudflared tunnel --url http://127.0.0.1:<port>` as a
  managed child, scrapes the `*.trycloudflare.com` URL, and pushes it to the phone
  via notify on startup. Server binds loopback only. No domain/account; URL rotates
  per launch (acceptable — styx sends it). `[remote].tunnel="none"` is a BYO escape
  hatch (loopback-only).
- [2026-06-29] (#2) **Blast-radius policy (auth + scoping).** Bearer token
  (Keychain), constant-time compare, body-size cap, auth-failure rate limit. A
  remote run ALWAYS uses a fresh `styx/remote/<run-id>` branch in a throwaway **git
  worktree**, may push that branch + open a PR (`[remote].allow_pr`), but can
  **never merge, never push the default branch, never force-push** (enforced in
  code, not config — styx has no merge call anywhere). Registered projects only;
  one run at a time. Worst case for a leaked token = closeable junk PRs.
- [2026-06-29] (#2 → #1) **Minimal notify seam, for #1 to adopt.** #1 (Notifications)
  owns the return channel but has no committed code yet, so #2 ships the minimal
  path it needs and records the shape here for #1 to adopt or wrap (if #1 lands a
  different interface first, #2 conforms — flagged as a likely small merge-time
  reconciliation, not yet a conflict):
  - new package `internal/notify` with
    `type Notification struct { Title, Body, URL string; Level Level }` and
    `type Notifier interface { Notify(ctx context.Context, n Notification) error }`;
  - one impl `NtfyNotifier` → HTTPS POST to `https://ntfy.sh/<topic>`
    (Title→`Title` header, URL→`Click`, Level→`Priority`);
  - minimal `[notify]` config section: `provider = "ntfy"` (topic in Keychain).
- [2026-06-29] (#2) **Keychain keys reserved** (service `styx`): `remote_bearer_token`
  (serve refuses to start if unset) and `notify_ntfy_topic` (secret ntfy topic,
  acts as a capability URL). Also confirms config sections **`[remote]`** (this
  topic) and a minimal **`[notify]`** (seeded for #1).
- [2026-06-29] (#4) **Conversational-first: the brain auto-routes chores to the
  secretary; NOT standalone verbs.** styx's north star is to be COMPLETELY
  conversational — only trivial ops (e.g. `styx budget`) stay as standalone
  commands. The secretary must therefore be reached by *talking* to styx, not by
  typing chore verbs. This **supersedes** two things in the secretary spec
  (`feature/free-llm`): (a) the user-facing `classify` / `name` verbs as the front
  door, and (b) the spec non-goal that defers brain→secretary routing ("a future
  change could let the brain escalate to the secretary, but not here") — that
  routing is now **in scope and is the primary interface**. The REPL brain
  silently routes cheap chores (task classification, branch naming, diff
  summarization) to the secretary mid-conversation; the user never types a chore
  verb. `classify` / `name` / `summarize` may survive only as internal building
  blocks / escape hatches, never the primary UX. **Constraint preserved:** do NOT
  replace or weaken the brain's existing local-ollama route classification or its
  192-utterance accuracy gate — this *adds* a chore→secretary routing path, it does
  not change how the brain picks routes. **Precedence:** this entry wins over the
  spec; the #4 session reconciles the spec to conversational-first before building.
- [2026-06-30] (pivot) **styx becomes a standalone MCP routing brain; OpenClaw is a
  consumer, not a master.** Spike confirmed OpenClaw consumes external MCP servers
  but has NO budget-aware/task-fit agent selection — exactly styx's core, so styx
  fills a real gap rather than duplicating OpenClaw. styx keeps its hands
  (standalone execution unchanged); we ADD `styx mcp` (stdio JSON-RPC: `route`,
  `budget_status`, `record_usage`). **Drops #2 (remote trigger) and #6 (CI offload)**
  as redundant with OpenClaw. Defers #1/#3/#4 (#3/#4 still sharpen the brain).
  Spec: `specs/2026-06-29-styx-mcp-routing-brain-design.md`; plan:
  `plans/2026-06-29-styx-mcp-routing-brain.md`; implemented on
  `feature/styx-mcp-brain` (Tasks 1–6, real-binary smoke-tested).
- [2026-07-01] (#7 follow-on) **MCP brain v2: channel-health, task-fit floor,
  project knowledge.** Follow-on to #7's Tasks 1–6, on
  `feature/styx-mcp-brain-v2` (9 tasks). Adds a capability-floor concept
  (`internal/signals.Tier`/`Floor`) so `route` never degrades a `complex`/`deep`
  task below the tier it requires, with additive `floor`/`tier_plan`/
  `blocked_by_budget`/`retry_after_s` fields and a loud budget-exhaustion refusal
  replacing the old silent chain-exhaustion return; a `channel_health` tool
  (read-only over the existing usage log, no new state); and project-knowledge
  tools `get_intel`/`refresh_intel` (the existing codebase intel index) and
  `recall` (semantic memory) so an MCP host can query styx's project awareness,
  not just its routing. Spec:
  `specs/2026-07-01-styx-mcp-brain-v2-design.md`; plan:
  `plans/2026-07-01-styx-mcp-brain-v2.md`.
