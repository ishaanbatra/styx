// Package shipgate confirms outward-facing actions (push, PR, deploy) with the
// user before the MCP server executes them. The gate is server-side so the
// safety contract holds for any MCP host, not just Claude Code.
package shipgate

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Mode selects the confirmation strategy.
type Mode string

const (
	ModeHandshake Mode = "handshake" // token relay through the brain (default)
	ModeTTY       Mode = "tty"       // prompt on /dev/tty, bypassing the brain
	ModeOff       Mode = "off"       // no gate (scripting)
)

// Result is what the calling tool returns to the brain.
type Result struct {
	Allowed bool   `json:"allowed"`
	Token   string `json:"token,omitempty"`
	Message string `json:"message,omitempty"`
}

type pending struct {
	action  string
	expires time.Time
}

// Gate issues and validates single-use confirmation tokens.
type Gate struct {
	mode Mode
	ttl  time.Duration
	now  func() time.Time // test override

	// ConfirmTTY is the ModeTTY hook; tests inject, production opens /dev/tty.
	ConfirmTTY func(action string) (bool, error)

	mu      sync.Mutex
	pending map[string]pending
}

// New builds a Gate; unknown modes fall back to ModeHandshake (fail-closed).
func New(m Mode) *Gate {
	if m != ModeOff && m != ModeTTY {
		m = ModeHandshake
	}
	return &Gate{mode: m, ttl: 10 * time.Minute, now: time.Now,
		pending: map[string]pending{}, ConfirmTTY: ttyConfirm}
}

// Check gates one action. In handshake mode the first call (empty token)
// returns denied plus a token to relay; the second call with that token is
// allowed exactly once.
func (g *Gate) Check(action, token string) (Result, error) {
	switch g.mode {
	case ModeOff:
		return Result{Allowed: true}, nil
	case ModeTTY:
		ok, err := g.ConfirmTTY(action)
		if err != nil {
			return Result{}, fmt.Errorf("tty confirm: %w", err)
		}
		if !ok {
			return Result{Allowed: false, Message: "user refused " + action}, nil
		}
		return Result{Allowed: true}, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if token != "" {
		p, ok := g.pending[token]
		delete(g.pending, token) // single-use even on mismatch
		if ok && p.action == action && g.now().Before(p.expires) {
			return Result{Allowed: true}, nil
		}
		return Result{Allowed: false,
			Message: "invalid or expired confirmation token; call again without confirm_token for a fresh one"}, nil
	}
	tok, err := newToken()
	if err != nil {
		return Result{}, fmt.Errorf("mint confirmation token: %w", err)
	}
	g.pending[tok] = pending{action: action, expires: g.now().Add(g.ttl)}
	return Result{Allowed: false, Token: tok, Message: fmt.Sprintf(
		"%s requires user confirmation. Relay this token to the user verbatim and resubmit with confirm_token ONLY after the user explicitly types it back: %s",
		action, tok)}, nil
}

func newToken() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ttyConfirm asks on the controlling terminal directly so the brain can
// neither see nor fabricate the answer. Interleaves with host rendering;
// documented trade-off of the strict mode.
func ttyConfirm(action string) (bool, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("open /dev/tty: %w", err)
	}
	defer tty.Close()
	if _, err := fmt.Fprintf(tty, "\n[styx] allow %s? [y/N] ", action); err != nil {
		return false, err
	}
	buf := make([]byte, 8)
	n, err := tty.Read(buf)
	if err != nil {
		return false, fmt.Errorf("read /dev/tty: %w", err)
	}
	ans := strings.ToLower(strings.TrimSpace(string(buf[:n])))
	return ans == "y" || ans == "yes", nil
}
