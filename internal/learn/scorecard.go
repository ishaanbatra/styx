// Package learn implements styx's self-improvement loop: a deterministic
// scorecard over dispatch outcomes, and an ollama-backed digest that turns
// the scorecard + session retrospectives into plain-text preference memories
// with provenance. Learning is additive and inspectable — never code
// changes, never routing.toml edits (the transparent table is absolute).
package learn

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ishaanbatra/styx/internal/budget"
)

// Cell is one cli × signal aggregate of the trailing outcome window.
type Cell struct {
	CLI             string
	Signal          string // "(none)" for signal-less dispatches
	Attempts        int
	Clean           int // no classified error AND not rated bad
	MedianDurationS float64
	MedianTokens    int // median of tokens_in + tokens_out
	Good, Bad       int // rating tallies
}

// Scorecard is the deterministic layer of styx learn: pure aggregation, no
// LLM involvement, independently useful via styx learn --scorecard. The
// digest consumes it as ground truth (evidence guard).
type Scorecard struct {
	WindowDays int
	Cells      []Cell // sorted by CLI, then Signal
}

// Build aggregates outcome rows into cli × signal cells. A row with N
// signals contributes to N cells; a row with none lands in "(none)".
func Build(rows []budget.Outcome, windowDays int) Scorecard {
	type acc struct {
		cell      Cell
		durations []float64
		tokens    []int
	}
	byKey := map[string]*acc{}
	for _, o := range rows {
		sigs := strings.Split(o.Signals, ",")
		var clean []string
		for _, s := range sigs {
			if s = strings.TrimSpace(s); s != "" {
				clean = append(clean, s)
			}
		}
		if len(clean) == 0 {
			clean = []string{"(none)"}
		}
		for _, sig := range clean {
			key := o.CLI + "\x00" + sig
			a, ok := byKey[key]
			if !ok {
				a = &acc{cell: Cell{CLI: o.CLI, Signal: sig}}
				byKey[key] = a
			}
			a.cell.Attempts++
			if o.ErrorKind == "" && o.Rating != "bad" {
				a.cell.Clean++
			}
			switch o.Rating {
			case "good":
				a.cell.Good++
			case "bad":
				a.cell.Bad++
			}
			a.durations = append(a.durations, o.DurationS)
			a.tokens = append(a.tokens, o.TokensIn+o.TokensOut)
		}
	}
	sc := Scorecard{WindowDays: windowDays}
	for _, a := range byKey {
		a.cell.MedianDurationS = medianF(a.durations)
		a.cell.MedianTokens = medianI(a.tokens)
		sc.Cells = append(sc.Cells, a.cell)
	}
	sort.Slice(sc.Cells, func(i, j int) bool {
		if sc.Cells[i].CLI != sc.Cells[j].CLI {
			return sc.Cells[i].CLI < sc.Cells[j].CLI
		}
		return sc.Cells[i].Signal < sc.Cells[j].Signal
	})
	return sc
}

// HasCell reports whether a cli × signal line exists — the mechanical
// evidence guard's ground truth for "scorecard:<cli>/<signal>" citations.
func (s Scorecard) HasCell(cli, signal string) bool {
	for _, c := range s.Cells {
		if c.CLI == cli && c.Signal == signal {
			return true
		}
	}
	return false
}

// Render prints one line per cell, stable order.
func (s Scorecard) Render() string {
	if len(s.Cells) == 0 {
		return fmt.Sprintf("scorecard (trailing %dd): no dispatch outcomes yet", s.WindowDays)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "scorecard (trailing %dd, cli × signal):\n", s.WindowDays)
	for _, c := range s.Cells {
		pct := 0
		if c.Attempts > 0 {
			pct = c.Clean * 100 / c.Attempts
		}
		fmt.Fprintf(&b, "  %s × %s: %d/%d clean (%d%%), median %.1fs, %d tok, rated +%d/-%d\n",
			c.CLI, c.Signal, c.Clean, c.Attempts, pct, c.MedianDurationS, c.MedianTokens, c.Good, c.Bad)
	}
	return b.String()
}

func medianF(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	if n := len(v); n%2 == 1 {
		return v[n/2]
	} else {
		return (v[n/2-1] + v[n/2]) / 2
	}
}

func medianI(v []int) int {
	if len(v) == 0 {
		return 0
	}
	sort.Ints(v)
	if n := len(v); n%2 == 1 {
		return v[n/2]
	} else {
		return (v[n/2-1] + v[n/2]) / 2
	}
}
