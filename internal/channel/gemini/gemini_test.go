package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ishaanbatra/styx/internal/channel"
)

func fakeCLI(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSend_PrefersGeminiCLIWhenPresent(t *testing.T) {
	dir := fakeCLI(t, "gemini", `echo "from cli"`)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	c := New()
	resp, err := c.Send(context.Background(), channel.Request{Model: "flash", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "from cli" {
		t.Errorf("Text = %q, want from cli", resp.Text)
	}
}

func TestSend_FallsBackToHTTPWhenCLIMissing(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"from api"}]}}]}`))
	}))
	defer srv.Close()
	c := NewWithConfig(Config{APIBaseURL: srv.URL, APIKey: "test-key"})
	resp, err := c.Send(context.Background(), channel.Request{Model: "flash", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "from api" {
		t.Errorf("Text = %q, want from api", resp.Text)
	}
}

func TestSend_HTTPRequestUsesAPIKey(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	var gotKey string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`))
	}))
	defer srv.Close()
	c := NewWithConfig(Config{APIBaseURL: srv.URL, APIKey: "K123"})
	_, err := c.Send(context.Background(), channel.Request{Model: "flash", Prompt: "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "K123" {
		t.Errorf("api key = %q, want K123", gotKey)
	}
}
