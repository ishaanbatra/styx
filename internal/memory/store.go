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
	ID        int64
	Kind      Kind
	Text      string
	Source    string // which thread/session/pipeline wrote it
	Embedding []float32
	CreatedAt time.Time
}

// Store is one SQLite memory database (per-project or global).
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS memory (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL,
    text       TEXT    NOT NULL,
    source     TEXT    NOT NULL DEFAULT '',
    embedding  BLOB    NOT NULL,
    created_at INTEGER NOT NULL
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
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Add inserts an item and returns its id.
func (s *Store) Add(ctx context.Context, it Item) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO memory (kind, text, source, embedding, created_at) VALUES (?, ?, ?, ?, ?)`,
		string(it.Kind), it.Text, it.Source, encodeVec(it.Embedding), time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("insert memory item: %w", err)
	}
	return res.LastInsertId()
}

// All returns every item in the store, newest first.
func (s *Store) All(ctx context.Context) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, text, source, embedding, created_at FROM memory ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query memory: %w", err)
	}
	defer rows.Close()
	var out []Item
	for rows.Next() {
		var it Item
		var kind string
		var blob []byte
		var ts int64
		if err := rows.Scan(&it.ID, &kind, &it.Text, &it.Source, &blob, &ts); err != nil {
			return nil, fmt.Errorf("scan memory item: %w", err)
		}
		it.Kind = Kind(kind)
		it.Embedding = decodeVec(blob)
		it.CreatedAt = time.Unix(ts, 0)
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
