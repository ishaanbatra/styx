// Package budget tracks per-channel usage via an append-only SQLite log
// and computes used-percentage against configured caps.
package budget

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ishaanbatra/styx/internal/paths"
)

// WindowSession is the rolling window for session-level message counts (Pro/Plus 5h limit).
const WindowSession = 5 * time.Hour

// WindowWeek is the rolling window for weekly message counts (168h = 7 days).
const WindowWeek = 168 * time.Hour

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

// healthKind maps a raw stored error_kind ("timeout"/"killed"/"429"/"5xx"/"") to the
// channel_health bucket label the tool contract exposes.
func healthKind(raw string) string {
	switch raw {
	case "timeout":
		return "timeout"
	case "killed":
		return "killed"
	case "429":
		return "rate_limit"
	case "5xx":
		return "server"
	default:
		return "other"
	}
}

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
	Model     string // model/tier used, e.g. "fable", "sonnet", "qwen2.5-coder:14b"
	TokensIn  int
	TokensOut int
	Success   bool
	ErrorKind string // "", "timeout", "killed", "429", "5xx", "other"
	Project   string // resolved project ID ("" = none)
	RunID     string // per-session / per-verb run correlation id ("" = none)
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
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if _, err := db.ExecContext(context.Background(), outcomesSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply outcomes schema: %w", err)
	}
	// v0.3 migration: per-model message counters for tier-aware degradation.
	if _, err := db.ExecContext(context.Background(),
		`ALTER TABLE usage ADD COLUMN model TEXT NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "duplicate column name") {
			db.Close()
			return nil, fmt.Errorf("migrate usage.model column: %w", err)
		}
	}
	// v0.4 migration: per-event project tag + run-id for usage attribution.
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
		`INSERT INTO usage (ts, channel, verb, model, tokens_in, tokens_out, success, error_kind, project, run_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), e.Channel, e.Verb, e.Model, e.TokensIn, e.TokensOut, successInt, e.ErrorKind, e.Project, e.RunID)
	if err != nil {
		return fmt.Errorf("record usage: %w", err)
	}
	return nil
}

// ModelCount returns the number of usage rows for (channel, model) within
// window. The REPL uses this for tier-aware degradation (fable -> opus).
func (t *Tracker) ModelCount(ctx context.Context, channel, model string, window time.Duration) (int, error) {
	cutoff := time.Now().Add(-window).Unix()
	row := t.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM usage WHERE channel = ? AND model = ? AND ts >= ?`,
		channel, model, cutoff)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count model messages for %s/%s: %w", channel, model, err)
	}
	return n, nil
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

// ChannelHealth reports whether channel's circuit is open, how many failures it
// had in the window, per-kind failure buckets, and remaining cooldown. Pure read
// over usage + cooldown; adds no state. Buckets are zero-filled with the friendly
// labels timeout/killed/rate_limit/server/other so a consumer can index them
// directly.
func (t *Tracker) ChannelHealth(ctx context.Context, channel string, threshold int, window time.Duration) (ChannelHealth, error) {
	cutoff := time.Now().Add(-window).Unix()
	h := ChannelHealth{
		Channel:       channel,
		WindowSeconds: int(window / time.Second),
		ErrorKinds:    map[string]int{"timeout": 0, "killed": 0, "rate_limit": 0, "server": 0, "other": 0},
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
