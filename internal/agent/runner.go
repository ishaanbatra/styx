package agent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// TurnResult is the outcome of one agent turn.
type TurnResult struct {
	Text         string
	InputTokens  int
	OutputTokens int
}

// Runner executes one turn of one thread: invoke the CLI fresh (per-turn
// resume), stream events, capture session id and real token usage.
type Runner struct {
	Adapter Adapter
	Thread  *Thread
	WorkDir string
	Timeout time.Duration // 0 = no timeout
	OnEvent func(Event)   // streaming callback (REPL prints); may be nil
}

// Send runs one turn. The thread's SessionID and context meter are updated
// in place; the caller is responsible for persisting the ThreadStore.
func (r *Runner) Send(ctx context.Context, msg, model string, extra []string) (TurnResult, error) {
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	args := r.Adapter.BuildArgs(msg, r.Thread.SessionID, model, extra)
	cmd := exec.CommandContext(ctx, r.Adapter.Bin(), args...)
	if r.WorkDir != "" {
		cmd.Dir = r.WorkDir
	}

	if !r.Adapter.SupportsStream() {
		out, err := cmd.Output()
		if err != nil {
			return TurnResult{}, classifyTurnError(r.Adapter.CLI(), err)
		}
		text := strings.TrimRight(string(out), "\n")
		res := TurnResult{Text: text, InputTokens: len(msg) / 4, OutputTokens: len(text) / 4}
		r.finish(res)
		return res, nil
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return TurnResult{}, fmt.Errorf("pipe %s stdout: %w", r.Adapter.CLI(), err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return TurnResult{}, classifyTurnError(r.Adapter.CLI(), err)
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // results can be large
	var res TurnResult
	var resultErr bool
	for sc.Scan() {
		ev, ok := r.Adapter.ParseEvent(sc.Bytes())
		if !ok {
			continue
		}
		if r.OnEvent != nil {
			r.OnEvent(ev)
		}
		switch ev.Type {
		case EventInit:
			r.Thread.SessionID = ev.SessionID
		case EventResult:
			res.Text = ev.Text
			res.InputTokens = ev.InputTokens
			res.OutputTokens = ev.OutputTokens
			resultErr = ev.IsError
			if ev.SessionID != "" {
				r.Thread.SessionID = ev.SessionID
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return TurnResult{}, fmt.Errorf("%s turn failed: %w: %s",
			r.Adapter.CLI(), err, strings.TrimSpace(stderr.String()))
	}
	if scanErr := sc.Err(); scanErr != nil {
		return TurnResult{}, fmt.Errorf("read %s stream: %w", r.Adapter.CLI(), scanErr)
	}
	if resultErr {
		r.finish(res) // usage is still real; meter it
		return res, fmt.Errorf("%s reported an error result: %s", r.Adapter.CLI(), res.Text)
	}
	r.finish(res)
	return res, nil
}

func (r *Runner) finish(res TurnResult) {
	r.Thread.ContextTokens = res.InputTokens + res.OutputTokens
	r.Thread.Turns++
	r.Thread.UpdatedAt = time.Now()
}

func classifyTurnError(cli string, err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Errorf("%s exited %d: %s", cli, ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("run %s: %w", cli, err)
}
