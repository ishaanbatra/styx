// Package microtask runs bounded, validated model prose tasks with at most one
// explicitly supplied fallback. Routing and publication remain caller-owned.
package microtask

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ishaanbatra/styx/internal/channel"
)

// Target is one already-routed model destination.
type Target struct {
	Channel channel.Channel
	Name    string
	Model   string
	Effort  string
	// Escalated marks a target the router had already degraded to before this
	// bounded run began.
	Escalated bool
}

// Attempt captures the observable result of one model call.
type Attempt struct {
	Channel         string
	Model           string
	Duration        time.Duration
	TokensIn        int
	TokensOut       int
	ErrorKind       string
	Error           string
	ValidationError string
	Escalated       bool
	SendSucceeded   bool
	Validated       bool
}

// Result is the validated value plus attempt metadata.
type Result[T any] struct {
	Value          T
	Attempts       []Attempt
	UsedFallback   bool
	StaticFallback bool
}

// Options configure one primary -> optional fallback -> static result run.
type Options[T any] struct {
	Prompt     string
	WorkingDir string
	Primary    Target
	// Fallback resolves the optional escalation target immediately before it
	// would be sent, allowing callers to recheck live budget/breaker state.
	Fallback func(context.Context) *Target
	Parse    func(string) (T, error)
	Validate func(T) error
	Static   T
}

// Run calls the primary and at most one fallback. Any send, parse, or
// validation failure advances the bounded chain; exhaustion returns Static.
func Run[T any](ctx context.Context, o Options[T]) Result[T] {
	result := Result[T]{Value: o.Static}
	for i := 0; i < 2; i++ {
		target := o.Primary
		if i > 0 {
			if o.Fallback == nil {
				break
			}
			resolved := o.Fallback(ctx)
			if resolved == nil {
				break
			}
			target = *resolved
		}
		attempt := Attempt{Channel: target.Name, Model: target.Model, Escalated: target.Escalated || i > 0}
		if target.Channel == nil {
			attempt.ErrorKind = "other"
			attempt.Error = fmt.Sprintf("channel %q is unavailable", target.Name)
			result.Attempts = append(result.Attempts, attempt)
			continue
		}
		started := time.Now()
		resp, err := target.Channel.Send(ctx, channel.Request{
			Model: target.Model, Effort: target.Effort, Prompt: o.Prompt,
			WorkingDir: o.WorkingDir,
		})
		attempt.Duration = time.Since(started)
		attempt.TokensIn = resp.EstTokensIn
		attempt.TokensOut = resp.EstTokensOut
		if err != nil {
			attempt.Error = err.Error()
			attempt.ErrorKind = errorKind(err)
			result.Attempts = append(result.Attempts, attempt)
			continue
		}
		attempt.SendSucceeded = true
		if o.Parse == nil {
			attempt.ValidationError = "parse function is required"
			attempt.ErrorKind = "validation"
			result.Attempts = append(result.Attempts, attempt)
			continue
		}
		value, err := o.Parse(resp.Text)
		if err == nil && o.Validate != nil {
			err = o.Validate(value)
		}
		if err != nil {
			attempt.ValidationError = err.Error()
			attempt.ErrorKind = "validation"
			result.Attempts = append(result.Attempts, attempt)
			continue
		}
		result.Attempts = append(result.Attempts, attempt)
		result.Attempts[len(result.Attempts)-1].Validated = true
		result.Value = value
		result.UsedFallback = i > 0
		return result
	}
	result.StaticFallback = true
	return result
}

func errorKind(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	var classified *channel.ClassifiedError
	if errors.As(err, &classified) {
		return string(classified.Kind)
	}
	return "other"
}

// TargetString returns a stable channel:model description for diagnostics.
func TargetString(t Target) string {
	if t.Model == "" {
		return t.Name
	}
	return fmt.Sprintf("%s:%s", t.Name, t.Model)
}
