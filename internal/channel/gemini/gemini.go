// Package gemini implements the Channel interface against Google's Gemini.
// Preferred path: `gemini-cli` (uses authenticated $20/mo subscription quota).
// Fallback path: direct HTTP to generativelanguage.googleapis.com using a
// Keychain-stored API key (free dev tier).
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/ishaanbatra/styx/internal/channel"
	"github.com/ishaanbatra/styx/internal/config"
)

const defaultAPIBase = "https://generativelanguage.googleapis.com/v1beta/models"

// Config injects test-time overrides.
type Config struct {
	APIBaseURL string // default: generativelanguage.googleapis.com/v1beta/models
	APIKey     string // when "", looked up from Keychain as "gemini_api_key"
	CLIName    string // default: "gemini"
}

type Channel struct {
	cfg    Config
	client *http.Client
}

func New() *Channel { return NewWithConfig(Config{}) }

func NewWithConfig(cfg Config) *Channel {
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = defaultAPIBase
	}
	if cfg.CLIName == "" {
		cfg.CLIName = "gemini"
	}
	return &Channel{cfg: cfg, client: &http.Client{Timeout: 5 * time.Minute}}
}

func (c *Channel) Name() string { return "gemini" }

func (c *Channel) BudgetState(ctx context.Context) (channel.Budget, error) {
	return channel.Budget{}, nil
}

func (c *Channel) Send(ctx context.Context, req channel.Request) (channel.Response, error) {
	if req.Interactive {
		return channel.Response{}, errors.New("gemini channel does not support interactive mode")
	}
	if _, err := exec.LookPath(c.cfg.CLIName); err == nil {
		return c.sendViaCLI(ctx, req)
	}
	return c.sendViaHTTP(ctx, req)
}

func (c *Channel) sendViaCLI(ctx context.Context, req channel.Request) (channel.Response, error) {
	args := []string{"-p", req.Prompt}
	if req.Model != "" {
		args = append([]string{"--model", req.Model}, args...)
	}
	cmd := exec.CommandContext(ctx, c.cfg.CLIName, args...)
	if req.WorkingDir != "" {
		cmd.Dir = req.WorkingDir
	}
	out, err := cmd.Output()
	if err != nil {
		return channel.Response{}, classifyExecError(err)
	}
	text := strings.TrimRight(string(out), "\n")
	return channel.Response{
		Text:         text,
		EstTokensIn:  estimateTokens(req.Prompt + req.System),
		EstTokensOut: estimateTokens(text),
	}, nil
}

type httpRequestBody struct {
	Contents []httpPart `json:"contents"`
}

type httpPart struct {
	Parts []struct {
		Text string `json:"text"`
	} `json:"parts"`
}

type httpResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
}

func (c *Channel) sendViaHTTP(ctx context.Context, req channel.Request) (channel.Response, error) {
	apiKey := c.cfg.APIKey
	if apiKey == "" {
		k, err := config.Secret("gemini_api_key")
		if err != nil {
			fallback := os.Getenv("GEMINI_API_KEY")
			if fallback == "" {
				return channel.Response{}, &channel.ClassifiedError{
					Kind: channel.ErrKindOther,
					Err:  fmt.Errorf("no gemini api key (set via styx migrate-secrets or GEMINI_API_KEY)"),
				}
			}
			apiKey = fallback
		} else {
			apiKey = k
		}
	}
	model := req.Model
	if model == "" {
		model = "gemini-2.5-flash"
	} else if model == "flash" {
		model = "gemini-2.5-flash"
	} else if model == "pro" {
		model = "gemini-2.5-pro"
	}

	body, _ := json.Marshal(httpRequestBody{
		Contents: []httpPart{{Parts: []struct {
			Text string `json:"text"`
		}{{Text: req.Prompt}}}},
	})
	url := fmt.Sprintf("%s/%s:generateContent?key=%s", c.cfg.APIBaseURL, model, apiKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return channel.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return channel.Response{}, &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return channel.Response{}, &channel.ClassifiedError{
			Kind: classifyStatus(resp.StatusCode),
			Err:  fmt.Errorf("gemini %d: %s", resp.StatusCode, string(raw)),
		}
	}
	var hr httpResponse
	if err := json.Unmarshal(raw, &hr); err != nil {
		return channel.Response{}, err
	}
	text := ""
	if len(hr.Candidates) > 0 && len(hr.Candidates[0].Content.Parts) > 0 {
		text = hr.Candidates[0].Content.Parts[0].Text
	}
	return channel.Response{
		Text:         text,
		EstTokensIn:  estimateTokens(req.Prompt + req.System),
		EstTokensOut: estimateTokens(text),
	}, nil
}

func estimateTokens(s string) int { return len(s) / 4 }

func classifyExecError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: err}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok {
			if status.Signal() == syscall.SIGKILL || status.Signal() == syscall.SIGTERM {
				return &channel.ClassifiedError{Kind: channel.ErrKindTimeout, Err: err}
			}
		}
		return &channel.ClassifiedError{Kind: channel.ErrKindOther, Err: fmt.Errorf("gemini exited %d: %s", ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))}
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
