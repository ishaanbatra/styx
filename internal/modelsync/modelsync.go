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
