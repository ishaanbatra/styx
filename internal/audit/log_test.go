package audit

import (
	"path/filepath"
	"testing"
)

func TestAppendAndTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := 0; i < 3; i++ {
		if err := l.Append(Record{Kind: KindTurn, Detail: "u"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Append(Record{Kind: KindDecision, Detail: "dispatch"}); err != nil {
		t.Fatal(err)
	}
	recs, err := l.Tail(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("tail = %d, want 2", len(recs))
	}
	if recs[1].Kind != KindDecision || recs[1].Detail != "dispatch" {
		t.Errorf("last record = %+v", recs[1])
	}
	if recs[0].At.IsZero() {
		t.Error("At not stamped")
	}
}

func TestRecordCarriesProject(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.jsonl")
	l, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := l.Append(Record{Kind: KindDispatch, Detail: "claude·opus", Project: "pid123"}); err != nil {
		t.Fatal(err)
	}
	recs, err := l.Tail(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Project != "pid123" {
		t.Errorf("project not round-tripped: %+v", recs)
	}
}
