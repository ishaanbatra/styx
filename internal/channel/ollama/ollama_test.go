package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

	c := NewWithBaseURL(srv.URL, "5m")
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
	c := NewWithBaseURL(srv.URL, "17m")
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
	if gotBody["keep_alive"] != "17m" {
		t.Errorf("keep_alive = %v, want configured 17m", gotBody["keep_alive"])
	}
}

func TestSend_SetsNumCtxForLargePrompts(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"message":{"content":"ok"}}`))
	}))
	defer srv.Close()
	c := NewWithBaseURL(srv.URL, "5m")
	// ~24k chars ≈ 6k estimated tokens > the 4096 ollama default.
	big := strings.Repeat("x ", 12000)
	_, err := c.Send(context.Background(), channel.Request{Model: "qwen2.5-coder:14b", Prompt: big})
	if err != nil {
		t.Fatal(err)
	}
	opts, _ := gotBody["options"].(map[string]any)
	if opts == nil || opts["num_ctx"] == nil {
		t.Fatal("large prompts must set options.num_ctx (ollama default 4096 silently truncates)")
	}
}

func TestSend_NetworkErrorIsClassified(t *testing.T) {
	old := goos
	goos = "linux" // skip the darwin auto-launch + 20s poll
	defer func() { goos = old }()
	c := NewWithBaseURL("http://127.0.0.1:1", "5m") // unreachable
	_, err := c.Send(context.Background(), channel.Request{Model: "x", Prompt: "y"})
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *channel.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClassifiedError, got %v", err)
	}
}

func TestEnsureUp_NonDarwinFailsFastWithoutLaunch(t *testing.T) {
	for _, g := range []string{"linux", "windows"} {
		t.Run(g, func(t *testing.T) {
			old := goos
			goos = g
			defer func() { goos = old }()
			c := NewWithBaseURL("http://127.0.0.1:1", "5m") // unreachable
			start := time.Now()
			_, err := c.Send(context.Background(), channel.Request{Model: "x", Prompt: "y"})
			if err == nil || !strings.Contains(err.Error(), "start it manually") {
				t.Fatalf("want manual-start error, got %v", err)
			}
			if time.Since(start) > 5*time.Second {
				t.Errorf("non-darwin path must fail fast, took %v", time.Since(start))
			}
		})
	}
}

func TestSend_HonorsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewWithBaseURL(srv.URL, "5m")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Send(ctx, channel.Request{Model: "x", Prompt: "y"})
	if err == nil || !strings.Contains(err.Error(), "context") {
		t.Errorf("expected context error, got %v", err)
	}
}
