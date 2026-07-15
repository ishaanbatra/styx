package activity

import (
	"strings"
	"time"
)

// loopRun is the trailing identical-action count that merits model review.
const loopRun = 4

// signalSet is the mechanical read of one agent's recent board events.
type signalSet struct {
	ConsecutiveIdentical int
	DistinctRecent       int
	DistinctFiles        int
	Idle                 time.Duration
	EventsPerMin         float64
}

type verdict int

const (
	healthy verdict = iota
	suspicious
)

// classify computes deterministic activity signals. Only an identical-action
// run or an idle beyond stall reaches the LLM; changing work on one file is
// healthy regardless of how many edits or tests it takes.
func classify(s AgentState, now time.Time, stall time.Duration) (signalSet, verdict) {
	if stall <= 0 {
		stall = DefaultStall
	}
	sig := signalSet{Idle: now.Sub(s.LastAt)}
	if sig.Idle < 0 {
		sig.Idle = 0
	}

	distinct := map[string]struct{}{}
	files := map[string]struct{}{}
	for _, ev := range s.recentEvents {
		distinct[ev.Summary] = struct{}{}
		if target := fileTarget(ev.Summary); target != "" {
			files[target] = struct{}{}
		}
		if !ev.At.Before(now.Add(-time.Minute)) && !ev.At.After(now) {
			sig.EventsPerMin++
		}
	}
	sig.DistinctRecent = len(distinct)
	sig.DistinctFiles = len(files)

	for i := len(s.recentEvents) - 1; i >= 0; i-- {
		if s.recentEvents[i].Summary != s.Last {
			break
		}
		sig.ConsecutiveIdentical++
	}
	if sig.ConsecutiveIdentical >= loopRun || sig.Idle > stall {
		return sig, suspicious
	}
	return sig, healthy
}

// fileTarget recognizes file-oriented "<tool>: <target>" activity lines.
// Shell commands and other colon-bearing summaries are deliberately excluded.
func fileTarget(summary string) string {
	i := strings.Index(summary, ": ")
	if i < 0 {
		return ""
	}
	tool := strings.ToLower(strings.TrimSpace(summary[:i]))
	switch tool {
	case "edit", "write", "read", "multiedit", "apply_patch", "file_change":
		return strings.TrimSpace(summary[i+2:])
	default:
		return ""
	}
}
