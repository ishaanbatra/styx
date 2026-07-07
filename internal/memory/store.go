// Package memory implements styx's per-project and global long-term memory:
// SQLite-backed items with ollama embeddings, recalled by brute-force cosine
// similarity (personal scale needs no vector DB).
package memory

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

// Kind labels what a memory item is.
type Kind string

const (
	KindDecision          Kind = "decision"
	KindTodo              Kind = "todo"
	KindDistillation      Kind = "distillation"
	KindBrief             Kind = "brief"
	KindFact              Kind = "fact"
	KindRoutingPreference Kind = "routing-preference"
	KindUserPreference    Kind = "user-preference" // how the user likes to work (styx learn)
	KindRetrospective     Kind = "retrospective"   // raw session notes; digest fuel, never injected
)

// Item is one memory record.
type Item struct {
	ID         int64
	Kind       Kind
	Text       string
	Source     string
	Project    string  // owning project ("" = global)
	Scope      string  // optional applicability hint, e.g. "reviews" or "general"
	Confidence float64 // 0..1; explicit facts high, one-off corrections low
	Embedding  []float32
	CreatedAt  time.Time
	LastUsedAt time.Time // bumped on recall; reserved for future eviction
	ConsumedAt time.Time // retrospectives: when the digest consumed this (zero = unconsumed)
}

// Store is one SQLite memory database (per-project or global).
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS memory (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    kind         TEXT    NOT NULL,
    text         TEXT    NOT NULL,
    source       TEXT    NOT NULL DEFAULT '',
    project      TEXT    NOT NULL DEFAULT '',
    scope        TEXT    NOT NULL DEFAULT '',
    confidence   REAL    NOT NULL DEFAULT 1,
    embedding    BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    consumed_at  INTEGER NOT NULL DEFAULT 0
);
`

// Open opens (creating if needed) the memory database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open memory db %s: %w", path, err)
	}
	if _, err := db.ExecContext(context.Background(), schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply memory schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// migrate adds provenance columns to memory DBs created before they existed.
func migrate(db *sql.DB) error {
	want := map[string]string{
		"project":      "TEXT NOT NULL DEFAULT ''",
		"scope":        "TEXT NOT NULL DEFAULT ''",
		"confidence":   "REAL NOT NULL DEFAULT 1",
		"last_used_at": "INTEGER NOT NULL DEFAULT 0",
		"consumed_at":  "INTEGER NOT NULL DEFAULT 0",
	}
	rows, err := db.Query(`PRAGMA table_info(memory)`)
	if err != nil {
		return fmt.Errorf("inspect memory schema: %w", err)
	}
	have := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("scan memory schema: %w", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read memory schema: %w", err)
	}
	rows.Close()
	for name, def := range want {
		if !have[name] {
			if _, err := db.Exec("ALTER TABLE memory ADD COLUMN " + name + " " + def); err != nil {
				return fmt.Errorf("add memory column %s: %w", name, err)
			}
		}
	}
	return nil
}

// Add inserts an item and returns its id.
func (s *Store) Add(ctx context.Context, it Item) (int64, error) {
	if it.Confidence == 0 {
		it.Confidence = 1
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO memory (kind, text, source, project, scope, confidence, embedding, created_at, last_used_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(it.Kind), it.Text, it.Source, it.Project, it.Scope, it.Confidence,
		encodeVec(it.Embedding), time.Now().Unix(), 0)
	if err != nil {
		return 0, fmt.Errorf("insert memory item: %w", err)
	}
	return res.LastInsertId()
}

// All returns every item in the store, newest first.
func (s *Store) All(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, text, source, project, scope, confidence, embedding, created_at, last_used_at, consumed_at FROM memory ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query memory: %w", err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var kind string
		var blob []byte
		var ts, lastUsed, consumed int64
		if err := rows.Scan(&it.ID, &kind, &it.Text, &it.Source, &it.Project, &it.Scope, &it.Confidence, &blob, &ts, &lastUsed, &consumed); err != nil {
			return nil, fmt.Errorf("scan memory item: %w", err)
		}
		it.Kind = Kind(kind)
		it.Embedding = decodeVec(blob)
		it.CreatedAt = time.Unix(ts, 0)
		if lastUsed != 0 {
			it.LastUsedAt = time.Unix(lastUsed, 0)
		}
		if consumed != 0 {
			it.ConsumedAt = time.Unix(consumed, 0)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// encodeVec packs a float32 vector as a little-endian blob.
func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// decodeVec unpacks a little-endian blob into a float32 vector.
func decodeVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// TopByKind returns up to k items of kind ranked by confidence × recency —
// the launch-guidance ranking for learned preferences (the recall decay
// curve with similarity fixed at 1). Newer, more confident memories outrank
// older ones, so preference drift resolves itself.
func (s *Store) TopByKind(ctx context.Context, kind Kind, k int) ([]Item, error) {
	items, err := s.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, it := range items {
		if it.Kind == kind {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		si := decayedScore(1, out[i].Confidence, time.Since(out[i].CreatedAt))
		sj := decayedScore(1, out[j].Confidence, time.Since(out[j].CreatedAt))
		return si > sj
	})
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// UnconsumedByKind returns items of kind not yet marked consumed, oldest
// first (digest order).
func (s *Store) UnconsumedByKind(ctx context.Context, kind Kind) ([]Item, error) {
	items, err := s.All(ctx) // newest first
	if err != nil {
		return nil, err
	}
	var out []Item
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind == kind && items[i].ConsumedAt.IsZero() {
			out = append(out, items[i])
		}
	}
	return out, nil
}

// MarkConsumed stamps consumed_at on the given items so future digests skip
// them. An empty id list is a no-op.
func (s *Store) MarkConsumed(ctx context.Context, ids []int64) error {
	now := time.Now().Unix()
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE memory SET consumed_at = ? WHERE id = ?`, now, id); err != nil {
			return fmt.Errorf("mark memory %d consumed: %w", id, err)
		}
	}
	return nil
}

// Delete removes one item — styx learn --forget. Unknown ids error loudly.
func (s *Store) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM memory WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete memory %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("delete memory %d: no such memory (see styx learn --list)", id)
	}
	return nil
}

// UpdateEvidence rewrites an item's text and refreshes created_at — the
// digest's dedupe path: a re-learned memory gets fresher evidence and
// recency instead of a duplicate row.
func (s *Store) UpdateEvidence(ctx context.Context, id int64, text string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE memory SET text = ?, created_at = ? WHERE id = ?`, text, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("update memory %d: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update memory %d: no such memory", id)
	}
	return nil
}

// MostSimilar returns the same-kind item with the highest cosine similarity
// to vec. Similarity 0 (zero Item) when the store holds no items of kind.
func (s *Store) MostSimilar(ctx context.Context, kind Kind, vec []float32) (Item, float64, error) {
	items, err := s.All(ctx)
	if err != nil {
		return Item{}, 0, err
	}
	var best Item
	bestSim := 0.0
	for _, it := range items {
		if it.Kind != kind {
			continue
		}
		if sim := cosine(vec, it.Embedding); sim > bestSim {
			best, bestSim = it, sim
		}
	}
	return best, bestSim, nil
}
