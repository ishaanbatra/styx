// Package ollama implements the Channel interface against a local Ollama
// instance (http://localhost:11434 by default).
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/ishaanbatra/styx/internal/channel"
)

const defaultBaseURL = "http://localhost:11434"

// goos is a package-level alias so tests can exercise non-darwin behavior.
var goos = runtime.GOOS

// Channel is the Ollama implementation.
type Channel struct {
	baseURL string
	client  *http.Client
}

// New returns a Channel pointing at the default localhost endpoint.
func New() *Channel { return NewWithBaseURL(defaultBaseURL) }

// NewWithBaseURL is used in tests / non-default deployments.
func NewWithBaseURL(base string) *Channel {
	return &Channel{
		baseURL: strings.TrimRight(base, "/"),
		client:  &http.Client{Timeout: 15 * time.Minute},
	}
}

// Name implements channel.Channel.
func (c *Channel) Name() string { return "ollama" }

// BudgetState implements channel.Channel. Ollama is local-only and unlimited.
func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model     string         `json:"model"`
	Stream    bool           `json:"stream"`
	KeepAlive string         `json:"keep_alive,omitempty"`
	Options   map[string]any `json:"options,omitempty"`
	Messages  []chatMessage  `json:"messages"`
}

type chatResponse struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
	Done bool `json:"done"`
}

// Send implements channel.Channel.
func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return channel.Response{}, errors.New("ollama channel does not support interactive mode")
	}
	if err := c.ensureUp(ctx); err != nil {
		return channel.Response{}, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
	}

	msgs := []chatMessage{}
	if req.System != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: req.System})
	}
	prompt := req.Prompt
	for _, a := range req.Attachments {
		prompt += "\n\n--- FILE: " + a.Path + " ---\n" // attachments inlined for ollama
	}
	msgs = append(msgs, chatMessage{Role: "user", Content: prompt})

	creq := chatRequest{Model: req.Model, Stream: false, KeepAlive: "30m", Messages: msgs}
	if est := estimateTokens(prompt + req.System); est+1024 > 4096 {
		// Ollama defaults num_ctx to 4096 and silently truncates beyond it.
		creq.Options = map[string]any{"num_ctx": est + 2048}
	}
	body, err := json.Marshal(creq)
	if err != nil {
		return channel.Response{}, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return channel.Response{}, fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return channel.Response{}, classifyHTTPError(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return channel.Response{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return channel.Response{}, &channel.ClassifiedError{
			Kind: classifyStatus(resp.StatusCode),
			Err:  fmt.Errorf("ollama %d: %s", resp.StatusCode, string(raw)),
		}
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return channel.Response{}, fmt.Errorf("parse response: %w", err)
	}
	return channel.Response{
		Text:         cr.Message.Content,
		EstTokensIn:  estimateTokens(prompt + req.System),
		EstTokensOut: estimateTokens(cr.Message.Content),
	}, nil
}

func (c *Channel) ensureUp(ctx context.Context) error {
	if c.ping(ctx) {
		return nil
	}
	if goos != "darwin" {
		// Only macOS has an app bundle styx can auto-launch; elsewhere the
		// HTTP channel works iff ollama is already running.
		return fmt.Errorf("ollama is not responding at %s — start it manually (e.g. `ollama serve`)", c.baseURL)
	}
	// Try to launch the Ollama desktop app (macOS).
	_ = exec.CommandContext(ctx, "open", "-a", "Ollama").Run()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		if c.ping(ctx) {
			return nil
		}
	}
	return errors.New("Ollama did not respond on /api/tags after 20s")
}

func (c *Channel) ping(ctx context.Context) bool {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(pingCtx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return false
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

func estimateTokens(s string) int { return len(s) / 4 }

func classifyHTTPError(err error) error {
	if uerr, ok := err.(*url.Error); ok {
		if uerr.Timeout() {
			return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
		}
	}
	return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
}

func classifyStatus(code int) channel.ErrorKindLabel {
	switch {
	case code == http.StatusTooManyRequests:
		return channel.ErrKindRateLimit
	case code >= 500:
		return channel.ErrKindServer
	default:
		return channel.ErrKindOther
	}
}
