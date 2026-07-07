// Package e2e drives a real `styx mcp` subprocess over JSON-RPC — the exact
// first-contact sequences an MCP conductor performs — with fakeagent CLIs on
// PATH and isolated XDG config. Hermetic by default (no quota, no network
// beyond a possibly-absent local ollama); STYX_E2E_LIVE=1 adds real-CLI smoke.
// Run via `make e2e`.
package e2e
