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
    last_used_at INTEGER NOT NULL DEFAULT 0
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
		`SELECT id, kind, text, source, project, scope, confidence, embedding, created_at, last_used_at FROM memory ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query memory: %w", err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var kind string
		var blob []byte
		var ts, lastUsed int64
		if err := rows.Scan(&it.ID, &kind, &it.Text, &it.Source, &it.Project, &it.Scope, &it.Confidence, &blob, &ts, &lastUsed); err != nil {
			return nil, fmt.Errorf("scan memory item: %w", err)
		}
		it.Kind = Kind(kind)
		it.Embedding = decodeVec(blob)
		it.CreatedAt = time.Unix(ts, 0)
		if lastUsed != 0 {
			it.LastUsedAt = time.Unix(lastUsed, 0)
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
