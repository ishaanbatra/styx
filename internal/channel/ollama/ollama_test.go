package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func TestSend_ParsesChatResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":{"role":"assistant","content":"hi back"},"done":true}`))
	}))
	defer srv.Close()

	c := NewWithBaseURL(srv.URL)
	resp, err := c.Send(context.Background(), channel.Request{Model: "qwen2.5-coder:14b", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hi back" {
		t.Errorf("Text = %q, want %q", resp.Text, "hi back")
	}
}

func TestSend_EmitsCorrectPayload(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"message":{"content":"ok"}}`))
	}))
	defer srv.Close()
	c := NewWithBaseURL(srv.URL)
	_, err := c.Send(context.Background(), channel.Request{Model: "qwen2.5-coder:14b", System: "be terse", Prompt: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != "qwen2.5-coder:14b" {
		t.Errorf("model = %v, want qwen2.5-coder:14b", gotBody["model"])
	}
	if gotBody["stream"] != false {
		t.Errorf("stream = %v, want false", gotBody["stream"])
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2 (system + user)", len(msgs))
	}
}

func TestSend_NetworkErrorIsClassified(t *testing.T) {
	c := NewWithBaseURL("http://127.0.0.1:1") // unreachable
	_, err := c.Send(context.Background(), channel.Request{Model: "x", Prompt: "y"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *channel.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClassifiedError, got %v", err)
	}
}

func TestSend_HonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewWithBaseURL(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Send(ctx, channel.Request{Model: "x", Prompt: "y"})
	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Errorf("expected context error, got %v", err)
	}
}
