package budget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Outcome is one dispatch completion record — the learning substrate shared
// with the self-improvement digest (styx learn). Append-only; the single
// sanctioned mutation is RateOutcome stamping rating+note.
type Outcome struct {
	ID         int64
	CreatedAt  time.Time
	Project    string // stable project ID ("" = none)
	Thread     string // agent thread name ("" for one-shots)
	TaskID     string // background task id ("" for sync dispatches)
	CLI        string // claude | codex | agy | ollama
	Model      string
	Signals    string // comma-joined routing signals from signals.Extract
	Risk       string // read | edit | ship
	DurationS  float64
	TokensIn   int
	TokensOut  int
	ErrorKind  string // "" on success, else classified: timeout|killed|429|5xx|other
	Background bool
	Rating     string // "" (unrated) | "good" | "bad"
	Note       string
}

const outcomesSchema = `
CREATE TABLE IF NOT EXISTS outcomes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          INTEGER NOT NULL,
    project     TEXT    NOT NULL DEFAULT '',
    thread      TEXT    NOT NULL DEFAULT '',
    task_id     TEXT    NOT NULL DEFAULT '',
    cli         TEXT    NOT NULL,
    model       TEXT    NOT NULL DEFAULT '',
    signals     TEXT    NOT NULL DEFAULT '',
    risk        TEXT    NOT NULL DEFAULT '',
    duration_s  REAL    NOT NULL DEFAULT 0,
    tokens_in   INTEGER NOT NULL DEFAULT 0,
    tokens_out  INTEGER NOT NULL DEFAULT 0,
    error_kind  TEXT    NOT NULL DEFAULT '',
    background  INTEGER NOT NULL DEFAULT 0,
    rating      TEXT    NOT NULL DEFAULT '',
    note        TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS outcomes_ts ON outcomes (ts DESC);
`

// RecordOutcome appends one dispatch-completion row.
func (t *Tracker) RecordOutcome(ctx context.Context, o Outcome) error {
	bg := 0
	if o.Background {
		bg = 1
	}
	_, err := t.db.ExecContext(ctx,
		`INSERT INTO outcomes (ts, project, thread, task_id, cli, model, signals, risk,
		                       duration_s, tokens_in, tokens_out, error_kind, background, rating, note)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), o.Project, o.Thread, o.TaskID, o.CLI, o.Model, o.Signals, o.Risk,
		o.DurationS, o.TokensIn, o.TokensOut, o.ErrorKind, bg, o.Rating, o.Note)
	if err != nil {
		return fmt.Errorf("record outcome: %w", err)
	}
	return nil
}

// RateOutcome stamps a rating onto the MOST RECENT outcome row whose task_id
// or thread matches ref — the one sanctioned mutation of the outcomes table.
// Returns the rated row's id; no match is a loud error. An empty ref is
// rejected: task_id/thread default to ” for one-shot/sync dispatches, so an
// empty ref would otherwise silently rate an unrelated row.
func (t *Tracker) RateOutcome(ctx context.Context, ref string, ok bool, note string) (int64, error) {
	if ref == "" {
		return 0, fmt.Errorf("rate outcome: empty thread/task ref")
	}
	rating := "bad"
	if ok {
		rating = "good"
	}
	var id int64
	err := t.db.QueryRowContext(ctx,
		`SELECT id FROM outcomes WHERE task_id = ?1 OR thread = ?1 ORDER BY id DESC LIMIT 1`,
		ref).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("rate outcome: no outcome matches thread or task %q", ref)
	}
	if err != nil {
		return 0, fmt.Errorf("rate outcome: query %q: %w", ref, err)
	}
	if _, err := t.db.ExecContext(ctx,
		`UPDATE outcomes SET rating = ?, note = ? WHERE id = ?`, rating, note, id); err != nil {
		return 0, fmt.Errorf("rate outcome %d: %w", id, err)
	}
	return id, nil
}

// OutcomesSince returns outcome rows recorded at or after since, newest first.
func (t *Tracker) OutcomesSince(ctx context.Context, since time.Time) ([]Outcome, error) {
	rows, err := t.db.QueryContext(ctx,
		`SELECT id, ts, project, thread, task_id, cli, model, signals, risk,
		        duration_s, tokens_in, tokens_out, error_kind, background, rating, note
		 FROM outcomes WHERE ts >= ? ORDER BY id DESC`, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("read outcomes: %w", err)
	}
	defer rows.Close()
	var out []Outcome
	for rows.Next() {
		var o Outcome
		var ts int64
		var bg int
		if err := rows.Scan(&o.ID, &ts, &o.Project, &o.Thread, &o.TaskID, &o.CLI, &o.Model,
			&o.Signals, &o.Risk, &o.DurationS, &o.TokensIn, &o.TokensOut, &o.ErrorKind,
			&bg, &o.Rating, &o.Note); err != nil {
			return nil, fmt.Errorf("scan outcome: %w", err)
		}
		o.CreatedAt = time.Unix(ts, 0)
		o.Background = bg == 1
		out = append(out, o)
	}
	return out, rows.Err()
}
