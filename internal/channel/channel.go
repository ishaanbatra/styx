// Package channel defines the abstraction every model provider implements
// so the router can treat them interchangeably.
package channel

import (
	"context"
	"time"
)

// Channel is the provider abstraction (Claude, Codex, Gemini, Ollama).
type Channel interface {
	Name() string
	Send(ctx context.Context, req Request) (Response, error)
	BudgetState(ctx context.Context) (Budget, error)
}

// Request is a single outbound call.
type Request struct {
	Model       string       // provider-specific identifier ("sonnet-4-6", "qwen2.5-coder:14b")
	Effort      string       // optional reasoning-effort, pass-through to the channel CLI
	System      string       // optional system prompt
	Prompt      string       // user prompt
	Attachments []Attachment // file contents to inline-include
	Interactive bool         // if true, exec interactively (build verb); response will be empty
	WorkingDir  string       // execute relative to this dir (used for interactive verbs)
	Write       bool         // if true, the channel may autonomously edit files / run commands (implement verb)
}

// Attachment is a file the channel should consider as context.
type Attachment struct {
	Path string
	Mime string // e.g. "text/markdown"; optional
}

// Response is the channel's reply.
type Response struct {
	Text         string
	EstTokensIn  int
	EstTokensOut int
}

// Budget is the channel's current spend posture.
type Budget struct {
	UsedPct       float64
	LimitHit      bool
	CooldownUntil time.Time
}

// ErrorKind classifies a Send error for telemetry. Channels SHOULD wrap their
// errors so the router can call ErrorKind(err) to label them.
type ErrorKindLabel string

const (
	ErrKindTimeout   ErrorKindLabel = "timeout"
	ErrKindRateLimit ErrorKindLabel = "429"
	ErrKindServer    ErrorKindLabel = "5xx"
	ErrKindOther     ErrorKindLabel = "other"
)

// ClassifiedError lets channels emit a structured error kind.
type ClassifiedError struct {
	Kind ErrorKindLabel
	Err  error
}

func (c *ClassifiedError) Error() string { return c.Err.Error() }
func (c *ClassifiedError) Unwrap() error { return c.Err }
