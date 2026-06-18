// Package audit records an append-only, per-session trail of what styx did:
// brain decisions, dispatches, pipeline runs, memory writes, and risk prompts.
package audit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Kind labels an audit record.
type Kind string

const (
	KindTurn        Kind = "turn"
	KindDecision    Kind = "decision"
	KindDispatch    Kind = "dispatch"
	KindPipeline    Kind = "pipeline"
	KindMemoryWrite Kind = "memory_write"
	KindRiskPrompt  Kind = "risk_prompt"
	KindError       Kind = "error"
)

// Record is one audited event.
type Record struct {
	At      time.Time         `json:"at"`
	Kind    Kind              `json:"kind"`
	Detail  string            `json:"detail"`
	Project string            `json:"project,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// Logger appends records to one session's JSONL file.
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// Open opens (creating if needed) the session log at path in append mode.
func Open(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}
	return &Logger{f: f, path: path}, nil
}

// Append writes one record, stamping the time if unset.
func (l *Logger) Append(r Record) error {
	if r.At.IsZero() {
		r.At = time.Now()
	}
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal audit record: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, err := l.f.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("write audit record: %w", err)
	}
	return nil
}

// Tail returns the last n records in chronological order.
func (l *Logger) Tail(n int) ([]Record, error) {
	if n <= 0 {
		return nil, nil
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return nil, fmt.Errorf("read audit log: %w", err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var recs []Record
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			continue
		}
		recs = append(recs, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan audit log: %w", err)
	}
	if len(recs) > n {
		recs = recs[len(recs)-n:]
	}
	return recs, nil
}

// Close releases the file handle.
func (l *Logger) Close() error {
	return l.f.Close()
}
