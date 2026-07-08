package activity

import (
	"fmt"
	"strings"
	"time"
)

// DefaultStall is the idle duration past which an agent is flagged ⚠.
const DefaultStall = 90 * time.Second

// Render paints one line per agent plus an optional watcher note. now is
// injected so output is deterministic. A live agent shows "▸ <last> <idle> ago";
// past the stall threshold it flips to "⚠ idle <idle>"; a finished agent shows
// "✓ done (<elapsed>)".
func Render(states []AgentState, note string, stall time.Duration, now time.Time) []string {
	out := make([]string, 0, len(states)+1)
	for _, s := range states {
		label := displayLabel(s.Label)
		if s.Done {
			out = append(out, fmt.Sprintf("%-8s ✓ done (%s)", label, short(s.Elapsed)))
			continue
		}
		idle := now.Sub(s.LastAt)
		if idle > stall {
			out = append(out, fmt.Sprintf("%-8s ⚠ idle %-6s (last: %s)", label, short(idle), s.Last))
			continue
		}
		out = append(out, fmt.Sprintf("%-8s ▸ %-30s %s ago", label, s.Last, short(idle)))
	}
	if note != "" {
		out = append(out, "watch (ollama): "+note)
	}
	return out
}

// displayLabel strips the "<projectID>/" namespace prefix that Manager applies
// to board keys (agent.BoardLabel), leaving the bare thread name for display.
// Labels without a slash (e.g. raw test entries) pass through unchanged.
func displayLabel(label string) string {
	if i := strings.LastIndex(label, "/"); i >= 0 {
		return label[i+1:]
	}
	return label
}

// short renders a duration as 2s / 4m03s / 1h12m.
func short(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}
