package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

// fakeOllama serves /api/chat returning the queued contents in order.
func fakeOllama(t *testing.T, replies ...string) *httptest.Server {
	t.Helper()
	i := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["format"] == nil {
			t.Error("chat request missing format (structured output) field")
		}
		reply := replies[min(i, len(replies)-1)]
		i++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": map[string]string{"role": "assistant", "content": reply},
			"done":    true,
		})
	}))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestDecideHappyPath(t *testing.T) {
	srv := fakeOllama(t, `{"action":"dispatch","dispatches":[{"thread":"claude","model":"sonnet","message":"do it","rationale":"impl"}],"confidence":0.9}`)
	defer srv.Close()
	b := &Ollama{BaseURL: srv.URL, Model: "qwen3:4b", ConfidenceThreshold: 0.5}
	a, err := b.Decide(context.Background(), Turn{Utterance: "implement the fix"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if a.Action != ActionDispatch || a.Dispatches[0].Thread != "claude" {
		t.Errorf("action = %+v", a)
	}
}

func TestDecideRetriesOnceOnInvalidJSON(t *testing.T) {
	srv := fakeOllama(t,
		`not json at all`,
		`{"action":"reply","reply":"hi","confidence":0.8}`)
	defer srv.Close()
	b := &Ollama{BaseURL: srv.URL, Model: "qwen3:4b"}
	a, err := b.Decide(context.Background(), Turn{Utterance: "hello"})
	if err != nil {
		t.Fatalf("Decide after retry: %v", err)
	}
	if a.Action != ActionReply || a.Reply != "hi" {
		t.Errorf("action = %+v", a)
	}
}

func TestDecideErrNeedUserAfterTwoFailures(t *testing.T) {
	srv := fakeOllama(t, `garbage`, `more garbage`)
	defer srv.Close()
	b := &Ollama{BaseURL: srv.URL, Model: "qwen3:4b"}
	_, err := b.Decide(context.Background(), Turn{Utterance: "hello"})
	if !errors.Is(err, ErrNeedUser) {
		t.Fatalf("err = %v, want ErrNeedUser", err)
	}
}

// fakeChannel is a scripted channel.Channel for escalation tests.
type fakeChannel struct{ text string }

func (f *fakeChannel) Name() string { return "claude" }
func (f *fakeChannel) BudgetState(context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}
func (f *fakeChannel) Send(_ context.Context, _ channel.Request) (channel.Response, error) {
	return channel.Response{Text: f.text}, nil
}

func TestDecideEscalatesOnLowConfidence(t *testing.T) {
	srv := fakeOllama(t, `{"action":"escalate","confidence":0.1}`)
	defer srv.Close()
	esc := &ClaudeEscalator{
		Channel: &fakeChannel{text: "Here you go:\n{\"action\":\"dispatch\",\"dispatches\":[{\"thread\":\"codex\",\"message\":\"check it\"}],\"confidence\":0.95}"},
		Model:   "haiku",
	}
	b := &Ollama{BaseURL: srv.URL, Model: "qwen3:4b", ConfidenceThreshold: 0.5, Escalator: esc}
	a, err := b.Decide(context.Background(), Turn{Utterance: "ambiguous thing"})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if a.Action != ActionDispatch || a.Dispatches[0].Thread != "codex" {
		t.Errorf("escalated action = %+v", a)
	}
}

func TestDecideErrNeedUserWhenOllamaDown(t *testing.T) {
	b := &Ollama{BaseURL: "http://127.0.0.1:1", Model: "qwen3:4b"}
	_, err := b.Decide(context.Background(), Turn{Utterance: "hello"})
	if !errors.Is(err, ErrNeedUser) {
		t.Fatalf("err = %v, want ErrNeedUser", err)
	}
}
