package learn

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/budget"
)

// scriptedOllama returns an httptest server answering /api/chat with the
// given candidates payload, capturing the request for assertions.
func scriptedOllama(t *testing.T, candidates string) (*httptest.Server, *map[string]any) {
	t.Helper()
	captured := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]any{"content": candidates},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &captured
}

func TestDigesterPropose(t *testing.T) {
	srv, captured := scriptedOllama(t, `{"candidates":[
		{"kind":"routing-preference","text":"codex for specced work","confidence":0.8,"evidence":"scorecard:codex/complex"}
	]}`)
	d := &Digester{BaseURL: srv.URL, Model: "qwen2.5-coder:7b", KeepAlive: "17m"}
	got, err := d.Propose(context.Background(),
		"scorecard text", []RetroNote{{ID: 7, Text: "codex nailed it"}}, []string{"good (codex): clean"})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if len(got) != 1 || got[0].Kind != "routing-preference" || got[0].Evidence != "scorecard:codex/complex" {
		t.Fatalf("candidates mismatch: %+v", got)
	}
	// The request is a schema-constrained, non-thinking, keep-alive chat and
	// the prompt carries all three feeds.
	req := *captured
	if req["think"] != false || req["keep_alive"] != "17m" || req["format"] == nil {
		t.Fatalf("chat request must mirror the brain's shape (think=false, keep_alive, format), got %v", req)
	}
	msgs, _ := req["messages"].([]any)
	user, _ := msgs[1].(map[string]any)["content"].(string)
	for _, want := range []string{"scorecard text", "retro:7", "codex nailed it", "good (codex): clean"} {
		if !strings.Contains(user, want) {
			t.Fatalf("prompt missing %q:\n%s", want, user)
		}
	}
}

func TestDigesterFailsLoudWhenOllamaDown(t *testing.T) {
	d := &Digester{BaseURL: "http://127.0.0.1:1", Model: "m"} // nothing listens
	if _, err := d.Propose(context.Background(), "sc", nil, nil); err == nil {
		t.Fatal("unreachable ollama must be a loud error")
	}
}

func TestFilterByEvidence(t *testing.T) {
	sc := Build([]budget.Outcome{{CLI: "codex", Signals: "complex"}}, 30)
	retros := []RetroNote{{ID: 7, Text: "note"}}
	cands := []Candidate{
		{Kind: "routing-preference", Text: "keep", Confidence: 0.8, Evidence: "scorecard:codex/complex"},
		{Kind: "user-preference", Text: "keep too", Confidence: 0.5, Evidence: "retro:7"},
		{Kind: "routing-preference", Text: "fabricated cell", Confidence: 0.9, Evidence: "scorecard:agy/huge"},
		{Kind: "user-preference", Text: "fabricated retro", Confidence: 0.9, Evidence: "retro:99"},
		{Kind: "decision", Text: "wrong kind", Confidence: 0.9, Evidence: "retro:7"},
		{Kind: "user-preference", Text: "no evidence", Confidence: 0.9, Evidence: ""},
		{Kind: "user-preference", Text: "bad confidence", Confidence: 1.5, Evidence: "retro:7"},
		{Kind: "user-preference", Text: "", Confidence: 0.5, Evidence: "retro:7"},
	}
	kept, dropped := FilterByEvidence(cands, sc, retros)
	if len(kept) != 2 || kept[0].Text != "keep" || kept[1].Text != "keep too" {
		t.Fatalf("guard kept the wrong set: %+v", kept)
	}
	if len(dropped) != 6 {
		t.Fatalf("want 6 drop reasons, got %d: %v", len(dropped), dropped)
	}

	// The 5-candidate cap truncates even valid extras.
	many := make([]Candidate, 8)
	for i := range many {
		many[i] = Candidate{Kind: "user-preference", Text: "t", Confidence: 0.5, Evidence: "retro:7"}
	}
	kept, dropped = FilterByEvidence(many, sc, retros)
	if len(kept) != 5 {
		t.Fatalf("cap must hold at 5, got %d", len(kept))
	}
	if len(dropped) != 3 || !strings.Contains(dropped[0], "cap") {
		t.Fatalf("over-cap drops must be reported, got %v", dropped)
	}
}
