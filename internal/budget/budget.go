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

// WindowSession is the rolling window for session-level message counts (Pro/Plus 5h limit).
const WindowSession = 5 * time.Hour

// WindowWeek is the rolling window for weekly message counts (168h = 7 days).
const WindowWeek = 168 * time.Hour

// msgLimit holds per-channel message caps for the two rolling windows.
type msgLimit struct {
	session int
	weekly  int
}

// Tracker is the budget API. Methods are safe for concurrent use.
type Tracker struct {
	db        *sql.DB
	mu        sync.RWMutex
	caps      map[string]int // channel name -> token cap per window
	wind      map[string]time.Duration
	msgLimits map[string]msgLimit // channel -> {session, weekly}
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

	// Message-count fields (populated alongside the legacy token fields).
	SessionCount int     // messages (rows) in last 5h
	SessionLimit int     // configured 5h message cap (0 = unset)
	WeeklyCount  int     // messages (rows) in last 168h
	WeeklyLimit  int     // configured weekly message cap (0 = unset)
	SessionPct   float64 // SessionCount/SessionLimit*100 (0 if no limit)
	WeeklyPct    float64 // WeeklyCount/WeeklyLimit*100 (0 if no limit)
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
			"claude": 30 * 24 * time.Hour,
			"codex":  30 * 24 * time.Hour,
			"agy":    30 * 24 * time.Hour,
			"ollama": 24 * time.Hour, // unlimited but bounded for reporting
		},
		msgLimits: map[string]msgLimit{},
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

// SetMessageLimits configures per-channel message caps for the 5h and weekly windows.
func (t *Tracker) SetMessageLimits(channel string, sessionLimit, weeklyLimit int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.msgLimits[channel] = msgLimit{session: sessionLimit, weekly: weeklyLimit}
}

// messageCount returns the number of usage rows for channel within the given window.
func (t *Tracker) messageCount(ctx context.Context, channel string, window time.Duration) (int, error) {
	cutoff := time.Now().Add(-window).Unix()
	row := t.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage WHERE channel = ? AND ts >= ?`,
		channel, cutoff)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count messages for %s: %w", channel, err)
	}
	return n, nil
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

// State computes UsedPct + cooldown for a channel, and also populates
// message-count fields (SessionCount, WeeklyCount, SessionPct, WeeklyPct).
func (t *Tracker) State(ctx context.Context, channel string) (State, error) {
	st := State{Channel: channel, Window: t.Window(channel)}

	// --- legacy token-based logic (unchanged) ---
	total, err := t.totalTokens(ctx, channel, st.Window)
	if err != nil {
		return State{}, err
	}
	t.mu.RLock()
	cap := t.caps[channel]
	ml := t.msgLimits[channel]
	t.mu.RUnlock()
	if cap > 0 {
		st.UsedPct = float64(total) / float64(cap) * 100
		if st.UsedPct >= 100 {
			st.LimitHit = true
		}
	}

	// --- message-count fields ---
	sessionCount, err := t.messageCount(ctx, channel, WindowSession)
	if err != nil {
		return State{}, err
	}
	weeklyCount, err := t.messageCount(ctx, channel, WindowWeek)
	if err != nil {
		return State{}, err
	}
	st.SessionCount = sessionCount
	st.WeeklyCount = weeklyCount
	st.SessionLimit = ml.session
	st.WeeklyLimit = ml.weekly
	if ml.session > 0 {
		st.SessionPct = float64(sessionCount) / float64(ml.session) * 100
		if st.SessionPct >= 100 {
			st.LimitHit = true
		}
	}
	if ml.weekly > 0 {
		st.WeeklyPct = float64(weeklyCount) / float64(ml.weekly) * 100
		if st.WeeklyPct >= 100 {
			st.LimitHit = true
		}
	}

	// --- cooldown ---
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

// MarkCooldown sets a cooldown deadline for a channel. Subsequent State()
// calls report this until the time passes.
func (t *Tracker) MarkCooldown(ctx context.Context, channel string, until time.Time) error {
	_, err := t.db.ExecContext(ctx,
		`INSERT INTO cooldown (channel, until) VALUES (?, ?)
		 ON CONFLICT(channel) DO UPDATE SET until = excluded.until`,
		channel, until.Unix())
	if err != nil {
		return fmt.Errorf("mark cooldown for %s: %w", channel, err)
	}
	return nil
}

// UsedPct returns the higher of the session (5h) and weekly message-count
// percentages for channel — styx degrades on whichever ceiling it is nearest.
// Returns 0 when no message limits are configured for the channel.
func (t *Tracker) UsedPct(ctx context.Context, channel string) (float64, error) {
	st, err := t.State(ctx, channel)
	if err != nil {
		return 0, err
	}
	if st.WeeklyPct > st.SessionPct {
		return st.WeeklyPct, nil
	}
	return st.SessionPct, nil
}

// ShouldCircuitBreak returns true if `channel` has had >= `threshold`
// failures within the last `window`. The router uses this to short-circuit
// thrashing on a broken channel.
func (t *Tracker) ShouldCircuitBreak(ctx context.Context, channel string, threshold int, window time.Duration) (bool, error) {
	cutoff := time.Now().Add(-window).Unix()
	row := t.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage WHERE channel = ? AND ts >= ? AND success = 0`,
		channel, cutoff)
	var n int
	if err := row.Scan(&n); err != nil {
		return false, fmt.Errorf("count failures for %s: %w", channel, err)
	}
	return n >= threshold, nil
}
