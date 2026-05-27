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
